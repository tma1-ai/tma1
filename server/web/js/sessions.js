/* Sessions view — orchestrator: KPI cards, session list, detail loading, search, tab change.
   Sub-modules: sessions-stats.js, sessions-detail.js, sessions-insights.js, sessions-waterfall.js, sessions-timeline.js */
/* globals: query, rows, rowsToObjects, intervalSQL, fmtNum, fmtCost, escapeHTML, escapeJSString, escapeSQLString, tsToMs, t, loadPricing, modelPricing, AgentCanvas,
   sess_computeStats, sess_parseCCOTel, sess_parseCodexOTel, sess_collectConversationIds, sess_filterBySessionId, sess_lookupPrice, fmtDurSec, fmtTokens, fmtDurMs,
   renderSessionDetail, sess_scrollToEvent, sess_highlightAPICall */

var sessFilterTimer = null;
function sess_debouncedFilter() {
  if (sessFilterTimer) clearTimeout(sessFilterTimer);
  sessFilterTimer = setTimeout(function() { sessPage = 0; sess_loadList(); }, 300);
}

var sessPage = 0;
var sessPageSize = 20;
var sessHasNext = false;
var sessExpandedId = null;
var sessTimelineData = [];
var sessCurrentStats = null;
var sessDetailVersion = 0;

// Stable colors for tool names in gantt.
var GANTT_COLORS = ['#79c0ff', '#f0883e', '#57cb8e', '#d2a9ff', '#f85149', '#e5bd57', '#79c0ff', '#ff7b72'];
function ganttColor(toolName) {
  var h = 0;
  for (var i = 0; i < toolName.length; i++) h = ((h << 5) - h + toolName.charCodeAt(i)) | 0;
  return GANTT_COLORS[Math.abs(h) % GANTT_COLORS.length];
}

// ── KPI Cards ──────────────────────────────────────────────────────────

async function sess_loadCards() {
  var iv = intervalSQL();
  // Filter out "ghost" sessions that only fired infrastructure events
  // (e.g., SessionEnd from plugin-install subprocesses). A real session
  // must have at least one UserPromptSubmit or PreToolUse.
  var realSessionWhere =
    "ts > NOW() - INTERVAL '" + iv + "' " +
    "GROUP BY session_id " +
    "HAVING SUM(CASE WHEN event_type IN ('UserPromptSubmit','PreToolUse') THEN 1 ELSE 0 END) > 0";
  var results = await Promise.all([
    query("SELECT COUNT(*) AS v FROM (SELECT session_id FROM tma1_hook_events WHERE " + realSessionWhere + ") t"),
    query("SELECT COUNT(*) AS v FROM tma1_hook_events WHERE event_type = 'PreToolUse' AND ts > NOW() - INTERVAL '" + iv + "'"),
    query("SELECT COUNT(*) AS v FROM tma1_hook_events WHERE event_type = 'SubagentStart' AND ts > NOW() - INTERVAL '" + iv + "'"),
  ]);
  var total = Number((rows(results[0])[0] || [])[0]) || 0;
  var tools = Number((rows(results[1])[0] || [])[0]) || 0;
  var subs = Number((rows(results[2])[0] || [])[0]) || 0;

  document.getElementById('sess-val-total').textContent = fmtNum(total);
  document.getElementById('sess-val-tools').textContent = fmtNum(tools);
  document.getElementById('sess-val-subagents').textContent = fmtNum(subs);
  document.getElementById('sess-val-duration').textContent = '\u2014';

  if (total > 0) {
    try {
      // Two-step: fetch active session IDs (capped) then aggregate with a literal IN list,
      // avoiding GreptimeDB subquery planner memory issues (cf. prompts.js pr_sourceSessionIDs).
      var idsRes = await query(
        "SELECT session_id FROM tma1_hook_events WHERE " + realSessionWhere +
        " ORDER BY MAX(ts) DESC LIMIT 500"
      );
      var idRows = rowsToObjects(idsRes);
      if (idRows.length === 0) return total > 0;
      var idList = idRows.map(function(r) { return "'" + escapeSQLString(r.session_id) + "'"; }).join(',');
      var dRes = await query(
        "SELECT MIN(ts) AS start_ts, MAX(ts) AS end_ts FROM tma1_hook_events" +
        " WHERE session_id IN (" + idList + ") GROUP BY session_id"
      );
      var dRows = rowsToObjects(dRes);
      if (dRows.length > 0) {
        var sumSec = 0, count = 0;
        for (var di = 0; di < dRows.length; di++) {
          var s = tsToMs(dRows[di].start_ts), e = tsToMs(dRows[di].end_ts);
          if (s && e && e > s) { sumSec += (e - s) / 1000; count++; }
        }
        if (count > 0) {
          document.getElementById('sess-val-duration').textContent = fmtDurSec(sumSec / count);
        }
      }
    } catch (e) { /* ignore */ }
  }
  return total > 0;
}

// ── Session List ───────────────────────────────────────────────────────

async function sess_loadList() {
  var iv = intervalSQL();
  var source = document.getElementById('sess-source-filter').value;
  var keyword = (document.getElementById('sess-keyword-filter').value || '').trim();

  // Step 1: fetch active session IDs in the window. Two-step (instead of an IN-subquery)
  // avoids GreptimeDB planner memory issues — same approach as prompts.js pr_sourceSessionIDs.
  var activeWhere = "ts > NOW() - INTERVAL '" + iv + "'";
  if (source) activeWhere += " AND agent_source = '" + escapeSQLString(source) + "'";
  if (keyword) {
    activeWhere += " AND (tool_name LIKE '%" + escapeSQLString(keyword) + "%'" +
      " OR tool_input LIKE '%" + escapeSQLString(keyword) + "%'" +
      " OR tool_result LIKE '%" + escapeSQLString(keyword) + "%')";
  }
  var idsRes = await query(
    "SELECT session_id FROM tma1_hook_events WHERE " + activeWhere +
    " GROUP BY session_id " +
    " HAVING SUM(CASE WHEN event_type IN ('UserPromptSubmit','PreToolUse') THEN 1 ELSE 0 END) > 0" +
    " ORDER BY MAX(ts) DESC LIMIT 500"
  );
  var idRows = rowsToObjects(idsRes);

  var tbody = document.getElementById('sess-table-body');
  if (!idRows.length) {
    tbody.innerHTML = '<tr><td colspan="9" class="loading">' + t('empty.no_data') + '</td></tr>';
    sessHasNext = false;
    renderSessPagination();
    return;
  }
  var idList = idRows.map(function(r) { return "'" + escapeSQLString(r.session_id) + "'"; }).join(',');

  // Step 2: aggregate full-session stats over those IDs (no time predicate so cross-window
  // sessions report their real MIN/MAX). ORDER BY MIN(ts) DESC matches the displayed "Time" column.
  var sql =
    "SELECT session_id, agent_source, MIN(ts) AS start_ts, MAX(ts) AS end_ts, " +
    "SUM(CASE WHEN event_type = 'PreToolUse' THEN 1 ELSE 0 END) AS tool_calls, " +
    "SUM(CASE WHEN event_type = 'SubagentStart' THEN 1 ELSE 0 END) AS subagents, " +
    "MAX(cwd) AS cwd " +
    "FROM tma1_hook_events WHERE session_id IN (" + idList + ") " +
    "GROUP BY session_id, agent_source " +
    "ORDER BY MAX(ts) DESC " +
    "LIMIT " + (sessPageSize + 1) + " OFFSET " + (sessPage * sessPageSize);

  var res = await query(sql);
  var data = rowsToObjects(res);
  sessHasNext = data.length > sessPageSize;
  if (sessHasNext) data = data.slice(0, sessPageSize);

  // Secondary query: cost estimates from messages.
  var costMap = {};
  if (data.length > 0) {
    try {
      await loadPricing();
      var sids = data.map(function(d) { return "'" + escapeSQLString(d.session_id) + "'"; }).join(',');
      var costRes = await query(
        "SELECT session_id, " +
        "SUM(CASE WHEN message_type IN ('user','tool_result','tool_use') THEN LENGTH(COALESCE(content,''))/4 ELSE 0 END) AS input_tok, " +
        "SUM(CASE WHEN message_type IN ('assistant','thinking') THEN LENGTH(COALESCE(content,''))/4 ELSE 0 END) AS output_tok, " +
        "MAX(model) AS model " +
        "FROM tma1_messages WHERE session_id IN (" + sids + ") GROUP BY session_id"
      );
      var costRows = rowsToObjects(costRes);
      for (var ci = 0; ci < costRows.length; ci++) {
        var cr = costRows[ci];
        var price = sess_lookupPrice(cr.model);
        var cost = (Number(cr.input_tok) || 0) * price.input / 1000000 + (Number(cr.output_tok) || 0) * price.output / 1000000;
        costMap[cr.session_id] = cost;
      }
    } catch (e) { /* tma1_messages may not exist */ }
  }

  if (!data.length) {
    tbody.innerHTML = '<tr><td colspan="9" class="loading">' + t('empty.no_data') + '</td></tr>';
    renderSessPagination();
    return;
  }

  var html = '';
  for (var i = 0; i < data.length; i++) {
    var d = data[i];
    var sid = d.session_id || '';
    var startMs = tsToMs(d.start_ts);
    var endMs = tsToMs(d.end_ts);
    var durSec = (endMs && startMs) ? (endMs - startMs) / 1000 : 0;
    var cwd = d.cwd || '';
    var shortCwd = cwd.length > 40 ? '\u2026' + cwd.slice(-39) : cwd;
    var agentSrc = d.agent_source || '';
    var sourceBadge;
    if (agentSrc === 'codex') sourceBadge = '<span class="badge badge-codex">Codex</span>';
    else if (agentSrc === 'openclaw') sourceBadge = '<span class="badge badge-oc">OC</span>';
    else if (agentSrc === 'copilot_cli') sourceBadge = '<span class="badge badge-copilot">GH</span>';
    else sourceBadge = '<span class="badge badge-cc">CC</span>';
    var costStr = costMap[sid] != null ? fmtCost(costMap[sid]) : '\u2014';

    var shortSid = sid.length > 8 ? sid.slice(0, 8) : sid;
    html += '<tr class="sess-row clickable" onclick="sess_openDetail(\x27' + escapeJSString(sid) + '\x27,\x27' + escapeJSString(agentSrc) + '\x27)">';
    html += '<td><code title="' + escapeHTML(sid) + '" style="font-size:11px;color:var(--text-dim)">' + escapeHTML(shortSid) + '</code></td>';
    html += '<td>' + (startMs ? new Date(startMs).toLocaleString() : '\u2014') + '</td>';
    html += '<td>' + (endMs ? new Date(endMs).toLocaleString() : '\u2014') + '</td>';
    html += '<td>' + sourceBadge + '</td>';
    html += '<td>' + fmtDurSec(durSec) + '</td>';
    html += '<td>' + fmtNum(Number(d.tool_calls) || 0) + '</td>';
    html += '<td>' + fmtNum(Number(d.subagents) || 0) + '</td>';
    html += '<td class="cost">' + costStr + '</td>';
    html += '<td title="' + escapeHTML(cwd) + '">' + escapeHTML(shortCwd) + '</td>';
    html += '</tr>';
  }
  tbody.innerHTML = html;
  renderSessPagination();
}

function renderSessPagination() {
  var el = document.getElementById('sess-pagination');
  if (sessPage === 0 && !sessHasNext) { el.innerHTML = ''; return; }
  var html = '';
  if (sessPage > 0) html += '<button class="filter-btn" onclick="sessPage--;sess_loadList()">\u2190 ' + t('btn.prev') + '</button> ';
  html += '<span class="page-info">' + t('table.page') + ' ' + (sessPage + 1) + '</span> ';
  if (sessHasNext) html += '<button class="filter-btn" onclick="sessPage++;sess_loadList()">' + t('btn.next') + ' \u2192</button>';
  el.innerHTML = html;
}

// ── Session Detail Overlay ────────────────────────────────────────────

function sess_escHandler(e) {
  if (e.key === 'Escape') sess_closeDetail();
}

var sessTargetTs = 0;          // timestamp (ms) for timeline scroll
var sessApiCallFP = '';        // fingerprint string for API call highlight (e.g. "3,1035,698172" or nanosecond ts)

function sess_openDetail(sessionId, agentSource, targetTs, apiCallFP, skipHash) {
  sessExpandedId = sessionId;
  sessTargetTs = targetTs || 0;
  sessApiCallFP = apiCallFP || '';
  var overlay = document.getElementById('sess-detail-overlay');
  var content = document.getElementById('sess-detail-content');
  content.innerHTML = '<div class="loading" style="padding:40px;text-align:center">' + t('empty.loading') + '</div>';
  overlay.style.display = 'flex';
  document.removeEventListener('keydown', sess_escHandler);
  document.addEventListener('keydown', sess_escHandler);
  if (!skipHash) updateHash();
  sess_loadDetail(sessionId, agentSource || '');
}

function sess_closeDetail(skipHash) {
  sessDetailVersion++; // invalidate any in-flight async load
  sessExpandedId = null;
  sessTimelineData = [];
  sessCurrentStats = null;
  var overlay = document.getElementById('sess-detail-overlay');
  overlay.style.display = 'none';
  document.getElementById('sess-detail-content').innerHTML = '';
  document.removeEventListener('keydown', sess_escHandler);
  if (!skipHash) updateHash();
}

// ── Load Detail Data ──────────────────────────────────────────────────

async function sess_loadDetail(sessionId, agentSource) {
  var myVersion = ++sessDetailVersion;
  var sid = escapeSQLString(sessionId);

  // Phase 1: hook events + JSONL messages in parallel.
  var phase1 = await Promise.all([
    query(
      "SELECT ts, session_id, event_type, agent_source, tool_name, tool_input, tool_result, " +
      "tool_use_id, agent_id, agent_type, notification_type, \"message\", cwd, permission_mode, metadata, conversation_id " +
      "FROM tma1_hook_events WHERE session_id = '" + sid + "' ORDER BY ts ASC LIMIT 50000"
    ).catch(function() { return null; }),
    query(
      "SELECT ts, session_id, message_type, tool_name, tool_use_id, content, model, " +
      "input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens, duration_ms " +
      "FROM tma1_messages WHERE session_id = '" + sid + "' ORDER BY ts ASC LIMIT 50000"
    ).catch(function() { return null; }),
  ]);
  if (sessDetailVersion !== myVersion) return;

  var hookEvents = phase1[0] ? rowsToObjects(phase1[0]) : [];
  var messages = phase1[1] ? rowsToObjects(phase1[1]) : [];

  // Infer agent source from hook data when not provided (e.g. search
  // results pass empty agent_source). Iterate until we find a row with
  // a non-empty source — early rows may be agent-less SessionStart
  // pings.
  if (!agentSource) {
    for (var srcIdx = 0; srcIdx < hookEvents.length; srcIdx++) {
      if (hookEvents[srcIdx].agent_source) {
        agentSource = hookEvents[srcIdx].agent_source;
        break;
      }
    }
  }

  // Merge tool pairs from hooks (PostToolUse with matching PreToolUse).
  var pending = {};
  var toolPairs = [];
  for (var h = 0; h < hookEvents.length; h++) {
    var ev = hookEvents[h];
    ev.ts_ms = tsToMs(ev.ts);
    if (ev.event_type === 'PreToolUse' && ev.tool_use_id) {
      pending[ev.tool_use_id] = ev;
    } else if ((ev.event_type === 'PostToolUse' || ev.event_type === 'PostToolUseFailure') && ev.tool_use_id && pending[ev.tool_use_id]) {
      var pre = pending[ev.tool_use_id];
      toolPairs.push({
        tool_name: pre.tool_name || ev.tool_name, tool_input: pre.tool_input || '',
        tool_result: ev.tool_result || '', tool_use_id: pre.tool_use_id,
        start_ts: pre.ts_ms, end_ts: ev.ts_ms, agent_id: pre.agent_id || '',
        failed: ev.event_type === 'PostToolUseFailure',
      });
      delete pending[ev.tool_use_id];
    }
  }

  // Track paired tool_use_ids to suppress duplicates from messages.
  var pairedIds = {};
  for (var pi = 0; pi < toolPairs.length; pi++) pairedIds[toolPairs[pi].tool_use_id] = true;

  // Build unified timeline (sorted by ts).
  var timeline = [];
  for (var tp = 0; tp < toolPairs.length; tp++) {
    timeline.push({ ts: toolPairs[tp].start_ts, source: 'tool_pair', data: toolPairs[tp] });
  }
  // Add unmatched PreToolUse (active/pending tools) back to the timeline.
  for (var pk in pending) {
    timeline.push({ ts: pending[pk].ts_ms, source: 'hook', data: pending[pk] });
  }
  // Hook events that aren't tool-lifecycle go to timeline directly.
  var toolLifecycle = { PreToolUse: 1, PostToolUse: 1, PostToolUseFailure: 1 };
  for (var he = 0; he < hookEvents.length; he++) {
    if (!toolLifecycle[hookEvents[he].event_type]) {
      timeline.push({ ts: hookEvents[he].ts_ms, source: 'hook', data: hookEvents[he] });
    }
  }
  // Messages: skip tool_use/tool_result if already covered by a hook-based tool pair.
  // Skip the synthetic 'usage' rows the Codex parser writes
  // (insertCodexModelMessage to carry model name into the KPI lookup,
  // insertCodexUsageMessage to carry per-call token usage into the
  // apiCalls fallback). They have no displayable content; the
  // dedicated message_type lets us drop them by type instead of an
  // empty-content heuristic that could mis-fire on real data.
  // Legacy fallback: pre-rename data wrote synthetic rows as
  // message_type='assistant' with empty content — also drop those
  // so old sessions stay clean.
  for (var mi = 0; mi < messages.length; mi++) {
    var msg = messages[mi];
    if ((msg.message_type === 'tool_use' || msg.message_type === 'tool_result') && msg.tool_use_id && pairedIds[msg.tool_use_id]) continue;
    if (msg.message_type === 'usage') continue;
    if (msg.message_type === 'assistant' && !(msg.content && msg.content.length) && !msg.tool_use_id) continue;
    timeline.push({ ts: tsToMs(msg.ts), source: 'message', data: msg });
  }
  timeline.sort(function(a, b) { return a.ts - b.ts; });

  sessTimelineData = timeline;

  // Phase 2: OTel API call enrichment (parallel).
  await loadPricing();
  var apiCalls = [];
  var apiErrors = [];

  if (agentSource === 'codex') {
    // Codex: prefer OTel logs (richer + cost_usd via app billing)
    // and fall back to assistant rows written by the JSONL parser
    // from event_msg.token_count when OTel data isn't present.
    var conversationIds = sess_collectConversationIds(hookEvents);
    if (conversationIds.length > 0 && timeline.length > 0) {
      var tsBetween = "timestamp BETWEEN '" + new Date(timeline[0].ts - 60000).toISOString() + "' AND '" + new Date(timeline[timeline.length - 1].ts + 60000).toISOString() + "'";
      var otelRes = await query(
        "SELECT timestamp, log_attributes FROM opentelemetry_logs " +
        "WHERE scope_name LIKE 'codex_%' AND json_get_int(log_attributes, 'input_token_count') IS NOT NULL AND " + tsBetween +
        " ORDER BY timestamp ASC LIMIT 500"
      ).catch(function() { return null; });
      if (otelRes && sessDetailVersion === myVersion) {
        var otelRows = rowsToObjects(otelRes);
        apiCalls = sess_parseCodexOTel(otelRows, conversationIds);
      }
    }
    if (apiCalls.length === 0) {
      // JSONL fallback. insertCodexUsageMessage writes one 'usage' row
      // per Codex token_count event with input/output/cache/reasoning
      // populated; insertCodexModelMessage writes a model-only 'usage'
      // row with every token column NULL. The > 0 guard below skips
      // the latter so model-only rows never become spurious calls.
      for (var ci = 0; ci < messages.length; ci++) {
        var cm = messages[ci];
        if (cm.message_type !== 'usage') continue;
        var cIn = Number(cm.input_tokens) || 0;
        var cOut = Number(cm.output_tokens) || 0;
        var cReasoning = Number(cm.reasoning_tokens) || 0;
        if (cIn === 0 && cOut === 0 && cReasoning === 0) continue;
        var cCache = Number(cm.cache_read_tokens) || 0;
        var cPrice = sess_lookupPrice(cm.model);
        apiCalls.push({
          ts: tsToMs(cm.ts), model: cm.model || '',
          inputTokens: cIn, outputTokens: cOut,
          reasoningTokens: cReasoning,
          cacheTokens: cCache, cacheCreationTokens: 0,
          // OpenAI o-series bills reasoning at the output rate, so
          // mirror the cost shape sess_parseCodexOTel uses.
          cost: cIn * cPrice.input / 1000000 + (cOut + cReasoning) * cPrice.output / 1000000,
          durationMs: Number(cm.duration_ms) || 0, toolUseIds: [],
        });
      }
    }
  } else if (agentSource === 'openclaw') {
    // OpenClaw: usage + duration data is already in tma1_messages (parsed from JSONL transcript).
    // OpenClaw fronts Anthropic models, which fold thinking tokens
    // into output_tokens — reasoning is not surfaced here.
    for (var oi = 0; oi < messages.length; oi++) {
      var om = messages[oi];
      if (om.message_type !== 'assistant') continue;
      var oIn = Number(om.input_tokens) || 0;
      var oOut = Number(om.output_tokens) || 0;
      if (oIn === 0 && oOut === 0) continue;
      var oCacheR = Number(om.cache_read_tokens) || 0;
      var oCacheW = Number(om.cache_creation_tokens) || 0;
      var oPrice = sess_lookupPrice(om.model);
      apiCalls.push({
        ts: tsToMs(om.ts), model: om.model || '',
        inputTokens: oIn, outputTokens: oOut,
        cacheTokens: oCacheR, cacheCreationTokens: oCacheW,
        cost: oIn * oPrice.input / 1000000 + oOut * oPrice.output / 1000000,
        durationMs: Number(om.duration_ms) || 0, toolUseIds: [],
      });
    }
  } else {
    // CC: prefer message-level usage data, fallback to OTel logs.
    var hasUsageInMessages = messages.some(function(m) {
      return m.message_type === 'assistant' && (Number(m.input_tokens) > 0 || Number(m.output_tokens) > 0);
    });

    if (hasUsageInMessages) {
      // CC fronts Anthropic models; thinking is already inside
      // output_tokens, so reasoning is not surfaced as a separate
      // column on the dashboard.
      for (var ui = 0; ui < messages.length; ui++) {
        var um = messages[ui];
        if (um.message_type !== 'assistant') continue;
        var inTok = Number(um.input_tokens) || 0;
        var outTok = Number(um.output_tokens) || 0;
        if (inTok === 0 && outTok === 0) continue;
        var cacheRead = Number(um.cache_read_tokens) || 0;
        var cacheCreate = Number(um.cache_creation_tokens) || 0;
        var mPrice = sess_lookupPrice(um.model);
        apiCalls.push({
          ts: tsToMs(um.ts), model: um.model || '',
          inputTokens: inTok, outputTokens: outTok,
          cacheTokens: cacheRead, cacheCreationTokens: cacheCreate,
          cost: inTok * mPrice.input / 1000000 + outTok * mPrice.output / 1000000,
          durationMs: 0, toolUseIds: [],
        });
      }
    } else if (timeline.length > 0) {
      var trBetween = "timestamp BETWEEN '" + new Date(timeline[0].ts - 60000).toISOString() + "' AND '" + new Date(timeline[timeline.length - 1].ts + 60000).toISOString() + "'";
      var ccOtelRes = await query(
        "SELECT timestamp, log_attributes FROM opentelemetry_logs " +
        "WHERE body = 'claude_code.api_request' AND " + trBetween +
        " ORDER BY timestamp ASC LIMIT 500"
      ).catch(function() { return null; });
      if (ccOtelRes && sessDetailVersion === myVersion) {
        var ccOtelRows = rowsToObjects(ccOtelRes);
        var filtered = sess_filterBySessionId(ccOtelRows, sessionId);
        apiCalls = sess_parseCCOTel(filtered, sessionId);
      }
    }

    // CC errors.
    if (timeline.length > 0) {
      var errBetween = "timestamp BETWEEN '" + new Date(timeline[0].ts - 60000).toISOString() + "' AND '" + new Date(timeline[timeline.length - 1].ts + 60000).toISOString() + "'";
      var errRes = await query(
        "SELECT timestamp, log_attributes FROM opentelemetry_logs " +
        "WHERE body = 'claude_code.api_error' AND " + errBetween +
        " ORDER BY timestamp ASC LIMIT 100"
      ).catch(function() { return null; });
      if (errRes && sessDetailVersion === myVersion) {
        var errRows = rowsToObjects(errRes);
        apiErrors = sess_filterBySessionId(errRows, sessionId);
      }
    }
  }
  if (sessDetailVersion !== myVersion) return;

  // Phase 3: CC trace spans (enhanced telemetry, CC-only).
  var ccTraceSpans = null;
  if (agentSource === 'claude_code' && timeline.length > 0) {
    var trBetween2 = "timestamp BETWEEN '" + new Date(timeline[0].ts - 60000).toISOString() + "' AND '" + new Date(timeline[timeline.length - 1].ts + 60000).toISOString() + "'";
    var trRes = await query(
      "SELECT trace_id, span_id, parent_span_id, span_name, timestamp, duration_nano, " +
      "\"span_attributes.tool_name\" AS tool_name, " +
      "\"span_attributes.input_tokens\" AS input_tokens, " +
      "\"span_attributes.output_tokens\" AS output_tokens, " +
      "\"span_attributes.cache_read_tokens\" AS cache_read_tokens, " +
      "\"span_attributes.cache_creation_tokens\" AS cache_creation_tokens, " +
      "\"span_attributes.ttft_ms\" AS ttft_ms, " +
      "\"span_attributes.speed\" AS speed, " +
      "\"span_attributes.decision\" AS decision, " +
      "\"span_attributes.source\" AS source, " +
      "\"span_attributes.success\" AS success " +
      "FROM opentelemetry_traces WHERE span_name LIKE 'claude_code.%' " +
      "AND \"span_attributes.session.id\" = '" + sid + "' AND " + trBetween2 +
      " ORDER BY timestamp ASC LIMIT 2000"
    ).catch(function() { return null; });
    if (trRes && sessDetailVersion === myVersion) {
      ccTraceSpans = rowsToObjects(trRes);
      if (ccTraceSpans.length === 0) ccTraceSpans = null;
    }
  }
  if (sessDetailVersion !== myVersion) return;

  // Compute stats.
  sessCurrentStats = sess_computeStats(hookEvents, messages, timeline, apiCalls, apiErrors);
  sessCurrentStats.ccTraceSpans = ccTraceSpans;

  renderSessionDetail(timeline, sessCurrentStats);
}

// ── Search ─────────────────────────────────────────────────────────────

async function sess_search() {
  var q = document.getElementById('sess-search-input').value.trim();
  var el = document.getElementById('sess-search-results');
  if (!q) { el.innerHTML = ''; return; }
  var iv = intervalSQL();
  var results = await Promise.all([
    query("SELECT session_id, ts, 'hook' AS src, event_type AS msg_type, tool_name, agent_source, COALESCE(tool_input, '') AS content FROM tma1_hook_events WHERE (tool_name LIKE '%" + escapeSQLString(q) + "%' OR tool_input LIKE '%" + escapeSQLString(q) + "%' OR tool_result LIKE '%" + escapeSQLString(q) + "%') AND ts > NOW() - INTERVAL '" + iv + "' ORDER BY ts DESC LIMIT 25").catch(function() { return null; }),
    query("SELECT session_id, ts, 'msg' AS src, message_type AS msg_type, '' AS tool_name, '' AS agent_source, COALESCE(content, '') AS content FROM tma1_messages WHERE matches_term(content, '" + escapeSQLString(q) + "') AND ts > NOW() - INTERVAL '" + iv + "' ORDER BY ts DESC LIMIT 25").catch(function() { return null; }),
  ]);
  var data = [];
  if (results[0]) data = data.concat(rowsToObjects(results[0]));
  if (results[1]) data = data.concat(rowsToObjects(results[1]));
  data.sort(function(a, b) { return tsToMs(b.ts) - tsToMs(a.ts); });
  if (data.length > 50) data = data.slice(0, 50);
  if (!data.length) { el.innerHTML = '<div class="loading">' + t('empty.no_data') + '</div>'; return; }
  var html = '';
  for (var i = 0; i < data.length; i++) {
    var d = data[i];
    var ms = tsToMs(d.ts);
    var content = d.content || '';
    if (content.length > 200) content = content.slice(0, 200) + '\u2026';
    var label = d.tool_name || d.msg_type || '';
    var sSrc = d.agent_source || '';
    var sBadgeCls = sSrc === 'codex' ? 'badge-codex' : sSrc === 'openclaw' ? 'badge-oc' : sSrc === 'copilot_cli' ? 'badge-copilot' : 'badge-cc';
    html += '<div class="search-result-item clickable" onclick="sess_openDetail(\x27' + escapeJSString(d.session_id) + '\x27,\x27' + escapeJSString(sSrc) + '\x27,' + (ms || 0) + ')">';
    html += '<div class="search-result-meta"><span class="badge ' + sBadgeCls + '">' + escapeHTML((d.session_id || '').slice(0, 8)) + '</span> ';
    if (label) html += '<span class="tl-tool-name" style="font-size:12px">' + escapeHTML(label) + '</span> ';
    html += '<span class="tl-time">' + (ms ? new Date(ms).toLocaleString() : '') + '</span></div>';
    html += '<div class="search-result-content">' + escapeHTML(content) + '</div></div>';
  }
  el.innerHTML = html;
}

// ── Tab change handler ─────────────────────────────────────────────────

function sess_onTabChange(tab) {
  if (tab === 'sess-list') sess_loadList();
}
