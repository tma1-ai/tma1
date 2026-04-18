/* Sessions — waterfall chart (CC traces + fallback hooks/logs). */
/* globals: t, escapeHTML, fmtNum, fmtDurMs, fmtTokens, tsToMs, ganttColor,
            sess_scrollToToolUseId, sess_scrollToEvent */

var sess_waterfallFlat = null;

// ── Feature: Session Waterfall ─────────────────────────────────────────

// Build span list from CC trace data (enhanced telemetry).
// Returns {spans, sessionStart, sessionEnd} or null if no trace data.
function sess_buildCCTraceSpans(ccTraceSpans) {
  if (!ccTraceSpans || ccTraceSpans.length === 0) return null;

  var SPAN_TYPE_MAP = {
    'claude_code.interaction': 'agent',
    'claude_code.llm_request': 'llm',
    'claude_code.tool': 'tool',
    'claude_code.tool.blocked_on_user': 'wait',
    'claude_code.tool.execution': 'exec',
  };

  var spans = [];
  var spanById = {}; // span_id → trace span row
  var sessionStart = Infinity, sessionEnd = 0;

  for (var i = 0; i < ccTraceSpans.length; i++) {
    var tr = ccTraceSpans[i];
    spanById[tr.span_id] = tr;
    var startMs = tsToMs(tr.timestamp);
    var durMs = (Number(tr.duration_nano) || 0) / 1e6;
    var endMs = startMs + durMs;
    if (startMs < sessionStart) sessionStart = startMs;
    if (endMs > sessionEnd) sessionEnd = endMs;

    var spanType = SPAN_TYPE_MAP[tr.span_name] || 'tool';
    var parentId = tr.parent_span_id || 'root';

    // Determine label.
    var label = tr.tool_name || tr.span_name.replace('claude_code.', '');
    if (spanType === 'llm') label = 'LLM';
    if (spanType === 'wait') label = 'wait';
    if (spanType === 'exec') label = 'exec';
    if (spanType === 'agent') label = 'Turn';

    // Build data for click-to-expand.
    var data = {
      inputTokens: Number(tr.input_tokens) || 0,
      outputTokens: Number(tr.output_tokens) || 0,
      cacheTokens: Number(tr.cache_read_tokens) || 0,
      cacheCreationTokens: Number(tr.cache_creation_tokens) || 0,
      ttftMs: Number(tr.ttft_ms) || 0,
      speed: tr.speed || '',
      decision: tr.decision || '',
      source: tr.source || '',
      success: tr.success,
    };

    var failed = false;
    if (spanType === 'wait' && tr.decision === 'reject') failed = true;
    if (spanType === 'exec' && tr.success === false) failed = true;

    spans.push({
      id: tr.span_id,
      parentId: parentId,
      name: label,
      spanType: spanType,
      start_ts: startMs,
      end_ts: endMs,
      failed: failed,
      data: data,
    });
  }

  return { spans: spans, sessionStart: sessionStart, sessionEnd: sessionEnd };
}

function sess_renderWaterfall(timeline, stats) {
  if (timeline.length < 2 && !(stats.ccTraceSpans && stats.ccTraceSpans.length > 0)) return '';

  var sessionStart, sessionEnd, spans, totalMs;

  // Prefer CC trace data when available.
  var traceResult = sess_buildCCTraceSpans(stats.ccTraceSpans);
  if (traceResult) {
    spans = traceResult.spans;
    sessionStart = traceResult.sessionStart;
    sessionEnd = traceResult.sessionEnd;
    totalMs = Math.max(sessionEnd - sessionStart, 1);
  } else {
    // Fallback: build from hooks + OTel logs.
    sessionStart = timeline[0].ts;
    sessionEnd = timeline[timeline.length - 1].ts;
    for (var i = 0; i < timeline.length; i++) {
      if (timeline[i].source === 'tool_pair' && timeline[i].data.end_ts) {
        sessionEnd = Math.max(sessionEnd, timeline[i].data.end_ts);
      }
    }
    var apiCalls = stats.apiCalls || [];
    for (var ci = 0; ci < apiCalls.length; ci++) {
      var cEnd = (apiCalls[ci].ts || 0) + (apiCalls[ci].durationMs || 0);
      if (cEnd > sessionEnd) sessionEnd = cEnd;
    }

    // 1. Build subagent spans from hookEvents in timeline.
    var agentSpans = {};
    for (var ai = 0; ai < timeline.length; ai++) {
      var te = timeline[ai];
      if (te.source !== 'hook') continue;
      if (te.data.event_type === 'SubagentStart' && te.data.agent_id) {
        agentSpans[te.data.agent_id] = {
          id: te.data.agent_id, type: te.data.agent_type || 'subagent',
          start_ts: te.ts, end_ts: sessionEnd
        };
      } else if (te.data.event_type === 'SubagentStop' && te.data.agent_id && agentSpans[te.data.agent_id]) {
        agentSpans[te.data.agent_id].end_ts = te.ts;
      }
    }

    // 2. Build flat span list.
    spans = [];
    var agentIds = Object.keys(agentSpans);
    for (var si = 0; si < agentIds.length; si++) {
      var ag = agentSpans[agentIds[si]];
      spans.push({ id: ag.id, parentId: 'root', name: ag.type, spanType: 'agent', start_ts: ag.start_ts, end_ts: ag.end_ts, data: ag });
    }

    for (var ti = 0; ti < timeline.length; ti++) {
      var item = timeline[ti];
      if (item.source === 'tool_pair') {
        var pid = (item.data.agent_id && agentSpans[item.data.agent_id]) ? item.data.agent_id : 'root';
        spans.push({
          id: item.data.tool_use_id || ('tool_' + ti), parentId: pid,
          name: item.data.tool_name || '?', spanType: 'tool',
          start_ts: item.data.start_ts, end_ts: item.data.end_ts,
          failed: item.data.failed, data: item.data
        });
      }
    }

    // Add API calls as LLM spans.
    for (var ci2 = 0; ci2 < apiCalls.length; ci2++) {
      var c = apiCalls[ci2];
      var cTs = c.ts || 0;
      var cDur = c.durationMs || 0;
      spans.push({
        id: 'api_' + ci2, parentId: 'root',
        name: (c.model || 'LLM').replace(/^claude-/, '').replace(/-\d{8}$/, ''),
        spanType: 'llm', start_ts: cTs, end_ts: cTs + cDur, data: c
      });
    }
    totalMs = Math.max(sessionEnd - sessionStart, 1);
  }

  // 3. Build tree via byId map + DFS.
  var byId = { root: { children: [] } };
  for (var bi = 0; bi < spans.length; bi++) {
    byId[spans[bi].id] = { span: spans[bi], children: [] };
  }
  for (var li = 0; li < spans.length; li++) {
    var sp = spans[li];
    var parent = byId[sp.parentId] || byId.root;
    parent.children.push(byId[sp.id]);
  }

  var flat = [];
  function dfs(node, depth) {
    if (node.span) flat.push({ span: node.span, depth: depth });
    node.children.sort(function(a, b) { return a.span.start_ts - b.span.start_ts; });
    for (var di = 0; di < node.children.length; di++) dfs(node.children[di], depth + (node.span ? 1 : 0));
  }
  dfs(byId.root, 0);

  if (flat.length === 0) return '';
  sess_waterfallFlat = flat;

  // 4. Type counts for summary.
  var typeCounts = {};
  for (var fi = 0; fi < flat.length; fi++) {
    var st = flat[fi].span.spanType;
    typeCounts[st] = (typeCounts[st] || 0) + 1;
  }

  // 5. Render.
  var labelWidth = 220;
  var badgeLabels = { agent: t('sessions.span_agent'), tool: t('sessions.span_tool'), llm: t('sessions.span_llm'), message: t('sessions.span_message'), wait: t('sessions.span_wait'), exec: t('sessions.span_exec') };
  var html = '<details class="sess-section" open>';
  html += '<summary>' + t('sessions.waterfall') + '</summary>';
  html += '<div class="waterfall">';

  // Type summary.
  var typeKeys = Object.keys(typeCounts);
  if (typeKeys.length > 0) {
    html += '<div class="waterfall-type-summary">';
    for (var ki = 0; ki < typeKeys.length; ki++) {
      var k = typeKeys[ki];
      html += '<span class="span-badge span-badge-' + k + '">' + escapeHTML(badgeLabels[k] || k) + ' ' + typeCounts[k] + '</span> ';
    }
    html += '</div>';
  }

  for (var ri = 0; ri < flat.length; ri++) {
    var s = flat[ri].span;
    var depth = flat[ri].depth;
    var startMs = s.start_ts - sessionStart;
    var durMs = Math.max(s.end_ts - s.start_ts, 0);
    var leftPct = (startMs / totalMs * 100).toFixed(2);
    var widthPct = Math.max(durMs / totalMs * 100, 0.5).toFixed(2);
    var indent = depth * 16;

    var barClass = s.spanType;
    if (s.spanType === 'tool') barClass = s.failed ? 'error' : 'ok';
    if (s.spanType === 'wait') {
      if (s.data && s.data.decision === 'reject') barClass = 'wait-reject';
      else if (durMs > 1000) barClass = 'wait';
      else barClass = 'wait-auto';
    }
    if (s.spanType === 'exec') barClass = s.failed ? 'error' : 'exec';

    var durLabel = '';
    if (durMs >= 1000) durLabel = (durMs / 1000).toFixed(1) + 's';
    else if (durMs > 0) durLabel = Math.round(durMs) + 'ms';

    var barInner = (s.spanType !== 'message' && durMs >= 10) ? durLabel : '';

    // Badge
    var badge = '<span class="span-badge span-badge-' + s.spanType + '">' + escapeHTML(badgeLabels[s.spanType] || s.spanType) + '</span> ';

    // Token inline for LLM
    var tokenHtml = '';
    if (s.spanType === 'llm' && s.data) {
      var inTok = s.data.inputTokens || 0;
      var outTok = s.data.outputTokens || 0;
      if (inTok || outTok) tokenHtml = ' <span class="span-tokens">' + fmtNum(inTok) + '\u2192' + fmtNum(outTok) + '</span>';
      if (s.data.ttftMs > 0) tokenHtml += ' <span style="color:var(--purple);font-size:10px">TTFT ' + fmtDurMs(s.data.ttftMs) + '</span>';
    }
    // Wait: show decision + source inline.
    if (s.spanType === 'wait' && s.data && s.data.decision) {
      tokenHtml = ' <span style="color:var(--text-dim);font-size:10px">' + escapeHTML(s.data.decision) + ' \u00B7 ' + escapeHTML(s.data.source || '') + '</span>';
    }

    // Color for tool bars via inline style.
    var barStyle = 'left:' + leftPct + '%;width:' + widthPct + '%';
    if (s.spanType === 'tool' && !s.failed) barStyle += ';background:' + ganttColor(s.name);

    var linkAttrs = ' data-ts="' + Math.round(s.start_ts) + '"';
    // In hook-fallback mode s.id is the real tool_use_id (matches the
    // right-side timeline's data-tool-use-id exactly). In CC trace mode it's
    // the OTel span_id, which the timeline doesn't carry — skip there and
    // fall back to ts matching. Also exclude synthetic 'tool_<idx>' / 'api_<idx>' IDs.
    var hasToolUseId = !traceResult && s.spanType === 'tool' && s.id &&
      !/^(tool|api)_/.test(s.id);
    if (hasToolUseId) linkAttrs += ' data-tool-use-id="' + escapeHTML(s.id) + '"';
    html += '<div class="waterfall-row waterfall-row-clickable" role="button" tabindex="0" aria-expanded="false" data-wf-idx="' + ri + '"' + linkAttrs + '>';
    html += '<div class="waterfall-label" style="width:' + labelWidth + 'px;padding-left:' + indent + 'px" title="' + escapeHTML(s.name) + '">';
    html += (depth > 0 ? '<span style="color:var(--text-dim);margin-right:4px">\u2514</span>' : '');
    html += badge + escapeHTML(s.name) + tokenHtml;
    html += '</div>';
    html += '<div class="waterfall-track"><div class="waterfall-bar ' + barClass + '" style="' + barStyle + '">' + barInner + '</div></div>';
    html += '<div class="waterfall-dur">' + durLabel + '</div>';
    html += '</div>';
  }

  html += '</div></details>';
  return html;
}

// Attach click handlers for waterfall detail expansion after DOM insertion.
function sess_initWaterfallClicks() {
  var container = document.querySelector('.sess-insights-panel .waterfall');
  if (!container) return;
  container.addEventListener('click', function(e) {
    var row = e.target.closest('.waterfall-row-clickable');
    if (!row) return;
    var idx = parseInt(row.getAttribute('data-wf-idx'), 10);
    if (isNaN(idx)) return;
    e.stopPropagation();
    sess_scrollTimelineToRow(row);
    sess_toggleWaterfallDetail(container, row, idx);
  });
  container.addEventListener('keydown', function(e) {
    if (e.key !== 'Enter' && e.key !== ' ') return;
    var row = e.target.closest('.waterfall-row-clickable');
    if (!row) return;
    e.preventDefault();
    var idx = parseInt(row.getAttribute('data-wf-idx'), 10);
    if (isNaN(idx)) return;
    sess_scrollTimelineToRow(row);
    sess_toggleWaterfallDetail(container, row, idx);
  });
}

function sess_scrollTimelineToRow(row) {
  var tuid = row.getAttribute('data-tool-use-id');
  if (tuid) { sess_scrollToToolUseId(tuid); return; }
  var tsAttr = row.getAttribute('data-ts');
  if (!tsAttr) return;
  var scrollEl = document.getElementById('sess-timeline-scroll');
  if (scrollEl) sess_scrollToEvent(scrollEl, Number(tsAttr));
}

function sess_toggleWaterfallDetail(container, row, idx) {
  var existing = row.nextElementSibling;
  if (existing && existing.classList.contains('waterfall-span-detail')) {
    existing.remove();
    row.setAttribute('aria-expanded', 'false');
    return;
  }
  // Remove any other open detail.
  var prev = container.querySelectorAll('.waterfall-span-detail');
  for (var i = 0; i < prev.length; i++) {
    var pr = prev[i].previousElementSibling;
    if (pr) pr.setAttribute('aria-expanded', 'false');
    prev[i].remove();
  }

  // Retrieve span data from the global flat list built during render.
  if (!sess_waterfallFlat || !sess_waterfallFlat[idx]) return;
  var s = sess_waterfallFlat[idx].span;

  var pairs = [];
  pairs.push(['name', s.name]);
  pairs.push(['type', s.spanType]);
  if (s.id) pairs.push(['id', s.id]);
  if (s.parentId && s.parentId !== 'root') pairs.push(['parent', s.parentId]);
  var detailDurMs = s.end_ts - s.start_ts;
  if (detailDurMs > 0) pairs.push(['duration_ms', Math.round(detailDurMs)]);
  if (s.spanType === 'tool' && s.data) {
    if (s.data.tool_input) pairs.push(['input', s.data.tool_input.length > 500 ? s.data.tool_input.slice(0, 500) + '...' : s.data.tool_input]);
    if (s.data.tool_result) pairs.push(['result', s.data.tool_result.length > 500 ? s.data.tool_result.slice(0, 500) + '...' : s.data.tool_result]);
  }
  if (s.spanType === 'llm' && s.data) {
    if (s.data.model) pairs.push(['model', s.data.model]);
    if (s.data.inputTokens) pairs.push(['input_tokens', s.data.inputTokens]);
    if (s.data.outputTokens) pairs.push(['output_tokens', s.data.outputTokens]);
    if (s.data.cacheTokens) pairs.push(['cache_read_tokens', s.data.cacheTokens]);
    if (s.data.cacheCreationTokens) pairs.push(['cache_creation_tokens', s.data.cacheCreationTokens]);
    if (s.data.ttftMs) pairs.push(['ttft_ms', s.data.ttftMs]);
    if (s.data.speed) pairs.push(['speed', s.data.speed]);
    if (s.data.cost) pairs.push(['cost', '$' + s.data.cost.toFixed(4)]);
  }
  if (s.spanType === 'wait' && s.data) {
    if (s.data.decision) pairs.push(['decision', s.data.decision]);
    if (s.data.source) pairs.push(['source', s.data.source]);
  }
  if (s.spanType === 'exec' && s.data) {
    if (s.data.success != null) pairs.push(['success', s.data.success]);
  }
  if (s.spanType === 'agent') {
    pairs.push(['agent_type', s.name]);
  }

  var json = '{\n' + pairs.map(function(p) { return '  "' + p[0] + '": ' + JSON.stringify(p[1]); }).join(',\n') + '\n}';
  var detail = document.createElement('div');
  detail.className = 'waterfall-span-detail';
  detail.innerHTML = '<pre class="waterfall-span-json">' + escapeHTML(json) + '</pre>';
  row.after(detail);
  row.setAttribute('aria-expanded', 'true');
}
