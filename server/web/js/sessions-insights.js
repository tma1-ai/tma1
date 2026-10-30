/* Sessions — left-panel insight renderers (API calls, file heatmap, context bar, trace insights, agent tree). */
/* globals: t, escapeHTML, escapeJSString, fmtCost, fmtTokens, fmtNum, fmtDurMs, sess_parseAttrs, sess_scrollToToolUseId, sess_scrollToEvent */

// ── API Calls Section ─────────────────────────────────────────────────

function sess_renderAPICalls(stats) {
  var calls = stats.apiCalls;
  if (!calls.length) return '';

  var html = '<details class="sess-section">';
  html += '<summary>' + t(calls.some(function(call) { return call.usageRecord; }) ? 'sessions.usage_records' : 'sessions.api_calls') + ' (' + calls.length + ') \u00B7 ' + fmtCost(stats.cost) + '</summary>';
  html += '<table class="sess-api-table"><thead><tr>';
  html += '<th>' + t('sessions.api_col_model') + '</th><th>' + t('sessions.api_col_in') + '</th><th>' + t('sessions.api_col_out') + '</th><th>' + t('sessions.api_col_reasoning') + '</th><th>' + t('sessions.api_col_cache') + '</th><th>' + t('sessions.api_col_cost') + '</th><th>' + t('sessions.api_col_dur') + '</th>';
  html += '</tr></thead><tbody>';

  for (var i = 0; i < calls.length; i++) {
    var c = calls[i];
    var modelShort = (c.model || 'unknown').replace(/^claude-/, '').replace(/-\d{8}$/, '');
    var tuids = (c.toolUseIds || []).join(',');
    var apiKey = c.eventSeq != null ? 'seq:' + c.eventSeq : String(c.ts || 0);
    var clickAction = tuids
      ? 'sess_scrollToToolUseId(\x27' + escapeJSString(tuids.split(',')[0]) + '\x27)'
      : 'sess_scrollToEvent(document.getElementById(\x27sess-timeline-scroll\x27),' + (c.ts || 0) + ')';
    html += '<tr class="clickable" data-ts="' + (c.ts || 0) + '" data-fp="' + escapeHTML(apiKey) + '" data-tool-use-ids="' + escapeHTML(tuids) + '" onclick="' + escapeHTML(clickAction) + '">';
    html += '<td style="text-align:left;max-width:100px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="' + escapeHTML(c.model || '') + '">' + escapeHTML(modelShort) + '</td>';
    html += '<td>' + fmtTokens(c.inputTokens + (c.inputIncludesCache ? 0 : (c.cacheTokens || 0) + (c.cacheCreationTokens || 0))) + '</td>';
    html += '<td>' + fmtTokens(c.outputTokens) + '</td>';
    html += '<td>' + (c.reasoningTokens > 0 ? fmtTokens(c.reasoningTokens) : '\u2014') + '</td>';
    html += '<td>' + (c.cacheTokens > 0 ? fmtTokens(c.cacheTokens) : '\u2014') + '</td>';
    html += '<td>' + fmtCost(c.cost) + '</td>';
    html += '<td>' + (c.durationMs > 0 ? (c.durationMs < 1000 ? Math.round(c.durationMs) + 'ms' : (c.durationMs / 1000).toFixed(1) + 's') : '\u2014') + '</td>';
    html += '</tr>';
  }

  // Total row.
  html += '<tr class="sess-api-total">';
  html += '<td style="text-align:left">' + t('sessions.api_total') + '</td>';
  html += '<td>' + fmtTokens(stats.totalInputTokens) + '</td>';
  html += '<td>' + fmtTokens(stats.totalOutputTokens) + '</td>';
  html += '<td>' + (stats.totalReasoningTokens > 0 ? fmtTokens(stats.totalReasoningTokens) : '\u2014') + '</td>';
  html += '<td>' + (stats.totalCacheTokens > 0 ? fmtTokens(stats.totalCacheTokens) : '\u2014') + '</td>';
  html += '<td>' + fmtCost(stats.cost) + '</td>';
  html += '<td></td>';
  html += '</tr>';
  html += '</tbody></table>';

  if (stats.cacheHitRatio > 0) {
    html += '<div class="sess-api-cache">' + t('sessions.api_cache_hit') + ': ' + Math.round(stats.cacheHitRatio * 100) + '%</div>';
  }
  html += '</details>';
  return html;
}

// ── Feature: File Attention Heatmap ───────────────────────────────────

function sess_renderFileHeatmap(files) {
  var entries = [];
  for (var fp in files) entries.push({ path: fp, reads: files[fp].reads, writes: files[fp].writes, total: files[fp].reads + files[fp].writes });
  if (!entries.length) return '';
  entries.sort(function(a, b) { return b.total - a.total; });
  var maxTotal = entries[0].total;
  if (entries.length > 20) entries = entries.slice(0, 20);

  var html = '<details class="sess-section" open>';
  html += '<summary>' + t('sessions.files_touched') + ' (' + entries.length + ')</summary>';
  html += '<div class="sess-file-heatmap">';
  for (var i = 0; i < entries.length; i++) {
    var e = entries[i];
    var readPct = (e.reads / maxTotal * 100).toFixed(1);
    var writePct = (e.writes / maxTotal * 100).toFixed(1);
    var parts = e.path.split('/');
    var shortPath = parts.length > 3 ? '\u2026/' + parts.slice(-3).join('/') : e.path;
    html += '<div class="sess-file-row">';
    html += '<span class="sess-file-path" title="' + escapeHTML(e.path) + '">' + escapeHTML(shortPath) + '</span>';
    html += '<div class="sess-file-bar-wrap">';
    if (e.reads > 0) html += '<div class="sess-file-bar-read" style="width:' + readPct + '%"></div>';
    if (e.writes > 0) html += '<div class="sess-file-bar-write" style="width:' + writePct + '%"></div>';
    html += '</div>';
    html += '<span class="sess-file-count">' + e.total + '</span>';
    html += '</div>';
  }
  html += '</div></details>';
  return html;
}

// ── Feature: Context Window Breakdown ─────────────────────────────────

function sess_renderContextBar(ctx) {
  var total = ctx.system + ctx.user + ctx.tools + ctx.reasoning + ctx.subagent;
  if (total === 0) return '';

  var segments = [
    { key: 'system', tokens: ctx.system, color: 'var(--purple)' },
    { key: 'user', tokens: ctx.user, color: 'var(--blue)' },
    { key: 'tools', tokens: ctx.tools, color: 'var(--green)' },
    { key: 'reasoning', tokens: ctx.reasoning, color: 'var(--orange)' },
    { key: 'subagent', tokens: ctx.subagent, color: 'var(--red)' },
  ];

  var html = '<div class="sess-section">';
  html += '<div class="sess-section-label">' + t('sessions.context_window') + ' (' + fmtTokens(total) + ' tokens)</div>';
  html += '<div class="sess-ctx-bar">';
  for (var i = 0; i < segments.length; i++) {
    var s = segments[i];
    if (s.tokens <= 0) continue;
    var pct = (s.tokens / total * 100).toFixed(1);
    html += '<div class="sess-ctx-seg" style="width:' + pct + '%;background:' + s.color + '" title="' + t('sessions.ctx_' + s.key) + ': ' + fmtTokens(s.tokens) + '"></div>';
  }
  html += '</div>';
  html += '<div class="sess-ctx-legend">';
  for (var j = 0; j < segments.length; j++) {
    var sg = segments[j];
    if (sg.tokens <= 0) continue;
    html += '<span><span class="sess-ctx-dot" style="background:' + sg.color + '"></span>' + t('sessions.ctx_' + sg.key) + ' ' + fmtTokens(sg.tokens) + '</span>';
  }
  html += '</div></div>';
  return html;
}

// ── Feature: CC Trace Insights (Turn Performance + Permissions) ───────

function sess_renderTraceInsights(spans) {
  var llmSpans = spans.filter(function(s) { return s.span_name === 'claude_code.llm_request'; });
  var waitSpans = spans.filter(function(s) { return s.span_name === 'claude_code.tool.blocked_on_user'; });
  var toolSpans = spans.filter(function(s) { return s.span_name === 'claude_code.tool'; });
  var execSpans = spans.filter(function(s) { return s.span_name === 'claude_code.tool.execution'; });

  if (llmSpans.length === 0) return '';

  // Compute metrics.
  var avgTTFT = 0, totalCacheRead = 0, totalCacheCreate = 0, totalInput = 0;
  var ttftValues = [];
  for (var i = 0; i < llmSpans.length; i++) {
    var s = llmSpans[i];
    var ttft = Number(s.ttft_ms) || 0;
    if (ttft > 0) { avgTTFT += ttft; ttftValues.push(ttft); }
    totalCacheRead += Number(s.cache_read_tokens) || 0;
    totalCacheCreate += Number(s.cache_creation_tokens) || 0;
    totalInput += Number(s.input_tokens) || 0;
  }
  if (ttftValues.length > 0) avgTTFT = Math.round(avgTTFT / ttftValues.length);

  var avgExec = 0;
  for (var ei = 0; ei < execSpans.length; ei++) {
    avgExec += (Number(execSpans[ei].duration_nano) || 0) / 1e6;
  }
  if (execSpans.length > 0) avgExec = Math.round(avgExec / execSpans.length);

  var totalWait = 0;
  for (var wi = 0; wi < waitSpans.length; wi++) {
    totalWait += (Number(waitSpans[wi].duration_nano) || 0) / 1e6;
  }

  // Turn Performance card.
  var html = '<details class="sess-section" open>';
  html += '<summary>' + t('sessions.turn_performance') + '</summary>';
  html += '<div style="display:grid;grid-template-columns:1fr 1fr;gap:4px 16px;font-size:12px;padding:8px 0">';
  html += '<div><span style="color:var(--text-dim)">' + t('sessions.llm_calls') + '</span></div><div style="text-align:right">' + llmSpans.length + '</div>';
  html += '<div><span style="color:var(--text-dim)">' + t('sessions.trace_tool_calls') + '</span></div><div style="text-align:right">' + toolSpans.length + '</div>';
  html += '<div><span style="color:var(--text-dim)">' + t('sessions.avg_ttft') + '</span></div><div style="text-align:right;color:var(--purple)">' + fmtDurMs(avgTTFT) + '</div>';
  html += '<div><span style="color:var(--text-dim)">' + t('sessions.avg_exec') + '</span></div><div style="text-align:right">' + fmtDurMs(avgExec) + '</div>';
  html += '<div><span style="color:var(--text-dim)">' + t('sessions.perm_wait') + '</span></div><div style="text-align:right;color:var(--orange)">' + fmtDurMs(Math.round(totalWait)) + '</div>';
  html += '</div>';

  // TTFT sparkline (inline SVG).
  if (ttftValues.length > 1) {
    var maxTTFT = Math.max.apply(null, ttftValues);
    var svgW = 200, svgH = 16;
    var step = svgW / (ttftValues.length - 1);
    var points = ttftValues.map(function(v, idx) {
      return Math.round(idx * step) + ',' + Math.round(svgH - (v / maxTTFT) * svgH);
    }).join(' ');
    html += '<svg width="' + svgW + '" height="' + svgH + '" style="display:block;margin:4px 0">';
    html += '<polyline points="' + points + '" fill="none" stroke="var(--purple)" stroke-width="1.5" />';
    html += '</svg>';
  }

  // Token flow stacked bar.
  var totalTok = totalCacheRead + totalCacheCreate + totalInput;
  if (totalTok > 0) {
    var pctRead = (totalCacheRead / totalTok * 100).toFixed(1);
    var pctCreate = (totalCacheCreate / totalTok * 100).toFixed(1);
    html += '<div style="font-size:11px;color:var(--text-dim);margin-top:8px">' + t('sessions.token_flow') + '</div>';
    html += '<div style="display:flex;height:10px;border-radius:3px;overflow:hidden;margin:4px 0">';
    html += '<div style="width:' + pctRead + '%;background:var(--green)" title="Cache Read: ' + fmtNum(totalCacheRead) + '"></div>';
    html += '<div style="width:' + pctCreate + '%;background:var(--orange)" title="Cache Create: ' + fmtNum(totalCacheCreate) + '"></div>';
    html += '<div style="flex:1;background:var(--blue)" title="Input: ' + fmtNum(totalInput) + '"></div>';
    html += '</div>';
    html += '<div style="display:flex;gap:12px;font-size:10px;color:var(--text-dim)">';
    html += '<span><span style="color:var(--green)">\u25CF</span> ' + fmtNum(totalCacheRead) + ' ' + t('sessions.tok_read') + '</span>';
    html += '<span><span style="color:var(--orange)">\u25CF</span> ' + fmtNum(totalCacheCreate) + ' ' + t('sessions.tok_create') + '</span>';
    html += '<span><span style="color:var(--blue)">\u25CF</span> ' + fmtNum(totalInput) + ' ' + t('sessions.tok_input') + '</span>';
    html += '</div>';
  }
  html += '</details>';

  // Permission breakdown (only if wait spans exist).
  if (waitSpans.length > 0) {
    var bySrc = {};
    for (var pi = 0; pi < waitSpans.length; pi++) {
      var src = waitSpans[pi].source || 'unknown';
      if (!bySrc[src]) bySrc[src] = { count: 0, totalMs: 0 };
      bySrc[src].count++;
      bySrc[src].totalMs += (Number(waitSpans[pi].duration_nano) || 0) / 1e6;
    }
    var srcColors = { 'config': 'var(--green)', 'user_permanent': 'var(--blue)', 'user_temporary': 'var(--orange)' };
    html += '<details class="sess-section" open>';
    html += '<summary>' + t('sessions.permissions') + ' (' + waitSpans.length + ')</summary>';
    html += '<div style="font-size:12px;padding:4px 0">';
    var srcKeys = Object.keys(bySrc).sort();
    for (var si = 0; si < srcKeys.length; si++) {
      var sk = srcKeys[si];
      var sv = bySrc[sk];
      var pct = (sv.count / waitSpans.length * 100).toFixed(0);
      var col = srcColors[sk] || 'var(--text-dim)';
      html += '<div style="display:flex;align-items:center;gap:8px;margin:2px 0">';
      html += '<span style="min-width:100px;color:var(--text-dim)">' + escapeHTML(sk) + '</span>';
      html += '<span style="font-weight:600;min-width:24px;text-align:right">' + sv.count + '</span>';
      html += '<div style="flex:1;height:8px;background:var(--border);border-radius:2px;overflow:hidden"><div style="width:' + pct + '%;height:100%;background:' + col + '"></div></div>';
      html += '<span style="color:var(--text-dim);font-size:10px;min-width:32px;text-align:right">' + pct + '%</span>';
      html += '</div>';
    }
    html += '<div style="color:var(--text-dim);font-size:11px;margin-top:4px">Total wait: ' + fmtDurMs(Math.round(totalWait)) + '</div>';
    html += '</div></details>';
  }

  return html;
}

// ── Feature: Agent Hierarchy ──────────────────────────────────────────

// Color palette for subagent types.
var SESS_AGENT_COLORS = {
  'explore': '#3b82f6',
  'general-purpose': '#8b5cf6',
  'code-review': '#10b981',
  'rubber-duck': '#f59e0b',
  'task': '#6b7280',
  'configure-copilot': '#ec4899',
};
function sess_agentColor(type) {
  if (SESS_AGENT_COLORS[type]) return SESS_AGENT_COLORS[type];
  // Stable hash → HSL for unknown types.
  var s = String(type || 'subagent'), h = 0;
  for (var i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) | 0;
  return 'hsl(' + (Math.abs(h) % 360) + ', 55%, 55%)';
}

// Gantt chart: one row per subagent span + main envelope on top.
function sess_renderSubagentGantt(agentSpans, sessionStart, sessionEnd, agentToolCounts) {
  if (!agentSpans || agentSpans.length === 0) return '';
  var totalMs = Math.max(sessionEnd - sessionStart, 1);
  var mainTools = (agentToolCounts && agentToolCounts['']) || 0;

  // Adaptive gridlines: aim for 6-10 ticks across the timeline.
  var ticks = [];
  var targetTicks = 8;
  var rawStep = totalMs / targetTicks;
  var niceSteps = [1000, 5000, 10000, 30000, 60000, 120000, 300000, 600000, 1800000, 3600000, 7200000, 21600000, 43200000, 86400000];
  var step = niceSteps[niceSteps.length - 1];
  for (var si = 0; si < niceSteps.length; si++) {
    if (niceSteps[si] >= rawStep) { step = niceSteps[si]; break; }
  }
  function fmtGanttTick(ms) {
    if (ms >= 3600000) return (ms / 3600000).toFixed(ms % 3600000 === 0 ? 0 : 1) + 'h';
    if (ms >= 60000) return Math.round(ms / 60000) + 'm';
    if (ms >= 1000) return Math.round(ms / 1000) + 's';
    return ms + 'ms';
  }
  for (var tkMs = step; tkMs < totalMs; tkMs += step) {
    ticks.push({ pct: (tkMs / totalMs) * 100, label: fmtGanttTick(tkMs) });
  }

  var html = '<details class="sess-section" open>';
  html += '<summary>' + t('sessions.subagent_timeline') + ' (' + agentSpans.length + ')';
  html += '<button class="sess-gantt-expand" onclick="event.preventDefault();event.stopPropagation();sess_togglePanel(\'left\')" title="' + t('ui.expand') + '">&#x21C9;</button>';
  html += '</summary>';
  html += '<div class="sess-gantt">';

  // Gridlines (rendered once, as tick lines per row + labels only on axis header).
  var gridLinesOnly = '<div class="sess-gantt-grid">';
  for (var gi = 0; gi < ticks.length; gi++) {
    gridLinesOnly += '<span class="sess-gantt-tick" style="left:' + ticks[gi].pct.toFixed(2) + '%"></span>';
  }
  gridLinesOnly += '</div>';

  // Axis header row with labels.
  html += '<div class="sess-gantt-row sess-gantt-axis">';
  html += '<div class="sess-gantt-label"></div>';
  html += '<div class="sess-gantt-track sess-gantt-axis-track">';
  for (var gj = 0; gj < ticks.length; gj++) {
    html += '<span class="sess-gantt-axis-label" style="left:' + ticks[gj].pct.toFixed(2) + '%">' +
      ticks[gj].label + '</span>';
  }
  html += '</div></div>';

  // Row: main envelope.
  html += '<div class="sess-gantt-row">';
  html += '<div class="sess-gantt-label"><span class="sess-gantt-type">' + t('sessions.agent_main') + '</span>';
  html += '<span class="sess-gantt-sub">' + mainTools + ' ' + t('sessions.tools_suffix') + '</span></div>';
  html += '<div class="sess-gantt-track">' + gridLinesOnly;
  html += '<div class="sess-gantt-bar sess-gantt-main" style="left:0%;width:100%" title="' +
    escapeHTML(t('sessions.agent_main')) + ' — ' + fmtDurMs(totalMs) + '"></div>';
  html += '</div></div>';

  // Rows: one per subagent.
  for (var i = 0; i < agentSpans.length; i++) {
    var sp = agentSpans[i];
    var left = Math.max(0, (sp.start_ts - sessionStart) / totalMs * 100);
    var width = Math.max(0.3, (sp.end_ts - sp.start_ts) / totalMs * 100);
    if (left + width > 100) width = 100 - left;
    var color = sess_agentColor(sp.agent_type);
    var spTools = (agentToolCounts && agentToolCounts[sp.agent_id]) || sp.total_tool_calls || 0;
    var tipParts = [
      sp.agent_type,
      'duration: ' + fmtDurMs(sp.duration_ms || (sp.end_ts - sp.start_ts)),
    ];
    if (sp.total_tokens > 0) tipParts.push('tokens: ' + fmtTokens(sp.total_tokens));
    if (spTools > 0) tipParts.push(spTools + ' tools');
    if (sp.model) tipParts.push(sp.model);
    if (sp.incomplete) tipParts.push('(incomplete)');
    var displayDesc = sp.task_description || sp.task_name || sp.description || '';
    if (sp.task_prompt) tipParts.push('\u2014 ' + sp.task_prompt.slice(0, 200));
    else if (displayDesc) tipParts.push('\u2014 ' + displayDesc);
    var tip = tipParts.join(' · ');

    html += '<div class="sess-gantt-row">';
    html += '<div class="sess-gantt-label">';
    html += '<span class="sess-gantt-dot" style="background:' + color + '"></span>';
    html += '<span class="sess-gantt-type">' + escapeHTML(sp.agent_type || 'subagent') + '</span>';
    if (spTools > 0 || sp.total_tokens > 0) {
      html += '<span class="sess-gantt-sub">';
      if (spTools > 0) html += spTools + 't';
      if (sp.total_tokens > 0) html += (spTools > 0 ? ' · ' : '') + fmtTokens(sp.total_tokens);
      html += '</span>';
    }
    html += '</div>';
    html += '<div class="sess-gantt-track">' + gridLinesOnly;
    html += '<div class="sess-gantt-bar' + (sp.incomplete ? ' sess-gantt-bar-incomplete' : '') +
      '" style="left:' + left.toFixed(2) + '%;width:' + width.toFixed(2) + '%;background:' + color +
      '" title="' + escapeHTML(tip) + '"></div>';
    if (displayDesc) {
      // Render description as caption: inside the bar if wide enough, else trailing it.
      var captionLeft = (width >= 15) ? left : (left + width);
      var captionMaxPct = 100 - captionLeft;
      var inside = width >= 15;
      html += '<div class="sess-gantt-caption' + (inside ? ' sess-gantt-caption-inside' : '') +
        '" style="left:' + captionLeft.toFixed(2) + '%;max-width:' + captionMaxPct.toFixed(2) + '%">' +
        escapeHTML(displayDesc) + '</div>';
    }
    html += '</div></div>';
  }

  html += '</div></details>';
  return html;
}

function sess_renderAgentTree(agents, agentToolCounts) {
  var mainTools = agentToolCounts[''] || 0;
  var html = '<details class="sess-section">';
  html += '<summary>' + t('sessions.agent_hierarchy') + ' (' + (agents.length + 1) + ')</summary>';
  html += '<div class="sess-agent-tree">';
  html += '<div class="sess-agent-node"><span class="sess-agent-icon">\u25B6</span><span class="sess-agent-type">' + t('sessions.agent_main') + '</span>';
  html += '<span class="sess-agent-tools">' + mainTools + ' ' + t('sessions.tools_suffix') + '</span></div>';
  if (agents.length > 0) {
    html += '<div class="sess-agent-children">';
    for (var i = 0; i < agents.length; i++) {
      var a = agents[i];
      var aTools = agentToolCounts[a.agent_id] || 0;
      html += '<div class="sess-agent-node"><span class="sess-agent-icon">\u25B6</span>';
      html += '<span class="sess-agent-type">' + escapeHTML(a.agent_type || t('canvas.subagent')) + '</span>';
      html += '<span class="sess-agent-tools">' + aTools + ' ' + t('sessions.tools_suffix');
      if (a.agent_id) html += ' \u00B7 ' + escapeHTML(a.agent_id.slice(0, 8));
      html += '</span></div>';
    }
    html += '</div>';
  }
  html += '</div></details>';
  return html;
}
