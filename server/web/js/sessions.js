/* Sessions view — session list, conversation timeline, search, full-screen detail overlay. */
/* globals: query, rows, rowsToObjects, intervalSQL, fmtNum, fmtCost, escapeHTML, escapeJSString, escapeSQLString, tsToMs, t, loadPricing, modelPricing, AgentCanvas */

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
  var results = await Promise.all([
    query("SELECT COUNT(DISTINCT session_id) AS v FROM tma1_hook_events WHERE ts > NOW() - INTERVAL '" + iv + "'"),
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
      var dRes = await query(
        "SELECT MIN(ts) AS start_ts, MAX(ts) AS end_ts FROM tma1_hook_events" +
        " WHERE ts > NOW() - INTERVAL '" + iv + "' GROUP BY session_id"
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

function fmtDurSec(sec) {
  if (sec < 60) return Math.round(sec) + 's';
  if (sec < 3600) return Math.round(sec / 60) + 'm';
  return (sec / 3600).toFixed(1) + 'h';
}

function fmtTokens(n) {
  if (n < 1000) return n + '';
  if (n < 1000000) return (n / 1000).toFixed(1) + 'k';
  return (n / 1000000).toFixed(1) + 'M';
}

// ── Session List ───────────────────────────────────────────────────────

async function sess_loadList() {
  var iv = intervalSQL();
  var source = document.getElementById('sess-source-filter').value;
  var keyword = (document.getElementById('sess-keyword-filter').value || '').trim();
  var where = "ts > NOW() - INTERVAL '" + iv + "'";
  if (source) where += " AND agent_source = '" + escapeSQLString(source) + "'";

  var sessionFilter = '';
  if (keyword) {
    sessionFilter = " AND session_id IN (" +
      "SELECT DISTINCT session_id FROM tma1_hook_events WHERE " + where +
      " AND (tool_name LIKE '%" + escapeSQLString(keyword) + "%'" +
      " OR tool_input LIKE '%" + escapeSQLString(keyword) + "%'" +
      " OR tool_result LIKE '%" + escapeSQLString(keyword) + "%'))";
  }

  var sql =
    "SELECT session_id, agent_source, MIN(ts) AS start_ts, MAX(ts) AS end_ts, " +
    "SUM(CASE WHEN event_type = 'PreToolUse' THEN 1 ELSE 0 END) AS tool_calls, " +
    "SUM(CASE WHEN event_type = 'SubagentStart' THEN 1 ELSE 0 END) AS subagents, " +
    "MAX(cwd) AS cwd " +
    "FROM tma1_hook_events WHERE " + where + sessionFilter + " " +
    "GROUP BY session_id, agent_source " +
    "ORDER BY MIN(ts) DESC " +
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

  var tbody = document.getElementById('sess-table-body');
  if (!data.length) {
    tbody.innerHTML = '<tr><td colspan="8" class="loading">' + t('empty.no_data') + '</td></tr>';
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
    var sourceBadge = (agentSrc === 'codex')
      ? '<span class="badge badge-codex">Codex</span>'
      : '<span class="badge badge-cc">CC</span>';
    var costStr = costMap[sid] != null ? fmtCost(costMap[sid]) : '\u2014';

    var shortSid = sid.length > 8 ? sid.slice(0, 8) : sid;
    html += '<tr class="sess-row clickable" onclick="sess_openDetail(\x27' + escapeJSString(sid) + '\x27,\x27' + escapeJSString(agentSrc) + '\x27)">';
    html += '<td><code title="' + escapeHTML(sid) + '" style="font-size:11px;color:var(--text-dim)">' + escapeHTML(shortSid) + '</code></td>';
    html += '<td>' + (startMs ? new Date(startMs).toLocaleString() : '\u2014') + '</td>';
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

function sess_lookupPrice(model) {
  if (!model || !modelPricing || !modelPricing.length) return { input: 3, output: 15 };
  for (var i = 0; i < modelPricing.length; i++) {
    if (model.indexOf(modelPricing[i].p) !== -1) {
      return { input: modelPricing[i].i, output: modelPricing[i].o };
    }
  }
  return { input: 3, output: 15 };
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

function sess_openDetail(sessionId, agentSource, targetTs, apiCallFP) {
  sessExpandedId = sessionId;
  sessTargetTs = targetTs || 0;
  sessApiCallFP = apiCallFP || '';
  var overlay = document.getElementById('sess-detail-overlay');
  var content = document.getElementById('sess-detail-content');
  content.innerHTML = '<div class="loading" style="padding:40px;text-align:center">' + t('empty.loading') + '</div>';
  overlay.style.display = 'flex';
  document.removeEventListener('keydown', sess_escHandler);
  document.addEventListener('keydown', sess_escHandler);
  sess_loadDetail(sessionId, agentSource || '');
}

function sess_closeDetail() {
  sessDetailVersion++; // invalidate any in-flight async load
  sessExpandedId = null;
  sessTimelineData = [];
  sessCurrentStats = null;
  var overlay = document.getElementById('sess-detail-overlay');
  overlay.style.display = 'none';
  document.getElementById('sess-detail-content').innerHTML = '';
  document.removeEventListener('keydown', sess_escHandler);
}

function sess_togglePanel(side) {
  var body = document.querySelector('.sess-overlay-body');
  if (!body) return;
  var cls = 'expand-' + side;
  if (body.classList.contains(cls)) {
    body.classList.remove(cls);
  } else {
    body.classList.remove('expand-left', 'expand-right');
    body.classList.add(cls);
  }
}

function sess_toggleErrors() {
  var panel = document.getElementById('sess-error-panel');
  if (panel) panel.style.display = panel.style.display === 'none' ? 'block' : 'none';
}

// ── Load Detail Data ──────────────────────────────────────────────────

async function sess_loadDetail(sessionId, agentSource) {
  var myVersion = ++sessDetailVersion;
  var sid = escapeSQLString(sessionId);

  // Phase 1: hook events + messages (always available).
  var results = await Promise.all([
    query(
      "SELECT ts, event_type, tool_name, tool_input, tool_result, " +
      "tool_use_id, agent_id, agent_type, notification_type, \"message\", " +
      "conversation_id, " +
      "agent_source " +
      "FROM tma1_hook_events WHERE session_id = '" + sid + "' ORDER BY ts ASC"
    ),
    query(
      "SELECT ts, message_type, \"role\", content, model, tool_name, tool_use_id " +
      "FROM tma1_messages WHERE session_id = '" + sid + "' ORDER BY ts ASC"
    ).catch(function() { return null; }),
  ]);

  if (myVersion !== sessDetailVersion) return; // stale request

  var hookEvents = rowsToObjects(results[0]);
  var messages = results[1] ? rowsToObjects(results[1]) : [];

  // Determine agent source if not provided.
  if (!agentSource && hookEvents.length > 0) {
    agentSource = hookEvents[0].agent_source || '';
  }

  // Pair PreToolUse + PostToolUse.
  var pendingTools = {};
  var timeline = [];

  for (var i = 0; i < hookEvents.length; i++) {
    var ev = hookEvents[i];
    if (ev.event_type === 'PreToolUse' && ev.tool_use_id) {
      pendingTools[ev.tool_use_id] = ev;
      continue;
    }
    if ((ev.event_type === 'PostToolUse' || ev.event_type === 'PostToolUseFailure') && ev.tool_use_id && pendingTools[ev.tool_use_id]) {
      var pre = pendingTools[ev.tool_use_id];
      delete pendingTools[ev.tool_use_id];
      timeline.push({
        source: 'tool_pair', ts: tsToMs(pre.ts),
        data: {
          tool_name: pre.tool_name || ev.tool_name, tool_input: pre.tool_input,
          tool_result: ev.tool_result, tool_use_id: ev.tool_use_id,
          agent_id: pre.agent_id || ev.agent_id || '',
          agent_type: pre.agent_type || ev.agent_type || '',
          start_ts: tsToMs(pre.ts), end_ts: tsToMs(ev.ts),
          failed: ev.event_type === 'PostToolUseFailure',
        },
      });
      continue;
    }
    timeline.push({ source: 'hook', ts: tsToMs(ev.ts), data: ev });
  }
  for (var tuid in pendingTools) {
    timeline.push({ source: 'hook', ts: tsToMs(pendingTools[tuid].ts), data: pendingTools[tuid] });
  }

  var pairedIds = {};
  for (var ti = 0; ti < timeline.length; ti++) {
    if (timeline[ti].source === 'tool_pair') pairedIds[timeline[ti].data.tool_use_id] = true;
  }
  for (var j = 0; j < messages.length; j++) {
    var msg = messages[j];
    if ((msg.message_type === 'tool_use' || msg.message_type === 'tool_result') && msg.tool_use_id && pairedIds[msg.tool_use_id]) continue;
    timeline.push({ source: 'message', ts: tsToMs(msg.ts), data: msg });
  }
  timeline.sort(function(a, b) { return a.ts - b.ts; });

  if (myVersion !== sessDetailVersion) return; // stale request
  sessTimelineData = timeline;
  await loadPricing();

  // Phase 2: API call enrichment data.
  var apiCalls = [];
  var apiErrors = [];
  if (timeline.length > 0) {
    try {
      if (agentSource === 'codex') {
        var conversationIds = sess_collectConversationIds(hookEvents);
        // Codex: API calls from OTel logs (JSONL has no usage data).
        var startISO = new Date(timeline[0].ts).toISOString();
        var endISO = new Date(timeline[timeline.length - 1].ts + 60000).toISOString();
        var tsBetween = "timestamp BETWEEN '" + startISO + "'::TIMESTAMP AND '" + endISO + "'::TIMESTAMP";
        var cdxRes = await query(
          "SELECT timestamp, log_attributes FROM opentelemetry_logs " +
          "WHERE scope_name LIKE 'codex_%' " +
          "AND json_get_int(log_attributes, 'input_token_count') IS NOT NULL " +
          "AND " + tsBetween + " ORDER BY timestamp ASC"
        ).catch(function() { return null; });
        if (cdxRes) apiCalls = sess_parseCodexOTel(rowsToObjects(cdxRes), conversationIds);
      } else {
        // CC: API calls from OTel (reliable, no context compression duplicates).
        // JSONL messages have usage but context compression replays inflate the count.
        if (timeline.length > 0) {
          var ccStart = new Date(timeline[0].ts).toISOString();
          var ccEnd = new Date(timeline[timeline.length - 1].ts + 60000).toISOString();
          var ccBetween = "timestamp BETWEEN '" + ccStart + "'::TIMESTAMP AND '" + ccEnd + "'::TIMESTAMP";
          var ccResults = await Promise.all([
            query(
              "SELECT timestamp, log_attributes FROM opentelemetry_logs " +
              "WHERE body = 'claude_code.api_request' " +
              "AND " + ccBetween + " ORDER BY timestamp ASC"
            ).catch(function() { return null; }),
            query(
              "SELECT timestamp, log_attributes FROM opentelemetry_logs " +
              "WHERE body = 'claude_code.api_error' " +
              "AND " + ccBetween + " ORDER BY timestamp ASC"
            ).catch(function() { return null; }),
          ]);
          if (ccResults[0]) apiCalls = sess_parseCCOTel(rowsToObjects(ccResults[0]), sessionId);
          if (ccResults[1]) apiErrors = sess_filterBySessionId(rowsToObjects(ccResults[1]), sessionId);
        }
      }
    } catch (e) { /* enrichment data not available */ }
  }

  if (myVersion !== sessDetailVersion) return; // stale request
  sessCurrentStats = sess_computeStats(hookEvents, messages, timeline, apiCalls, apiErrors);
  renderSessionDetail(timeline, sessCurrentStats);
}

// Fallback OTel parser for CC sessions without usage data in messages (old data).
function sess_parseCCOTel(rows, sessionId) {
  var calls = [];
  for (var i = 0; i < rows.length; i++) {
    var a = sess_parseAttrs(rows[i].log_attributes);
    if (!a) continue;
    var rowSessionId = sess_attr(a, 'session.id');
    if (sessionId && rowSessionId && rowSessionId !== sessionId) continue;
    var seq = a['event.sequence'];
    calls.push({
      ts: tsToMs(rows[i].timestamp),
      model: a.model || '',
      inputTokens: Number(a.input_tokens) || 0,
      outputTokens: Number(a.output_tokens) || 0,
      cacheTokens: Number(a.cache_read_tokens) || 0,
      cacheCreationTokens: Number(a.cache_creation_tokens) || 0,
      cost: parseFloat(a.cost_usd) || 0,
      durationMs: parseFloat(a.duration_ms) || 0,
      toolUseIds: [],
      eventSeq: seq != null ? seq : null,
    });
  }
  return calls;
}

function sess_parseCodexOTel(rows, conversationIds) {
  var allowedConversations = null;
  if (conversationIds && conversationIds.length) {
    allowedConversations = {};
    for (var ci = 0; ci < conversationIds.length; ci++) {
      allowedConversations[conversationIds[ci]] = true;
    }
  }

  var calls = [];
  for (var i = 0; i < rows.length; i++) {
    var a = sess_parseAttrs(rows[i].log_attributes);
    if (!a) continue;
    if (allowedConversations) {
      var conversationId = sess_attr(a, 'conversation.id');
      if (!conversationId || !allowedConversations[conversationId]) continue;
    }
    var inputTok = Number(a.input_token_count) || 0;
    var outputTok = Number(a.output_token_count) || 0;
    var model = a.model || '';
    var price = sess_lookupPrice(model);
    calls.push({
      ts: tsToMs(rows[i].timestamp),
      model: model,
      inputTokens: inputTok,
      outputTokens: outputTok,
      cacheTokens: Number(a.cached_token_count) || 0,
      cacheCreationTokens: 0,
      cost: inputTok * price.input / 1000000 + outputTok * price.output / 1000000,
      durationMs: parseFloat(a.duration_ms) || 0,
    });
  }
  return calls;
}

function sess_collectConversationIds(hookEvents) {
  var ids = [];
  var seen = {};
  for (var i = 0; i < hookEvents.length; i++) {
    var id = hookEvents[i].conversation_id;
    if (!id || seen[id]) continue;
    seen[id] = true;
    ids.push(id);
  }
  return ids;
}

function sess_attr(attrs, key) {
  if (!attrs) return null;
  if (Object.prototype.hasOwnProperty.call(attrs, key)) return attrs[key];
  var parts = key.split('.');
  var curr = attrs;
  for (var i = 0; i < parts.length; i++) {
    if (curr == null || typeof curr !== 'object' || !Object.prototype.hasOwnProperty.call(curr, parts[i])) {
      return null;
    }
    curr = curr[parts[i]];
  }
  return curr;
}

function sess_filterBySessionId(rows, sessionId) {
  if (!sessionId) return rows;
  return rows.filter(function(r) {
    var a = sess_parseAttrs(r.log_attributes);
    var rowSessionId = sess_attr(a, 'session.id');
    return !rowSessionId || rowSessionId === sessionId;
  });
}

// Parse log_attributes once per row (avoids redundant JSON.parse per field).
function sess_parseAttrs(la) {
  if (!la) return null;
  try { return typeof la === 'string' ? JSON.parse(la) : la; } catch (e) { return null; }
}

// ── Compute Stats ─────────────────────────────────────────────────────

function sess_computeStats(hookEvents, messages, timeline, apiCalls, apiErrors) {
  var stats = {
    duration: 0, toolCount: 0, primaryModel: '', cost: 0,
    files: {},
    context: { system: 5000, user: 0, tools: 0, reasoning: 0, subagent: 0 },
    agents: [],
    gantt: [],
    // OTel enrichment.
    apiCalls: apiCalls || [],
    totalInputTokens: 0,
    totalOutputTokens: 0,
    totalCacheTokens: 0,
    cacheHitRatio: 0,
    apiErrors: apiErrors || [],
    errorCount: (apiErrors || []).length,
    hasOTel: false,
    costSource: 'estimate',
  };

  // Duration from timeline bounds.
  if (timeline.length > 0) {
    stats.duration = (timeline[timeline.length - 1].ts - timeline[0].ts) / 1000;
  }

  // Tool pairs → file attention + gantt + tool count.
  for (var i = 0; i < timeline.length; i++) {
    var item = timeline[i];
    if (item.source === 'tool_pair') {
      stats.toolCount++;
      var tc = item.data;
      stats.gantt.push(tc);
      var fps = extractAllFilePaths(tc.tool_name, tc.tool_input);
      for (var fi = 0; fi < fps.length; fi++) {
        var fp = fps[fi];
        if (!stats.files[fp]) stats.files[fp] = { reads: 0, writes: 0 };
        if (tc.tool_name === 'Write' || tc.tool_name === 'Edit' || tc.tool_name === 'apply_patch') stats.files[fp].writes++;
        else stats.files[fp].reads++;
      }
      var resultLen = (tc.tool_result || '').length;
      var mult = (tc.tool_name === 'Read' ? 1.0 : tc.tool_name === 'Grep' || tc.tool_name === 'Glob' ? 0.5 : 0.3);
      if (tc.tool_name === 'Agent' || tc.tool_name === 'Task') {
        stats.context.subagent += Math.round(resultLen / 4 * mult);
      } else {
        stats.context.tools += Math.round(resultLen / 4 * mult);
      }
    }
  }

  // Agent hierarchy.
  var agentToolCounts = {};
  for (var h = 0; h < hookEvents.length; h++) {
    var hev = hookEvents[h];
    if (hev.event_type === 'SubagentStart') stats.agents.push(hev);
    if (hev.event_type === 'PreToolUse') {
      var aid = hev.agent_id || '';
      agentToolCounts[aid] = (agentToolCounts[aid] || 0) + 1;
    }
  }
  stats.agentToolCounts = agentToolCounts;

  // Messages → context breakdown + model + cost estimate.
  var estInputTokens = 0;
  var estOutputTokens = 0;
  for (var m = 0; m < messages.length; m++) {
    var msg = messages[m];
    var contentLen = (msg.content || '').length;
    var tokens = Math.round(contentLen / 4);
    if (!stats.primaryModel && msg.model) stats.primaryModel = msg.model;

    if (msg.message_type === 'user') {
      stats.context.user += tokens;
      estInputTokens += tokens;
    } else if (msg.message_type === 'tool_result') {
      estInputTokens += Math.round(tokens * 0.3);
    } else if (msg.message_type === 'tool_use') {
      estInputTokens += tokens;
    } else if (msg.message_type === 'assistant' || msg.message_type === 'thinking') {
      stats.context.reasoning += tokens;
      estOutputTokens += tokens;
    }
  }

  // OTel enrichment: prefer precise data over estimates.
  if (stats.apiCalls.length > 0) {
    stats.hasOTel = true;
    var totalIn = 0, totalOut = 0, totalCache = 0, totalCost = 0;
    for (var ac = 0; ac < stats.apiCalls.length; ac++) {
      var call = stats.apiCalls[ac];
      totalIn += call.inputTokens;
      totalOut += call.outputTokens;
      totalCache += call.cacheTokens;
      totalCost += call.cost;
      if (!stats.primaryModel && call.model) stats.primaryModel = call.model;
    }
    stats.totalInputTokens = totalIn;
    stats.totalOutputTokens = totalOut;
    stats.totalCacheTokens = totalCache;
    stats.cost = totalCost;
    stats.costSource = 'otel';
    if (totalIn + totalCache > 0) {
      stats.cacheHitRatio = totalCache / (totalIn + totalCache);
    }
  } else {
    // Fallback to estimates.
    stats.totalInputTokens = estInputTokens;
    stats.totalOutputTokens = estOutputTokens;
    var price = sess_lookupPrice(stats.primaryModel);
    stats.cost = estInputTokens * price.input / 1000000 + estOutputTokens * price.output / 1000000;
  }

  return stats;
}

// ── File path extraction ──────────────────────────────────────────────

function extractFilePath(toolName, inputStr) {
  if (!inputStr) return null;
  try {
    var obj = JSON.parse(inputStr);
    if (toolName === 'Read' || toolName === 'Write' || toolName === 'Edit') return obj.file_path || obj.path || null;
    if (toolName === 'Grep') return obj.path || null;
  } catch (e) { /* not JSON */ }
  if (toolName === 'apply_patch') {
    var m = inputStr.match(/\*\*\* (?:Update|Add|Delete) File: (.+)/);
    return m ? m[1].trim() : null;
  }
  return null;
}

function extractAllFilePaths(toolName, inputStr) {
  if (!inputStr) return [];
  if (toolName === 'apply_patch') {
    var paths = [];
    var re = /\*\*\* (?:Update|Add|Delete) File: (.+)/g;
    var match;
    while ((match = re.exec(inputStr)) !== null) paths.push(match[1].trim());
    return paths;
  }
  var single = extractFilePath(toolName, inputStr);
  return single ? [single] : [];
}

// ── Render Session Detail (two-column overlay) ────────────────────────

function renderSessionDetail(timeline, stats) {
  var content = document.getElementById('sess-detail-content');
  if (!timeline.length) {
    content.innerHTML = '<div class="loading" style="padding:40px;text-align:center">' + t('empty.no_data') + '</div>';
    return;
  }

  var html = '';

  // ── Header ──
  html += '<div class="sess-overlay-header">';
  html += '<div class="sess-detail-kpi">';
  html += '<div class="sess-kpi"><span class="sess-kpi-label">' + t('sessions.kpi_duration') + '</span><span class="sess-kpi-value">' + fmtDurSec(stats.duration) + '</span></div>';
  html += '<div class="sess-kpi"><span class="sess-kpi-label">' + t('sessions.kpi_tools') + '</span><span class="sess-kpi-value">' + stats.toolCount + '</span></div>';

  var costLabel = stats.costSource === 'otel' ? t('sessions.kpi_cost') : t('sessions.kpi_cost') + ' ~';
  html += '<div class="sess-kpi"><span class="sess-kpi-label">' + costLabel + '</span><span class="sess-kpi-value cost">' + (stats.cost > 0 ? fmtCost(stats.cost) : '\u2014') + '</span></div>';

  // Tokens KPI.
  var tokLabel = stats.hasOTel ? t('sessions.kpi_tokens') : t('sessions.kpi_tokens') + ' ~';
  html += '<div class="sess-kpi"><span class="sess-kpi-label">' + tokLabel + '</span><span class="sess-kpi-value" style="font-size:14px">' + fmtTokens(stats.totalInputTokens) + ' ' + t('sessions.token_in') + ' / ' + fmtTokens(stats.totalOutputTokens) + ' ' + t('sessions.token_out') + '</span></div>';

  // Cache KPI (only if OTel data available and cache > 0).
  if (stats.hasOTel && stats.totalCacheTokens > 0) {
    html += '<div class="sess-kpi"><span class="sess-kpi-label">' + t('sessions.kpi_cache') + '</span><span class="sess-kpi-value" style="font-size:14px">' + Math.round(stats.cacheHitRatio * 100) + '%</span></div>';
  }

  // Buttons — right-aligned.
  var lastEvent = timeline[timeline.length - 1];
  var lastIsEnd = lastEvent && lastEvent.source === 'hook' &&
    (lastEvent.data.event_type === 'SessionEnd' || lastEvent.data.event_type === 'Stop');
  var isRecent = lastEvent && (Date.now() - lastEvent.ts) < 10 * 60 * 1000;
  var isActive = isRecent && !lastIsEnd;
  html += '<div class="sess-kpi" style="margin-left:auto;display:flex;gap:6px;align-items:flex-end">';
  if (isActive) {
    html += '<button class="filter-btn" onclick="AgentCanvas.open(\x27live\x27,{sessionId:\x27' + escapeJSString(sessExpandedId) + '\x27})">' + t('sessions.btn_live_canvas') + '</button>';
  }
  html += '<button class="filter-btn" onclick="AgentCanvas.open(\x27replay\x27,{timelineData:sessTimelineData,speed:1,sessionId:\x27' + escapeJSString(sessExpandedId) + '\x27})">\u25B6 ' + t('sessions.btn_replay') + '</button>';
  html += '<button class="sess-close-btn" onclick="sess_closeDetail()" title="' + t('ui.close') + '" aria-label="' + t('ui.close') + '">\u2715</button>';
  html += '</div>';
  html += '</div>'; // .sess-detail-kpi

  // Secondary row.
  html += '<div class="sess-kpi-secondary">';
  html += '<span style="font-family:monospace;font-size:11px" title="' + escapeHTML(sessExpandedId || '') + '">' + escapeHTML(sessExpandedId || '') + '</span>';
  if (stats.primaryModel) html += '<span>' + escapeHTML(stats.primaryModel) + '</span>';
  if (stats.errorCount > 0) html += '<span class="badge badge-error clickable" onclick="sess_toggleErrors()" style="cursor:pointer">' + stats.errorCount + ' ' + t(stats.errorCount > 1 ? 'sessions.errors_badge_plural' : 'sessions.errors_badge') + '</span>';
  html += '</div>';
  // Error details panel (hidden, toggled by clicking error badge).
  if (stats.apiErrors.length > 0) {
    html += '<div id="sess-error-panel" style="display:none;padding:8px 24px;border-bottom:1px solid var(--border);max-height:200px;overflow-y:auto">';
    for (var ei = 0; ei < stats.apiErrors.length; ei++) {
      var err = stats.apiErrors[ei];
      var ea = sess_parseAttrs(err.log_attributes);
      var errMsg = (ea && ea.error) || t('sessions.error_unknown');
      var errModel = (ea && ea.model) || '';
      var errTs = tsToMs(err.timestamp);
      var errTime = errTs ? new Date(errTs).toLocaleTimeString() : '';
      html += '<div class="tl-item clickable" style="padding:4px 8px;font-size:12px;cursor:pointer" onclick="sess_scrollToEvent(document.getElementById(\x27sess-timeline-scroll\x27),' + (errTs || 0) + ')">';
      html += '<span class="tl-time">' + errTime + '</span>';
      html += '<span class="badge badge-error" style="font-size:10px">' + t('ui.error') + '</span> ';
      if (errModel) html += '<span style="color:var(--text-dim)">' + escapeHTML(errModel) + '</span> ';
      html += '<span>' + escapeHTML(errMsg) + '</span>';
      html += '</div>';
    }
    html += '</div>';
  }
  html += '</div>'; // .sess-overlay-header

  // ── Two-column body ──
  html += '<div class="sess-overlay-body">';

  // Left: Insights panel.
  html += '<div class="sess-insights-panel">';
  html += '<button class="sess-panel-toggle" onclick="sess_togglePanel(\x27left\x27)" title="' + t('ui.expand') + '">&#x21C9;</button>';
  html += sess_renderContextBar(stats.context);
  html += sess_renderWaterfall(timeline, stats);
  if (stats.apiCalls.length > 0) html += sess_renderAPICalls(stats);
  html += sess_renderFileHeatmap(stats.files);
  if (stats.agents.length > 0) html += sess_renderAgentTree(stats.agents, stats.agentToolCounts || {});
  html += '</div>';

  // Right: Timeline panel.
  html += '<div class="sess-timeline-panel">';
  html += '<button class="sess-panel-toggle" onclick="sess_togglePanel(\x27right\x27)" title="' + t('ui.expand') + '">&#x21C7;</button>';

  // Toolbar.
  var toolNames = {};
  for (var i = 0; i < timeline.length; i++) {
    var tn = null;
    if (timeline[i].source === 'tool_pair') tn = timeline[i].data.tool_name;
    else if (timeline[i].source === 'hook' && timeline[i].data.event_type === 'PreToolUse') tn = timeline[i].data.tool_name;
    else if (timeline[i].source === 'message' && timeline[i].data.message_type === 'tool_use') tn = timeline[i].data.tool_name;
    if (tn) toolNames[tn] = true;
  }
  html += '<div class="sess-detail-toolbar">';
  html += '<input class="sess-detail-filter" id="sess-detail-filter" type="text" placeholder="' + t('sessions.filter_placeholder') + '" oninput="sess_filterTimeline()" />';
  html += '<div class="sess-detail-chips">';
  html += '<button class="sess-chip active" onclick="sess_filterByTool(this, \'\')">' + t('sessions.chip_all') + '</button>';
  var toolList = Object.keys(toolNames).sort();
  for (var k = 0; k < toolList.length; k++) {
    html += '<button class="sess-chip" onclick="sess_filterByTool(this, \x27' + escapeJSString(toolList[k]) + '\x27)">' + escapeHTML(toolList[k]) + '</button>';
  }
  html += '</div></div>';

  // Timeline.
  html += '<div class="sess-timeline-scroll" id="sess-timeline-scroll">';
  html += '<div class="sess-timeline" id="sess-timeline-items">';
  for (var m = 0; m < timeline.length; m++) html += renderTimelineItem(timeline[m]);
  html += '</div></div>';

  html += '</div>'; // .sess-timeline-panel
  html += '</div>'; // .sess-overlay-body

  content.innerHTML = html;
  sess_initWaterfallClicks();
  var scrollEl = document.getElementById('sess-timeline-scroll');
  if (scrollEl) {
    if (sessTargetTs) {
      sess_scrollToEvent(scrollEl, sessTargetTs);
      if (sessApiCallFP) sess_highlightAPICall(sessApiCallFP);
      sessTargetTs = 0;
      sessApiCallFP = '';
    } else {
      scrollEl.scrollTop = scrollEl.scrollHeight;
    }
  }
}

// Scroll to the timeline item closest to the target timestamp and highlight it.
function sess_scrollToEvent(scrollEl, targetMs) {
  var items = scrollEl.querySelectorAll('.tl-item-wrap[data-ts]');
  var best = null, bestDiff = Infinity;
  for (var i = 0; i < items.length; i++) {
    var ts = Number(items[i].getAttribute('data-ts'));
    var diff = Math.abs(ts - targetMs);
    if (diff < bestDiff) { bestDiff = diff; best = items[i]; }
  }
  if (!best) { scrollEl.scrollTop = scrollEl.scrollHeight; return; }
  // Clear previous highlight.
  var prev = scrollEl.querySelector('.tl-highlight');
  if (prev) prev.classList.remove('tl-highlight');
  best.classList.add('tl-highlight');
  best.scrollIntoView({ block: 'center' });
}

// Scroll to a timeline tool_pair by tool_use_id (precise CC linkage).
function sess_scrollToToolUseId(toolUseId) {
  var scrollEl = document.getElementById('sess-timeline-scroll');
  if (!scrollEl) return;
  var items = scrollEl.querySelectorAll('.tl-item-wrap[data-tool-use-id]');
  var target = null;
  for (var i = 0; i < items.length; i++) {
    if (items[i].getAttribute('data-tool-use-id') === toolUseId) { target = items[i]; break; }
  }
  if (!target) {
    // Fallback: find by data-tool-use-ids containing this id.
    var allWraps = scrollEl.querySelectorAll('.tl-item-wrap');
    for (var j = 0; j < allWraps.length; j++) {
      var ids = allWraps[j].getAttribute('data-tool-use-id') || '';
      if (ids === toolUseId) { target = allWraps[j]; break; }
    }
  }
  if (!target) return;
  var prev = scrollEl.querySelector('.tl-highlight');
  if (prev) prev.classList.remove('tl-highlight');
  target.classList.add('tl-highlight');
  target.scrollIntoView({ block: 'center' });
}

// Expand the API Calls section and highlight the row matching the fingerprint.
function sess_highlightAPICall(fingerprint) {
  var table = document.querySelector('.sess-api-table');
  if (!table) return;
  var details = table.closest('details.sess-section');
  if (details) details.open = true;
  // Try exact fingerprint match first, then fallback to closest timestamp.
  var trs = table.querySelectorAll('tr[data-fp]');
  var best = null;
  for (var i = 0; i < trs.length; i++) {
    if (trs[i].getAttribute('data-fp') === fingerprint) { best = trs[i]; break; }
  }
  if (!best) {
    // Fallback: try as numeric timestamp for Codex (nanosecond string).
    var targetMs = Number(fingerprint);
    if (targetMs > 0) {
      var bestDiff = Infinity;
      for (var j = 0; j < trs.length; j++) {
        var ts = Number(trs[j].getAttribute('data-ts'));
        var diff = Math.abs(ts - targetMs);
        if (diff < bestDiff) { bestDiff = diff; best = trs[j]; }
      }
    }
  }
  if (!best) return;
  best.classList.add('tl-highlight');
  best.scrollIntoView({ block: 'nearest' });
}

// ── API Calls Section ─────────────────────────────────────────────────

function sess_renderAPICalls(stats) {
  var calls = stats.apiCalls;
  if (!calls.length) return '';

  var html = '<details class="sess-section">';
  html += '<summary>' + t('sessions.api_calls') + ' (' + calls.length + ') \u00B7 ' + fmtCost(stats.cost) + '</summary>';
  html += '<table class="sess-api-table"><thead><tr>';
  html += '<th>' + t('sessions.api_col_model') + '</th><th>' + t('sessions.api_col_in') + '</th><th>' + t('sessions.api_col_out') + '</th><th>' + t('sessions.api_col_cache') + '</th><th>' + t('sessions.api_col_cost') + '</th><th>' + t('sessions.api_col_dur') + '</th>';
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
    html += '<td>' + fmtTokens(c.inputTokens) + '</td>';
    html += '<td>' + fmtTokens(c.outputTokens) + '</td>';
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

// ── Feature: Agent Hierarchy ──────────────────────────────────────────

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

// ── Feature: Session Waterfall ─────────────────────────────────────────

function sess_renderWaterfall(timeline, stats) {
  if (timeline.length < 2) return '';

  var sessionStart = timeline[0].ts;
  var sessionEnd = timeline[timeline.length - 1].ts;
  // Extend sessionEnd to include all tool_pair end_ts values (long-running spans may finish later).
  for (var i = 0; i < timeline.length; i++) {
    if (timeline[i].source === 'tool_pair' && timeline[i].data.end_ts) {
      sessionEnd = Math.max(sessionEnd, timeline[i].data.end_ts);
    }
  }
  // Extend sessionEnd to include API call spans.
  var apiCalls = stats.apiCalls || [];
  for (var ci = 0; ci < apiCalls.length; ci++) {
    var cEnd = (apiCalls[ci].ts || 0) + (apiCalls[ci].durationMs || 0);
    if (cEnd > sessionEnd) sessionEnd = cEnd;
  }
  var totalMs = Math.max(sessionEnd - sessionStart, 1);

  // 1. Build subagent spans from hookEvents in timeline.
  var agentSpans = {}; // agent_id → {id, type, start_ts, end_ts}
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
  var spans = [];
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
  var badgeLabels = { agent: t('sessions.span_agent'), tool: t('sessions.span_tool'), llm: t('sessions.span_llm'), message: t('sessions.span_message') };
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
    }

    // Color for tool bars via inline style.
    var barStyle = 'left:' + leftPct + '%;width:' + widthPct + '%';
    if (s.spanType === 'tool' && !s.failed) barStyle += ';background:' + ganttColor(s.name);

    html += '<div class="waterfall-row waterfall-row-clickable" role="button" tabindex="0" aria-expanded="false" data-wf-idx="' + ri + '">';
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
    sess_toggleWaterfallDetail(container, row, idx);
  });
  container.addEventListener('keydown', function(e) {
    if (e.key !== 'Enter' && e.key !== ' ') return;
    var row = e.target.closest('.waterfall-row-clickable');
    if (!row) return;
    e.preventDefault();
    var idx = parseInt(row.getAttribute('data-wf-idx'), 10);
    if (!isNaN(idx)) sess_toggleWaterfallDetail(container, row, idx);
  });
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
    if (s.data.cacheTokens) pairs.push(['cache_tokens', s.data.cacheTokens]);
    if (s.data.cost) pairs.push(['cost', '$' + s.data.cost.toFixed(4)]);
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

var sess_waterfallFlat = null;

// ── Feature: Timeline Gantt ───────────────────────────────────────────

function sess_renderGantt(ganttData, timeline) {
  if (!ganttData.length) return '';

  var sessionStart = timeline[0].ts;
  var sessionEnd = timeline[timeline.length - 1].ts;
  var duration = sessionEnd - sessionStart;
  if (duration <= 0) return '';

  var lanes = {};
  for (var i = 0; i < ganttData.length; i++) {
    var g = ganttData[i];
    var name = g.tool_name || 'unknown';
    if (!lanes[name]) lanes[name] = [];
    lanes[name].push(g);
  }

  var laneNames = Object.keys(lanes).sort();
  var html = '<details class="sess-section">';
  html += '<summary>' + t('sessions.timeline_gantt') + '</summary>';
  html += '<div class="sess-gantt">';

  for (var li = 0; li < laneNames.length; li++) {
    var ln = laneNames[li];
    var color = ganttColor(ln);
    html += '<div class="sess-gantt-row">';
    html += '<span class="sess-gantt-label">' + escapeHTML(ln) + '</span>';
    html += '<div class="sess-gantt-track">';
    var items = lanes[ln];
    for (var bi = 0; bi < items.length; bi++) {
      var b = items[bi];
      var left = ((b.start_ts - sessionStart) / duration * 100).toFixed(2);
      var width = Math.max(0.3, ((b.end_ts - b.start_ts) / duration * 100)).toFixed(2);
      var durMs = b.end_ts - b.start_ts;
      var durLabel = durMs < 1000 ? durMs + 'ms' : (durMs / 1000).toFixed(1) + 's';
      var barStyle = 'left:' + left + '%;width:' + width + '%;background:' + color;
      if (b.failed) barStyle += ';background:var(--red)';
      html += '<div class="sess-gantt-bar" style="' + barStyle + '" title="' + escapeHTML(ln) + ' \u2014 ' + durLabel + '"></div>';
    }
    html += '</div></div>';
  }

  var durSec = duration / 1000;
  html += '<div class="sess-gantt-time-axis">';
  var ticks = 5;
  for (var ti = 0; ti <= ticks; ti++) {
    var sec = durSec * ti / ticks;
    html += '<span>' + (sec < 60 ? Math.round(sec) + 's' : Math.round(sec / 60) + 'm') + '</span>';
  }
  html += '</div></div></details>';
  return html;
}

// ── Timeline filter ───────────────────────────────────────────────────

var sessActiveToolFilter = '';

function sess_filterByTool(btn, toolName) {
  sessActiveToolFilter = toolName;
  document.querySelectorAll('.sess-chip').forEach(function(c) { c.classList.remove('active'); });
  btn.classList.add('active');
  sess_applyFilters();
}

function sess_filterTimeline() { sess_applyFilters(); }

function sess_applyFilters() {
  var keyword = (document.getElementById('sess-detail-filter').value || '').toLowerCase().trim();
  var filtered = sessTimelineData.filter(function(item) {
    if (sessActiveToolFilter) {
      var tn = null;
      if (item.source === 'tool_pair') tn = item.data.tool_name;
      else if (item.source === 'hook' && item.data.tool_name) tn = item.data.tool_name;
      else if (item.source === 'message' && item.data.tool_name) tn = item.data.tool_name;
      if (tn !== sessActiveToolFilter) return false;
    }
    if (keyword) {
      var text = '';
      if (item.source === 'tool_pair') text = (item.data.tool_name || '') + ' ' + (item.data.tool_input || '') + ' ' + (item.data.tool_result || '');
      else if (item.source === 'hook') text = (item.data.tool_name || '') + ' ' + (item.data.tool_input || '') + ' ' + (item.data.tool_result || '') + ' ' + (item.data.message || '');
      else text = (item.data.content || '') + ' ' + (item.data.tool_name || '');
      if (text.toLowerCase().indexOf(keyword) === -1) return false;
    }
    return true;
  });
  var container = document.getElementById('sess-timeline-items');
  if (!container) return;
  var html = '';
  for (var i = 0; i < filtered.length; i++) html += renderTimelineItem(filtered[i]);
  container.innerHTML = html || '<div class="loading">' + t('empty.no_data') + '</div>';
}

// ── Timeline item rendering ───────────────────────────────────────────

function renderTimelineItem(item) {
  var extraAttrs = '';
  if (item.source === 'tool_pair' && item.data.tool_use_id) {
    extraAttrs = ' data-tool-use-id="' + escapeHTML(item.data.tool_use_id) + '"';
  }
  var wrapper = '<div class="tl-item-wrap" data-ts="' + (item.ts || 0) + '"' + extraAttrs + '>';
  var inner = '';
  if (item.source === 'tool_pair') inner = renderToolPair(item.data, item.ts);
  else if (item.source === 'hook') inner = renderHookEvent(item.data, item.ts);
  else inner = renderMessage(item.data, item.ts);
  return wrapper + inner + '</div>';
}

function renderToolPair(tc, ts) {
  var time = ts ? new Date(ts).toLocaleTimeString() : '';
  var durMs = (tc.end_ts && tc.start_ts) ? tc.end_ts - tc.start_ts : 0;
  var durLabel = durMs < 1000 ? durMs + 'ms' : (durMs / 1000).toFixed(1) + 's';
  var statusClass = tc.failed ? 'tl-tool-card-err' : 'tl-tool-card-ok';
  var statusIcon = tc.failed ? '\u2717' : '\u2713';
  var result = tc.tool_result || '';
  var argsSummary = summarizeToolArgs(tc.tool_name, tc.tool_input);

  var html = '<div class="tl-tool-card ' + statusClass + '">';
  html += '<div class="tl-tool-card-header">';
  html += '<span class="tl-time">' + time + '</span>';
  html += '<span class="tl-tool-name">' + escapeHTML(tc.tool_name || 'unknown') + '</span>';
  html += '<span class="tl-tool-dur">' + durLabel + '</span>';
  html += '<span class="tl-tool-status">' + statusIcon + '</span>';
  html += '</div>';
  if (argsSummary) html += '<div class="tl-tool-card-args">' + escapeHTML(argsSummary) + '</div>';
  if (result) {
    html += '<details class="tl-tool-card-result"><summary>' + t('sessions.result') + '</summary>';
    html += formatToolResult(tc.tool_name, result);
    html += '</details>';
  }
  html += '</div>';
  return html;
}

function formatToolResult(toolName, result) {
  if (!result) return '';
  try {
    var obj = JSON.parse(result);
    if (typeof obj !== 'object' || obj === null) throw new Error('not an object');
    return '<div class="tl-result-structured">' + formatResultObj(toolName, obj) + '</div>';
  } catch (e) {
    var text = result.length > 2000 ? result.slice(0, 2000) + '\u2026' : result;
    return '<pre>' + escapeHTML(text) + '</pre>';
  }
}

function formatResultObj(toolName, obj) {
  var html = '';
  if (obj.stdout != null || obj.stderr != null) {
    if (obj.stdout) html += '<div class="tl-result-field"><span class="tl-result-key">stdout</span><pre>' + escapeHTML(truncResultText(obj.stdout)) + '</pre></div>';
    if (obj.stderr) html += '<div class="tl-result-field"><span class="tl-result-key">stderr</span><pre class="tl-result-err">' + escapeHTML(truncResultText(obj.stderr)) + '</pre></div>';
    if (!html) html = '<div class="tl-result-field"><span class="tl-result-key">stdout</span><pre>' + t('sessions.no_data_result') + '</pre></div>';
    return html;
  }
  if (obj.file && obj.file.content != null) {
    html += '<div class="tl-result-field"><span class="tl-result-key">content</span><pre>' + escapeHTML(truncResultText(obj.file.content)) + '</pre></div>';
    return html;
  }
  if (obj.filePath) {
    html += '<div class="tl-result-field"><span class="tl-result-key">file</span> ' + escapeHTML(obj.filePath) + '</div>';
    if (obj.newString) html += '<div class="tl-result-field"><span class="tl-result-key">new</span><pre>' + escapeHTML(truncResultText(obj.newString)) + '</pre></div>';
    return html;
  }
  if (obj.output != null) {
    html += '<div class="tl-result-field"><span class="tl-result-key">output</span><pre>' + escapeHTML(truncResultText(typeof obj.output === 'string' ? obj.output : JSON.stringify(obj.output))) + '</pre></div>';
    return html;
  }
  var pretty = JSON.stringify(obj, null, 2);
  return '<pre>' + escapeHTML(truncResultText(pretty)) + '</pre>';
}

function truncResultText(s) {
  return s.length > 2000 ? s.slice(0, 2000) + '\u2026' : s;
}

function summarizeToolArgs(toolName, argsStr) {
  if (!argsStr) return '';
  try {
    var obj = JSON.parse(argsStr);
    if (toolName === 'Read' || toolName === 'Write') return obj.file_path || obj.path || argsStr;
    if (toolName === 'Edit') return obj.file_path || obj.path || argsStr;
    if (toolName === 'Bash') return obj.command || argsStr;
    if (toolName === 'Glob') return obj.pattern || argsStr;
    if (toolName === 'Grep') return (obj.pattern || '') + (obj.path ? ' in ' + obj.path : '');
    if (toolName === 'Agent' || toolName === 'Task') return obj.description || obj.prompt || argsStr;
    if (toolName === 'WebSearch') return obj.query || argsStr;
    if (toolName === 'WebFetch') return obj.url || argsStr;
  } catch (e) { /* not JSON */ }
  if (argsStr.length > 120) return argsStr.slice(0, 120) + '\u2026';
  return argsStr;
}

function renderHookEvent(ev, ts) {
  var type = ev.event_type;
  var time = ts ? new Date(ts).toLocaleTimeString() : '';
  if (type === 'SessionStart') return '<div class="tl-item tl-lifecycle"><span class="tl-time">' + time + '</span> <span class="tl-badge tl-badge-start">' + t('sessions.ev_start') + '</span></div>';
  if (type === 'SessionEnd' || type === 'Stop') return '<div class="tl-item tl-lifecycle"><span class="tl-time">' + time + '</span> <span class="tl-badge tl-badge-end">' + t('sessions.ev_end') + '</span></div>';
  if (type === 'PreToolUse') {
    var args = summarizeToolArgs(ev.tool_name, ev.tool_input);
    return '<div class="tl-tool-card tl-tool-card-pending"><div class="tl-tool-card-header"><span class="tl-time">' + time + '</span><span class="tl-tool-name">' + escapeHTML(ev.tool_name || 'unknown') + '</span><span class="tl-tool-dur">\u2026</span></div>' + (args ? '<div class="tl-tool-card-args">' + escapeHTML(args) + '</div>' : '') + '</div>';
  }
  if (type === 'SubagentStart') return '<div class="tl-item tl-subagent"><span class="tl-time">' + time + '</span> <span class="tl-badge tl-badge-sub">\u25B6</span> ' + t('sessions.ev_subagent_start') + ' <strong>' + escapeHTML(ev.agent_type || '') + '</strong></div>';
  if (type === 'SubagentStop') return '<div class="tl-item tl-subagent"><span class="tl-time">' + time + '</span> <span class="tl-badge tl-badge-sub">\u25A0</span> ' + t('sessions.ev_subagent_stop') + '</div>';
  if (type === 'Notification') return '<div class="tl-item tl-notification"><span class="tl-time">' + time + '</span> <span class="tl-badge tl-badge-warn">\u26A0</span> ' + escapeHTML(ev.message || ev.notification_type || t('sessions.ev_notification')) + '</div>';
  return '<div class="tl-item"><span class="tl-time">' + time + '</span> ' + escapeHTML(type) + '</div>';
}

function renderMessage(msg, ts) {
  var time = ts ? new Date(ts).toLocaleTimeString() : '';
  var type = msg.message_type;
  var content = msg.content || '';
  if (type === 'user') return '<div class="tl-item tl-msg-user"><span class="tl-time">' + time + '</span> <span class="tl-role tl-role-user">' + t('sessions.role_user') + '</span> <div class="tl-content">' + escapeHTML(content) + '</div></div>';
  if (type === 'assistant') return '<div class="tl-item tl-msg-assistant"><span class="tl-time">' + time + '</span> <span class="tl-role tl-role-assistant">' + t('sessions.role_assistant') + '</span> <div class="tl-content">' + escapeHTML(content) + '</div></div>';
  if (type === 'thinking') return '<div class="tl-item tl-msg-thinking" onclick="this.classList.toggle(\x27expanded\x27)"><span class="tl-time">' + time + '</span> <span class="tl-role" style="color:var(--purple)">' + t('sessions.role_thinking') + '</span> <div class="tl-content tl-thinking-content">' + escapeHTML(content) + '</div></div>';
  if (type === 'tool_use') {
    var toolLabel = msg.tool_name || 'tool';
    var as = summarizeToolArgs(toolLabel, content);
    return '<div class="tl-tool-card tl-tool-card-ok"><div class="tl-tool-card-header"><span class="tl-time">' + time + '</span><span class="tl-tool-name">' + escapeHTML(toolLabel) + '</span></div>' + (as ? '<div class="tl-tool-card-args">' + escapeHTML(as) + '</div>' : '') + '</div>';
  }
  if (type === 'tool_result') return '<div class="tl-item tl-tool-result"><span class="tl-time">' + time + '</span> <span class="tl-badge tl-badge-ok">\u2713</span> <span class="tl-result">' + escapeHTML(content.length > 200 ? content.slice(0, 200) + '\u2026' : content) + '</span></div>';
  return '<div class="tl-item"><span class="tl-time">' + time + '</span> ' + escapeHTML(content) + '</div>';
}

// ── Search ─────────────────────────────────────────────────────────────

async function sess_search() {
  var q = document.getElementById('sess-search-input').value.trim();
  var el = document.getElementById('sess-search-results');
  if (!q) { el.innerHTML = ''; return; }
  var iv = intervalSQL();
  var results = await Promise.all([
    query("SELECT session_id, ts, 'hook' AS src, event_type AS msg_type, tool_name, COALESCE(tool_input, '') AS content FROM tma1_hook_events WHERE (tool_name LIKE '%" + escapeSQLString(q) + "%' OR tool_input LIKE '%" + escapeSQLString(q) + "%' OR tool_result LIKE '%" + escapeSQLString(q) + "%') AND ts > NOW() - INTERVAL '" + iv + "' ORDER BY ts DESC LIMIT 25").catch(function() { return null; }),
    query("SELECT session_id, ts, 'msg' AS src, message_type AS msg_type, '' AS tool_name, COALESCE(content, '') AS content FROM tma1_messages WHERE matches_term(content, '" + escapeSQLString(q) + "') AND ts > NOW() - INTERVAL '" + iv + "' ORDER BY ts DESC LIMIT 25").catch(function() { return null; }),
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
    html += '<div class="search-result-item clickable" onclick="sess_openDetail(\x27' + escapeJSString(d.session_id) + '\x27,\x27\x27,' + (ms || 0) + ')">';
    html += '<div class="search-result-meta"><span class="badge badge-cc">' + escapeHTML((d.session_id || '').slice(0, 8)) + '</span> ';
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
