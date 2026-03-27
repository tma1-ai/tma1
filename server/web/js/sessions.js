/* Sessions view — session list, conversation timeline, search. */
/* globals: query, rows, rowsToObjects, intervalSQL, fmtNum, fmtCost, escapeHTML, escapeJSString, escapeSQLString, tsToMs, t, loadPricing, modelPricing */

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
        "SELECT AVG(dur) AS v FROM (" +
        "  SELECT (MAX(ts) - MIN(ts)) / 1000 AS dur FROM tma1_hook_events" +
        "  WHERE ts > NOW() - INTERVAL '" + iv + "'" +
        "  GROUP BY session_id" +
        ") sub"
      );
      var dr = rows(dRes);
      if (dr.length && dr[0][0] != null) {
        document.getElementById('sess-val-duration').textContent = fmtDurSec(Number(dr[0][0]));
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
    document.getElementById('sess-detail').style.display = 'none';
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
    var sourceBadge = (d.agent_source === 'codex')
      ? '<span class="badge badge-codex">Codex</span>'
      : '<span class="badge badge-cc">CC</span>';
    var costStr = costMap[sid] != null ? fmtCost(costMap[sid]) : '\u2014';

    var shortSid = sid.length > 8 ? sid.slice(0, 8) : sid;
    html += '<tr class="sess-row clickable" onclick="sess_toggleDetail(\x27' + escapeJSString(sid) + '\x27)">';
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
  // modelPricing from core.js uses keys: { p: pattern, i: inputPrice, o: outputPrice }
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

// ── Session Detail ─────────────────────────────────────────────────────

function sess_toggleDetail(sessionId) {
  var detail = document.getElementById('sess-detail');
  if (sessExpandedId === sessionId) {
    sessExpandedId = null;
    detail.style.display = 'none';
    detail.innerHTML = '';
    return;
  }
  sessExpandedId = sessionId;
  detail.style.display = 'block';
  detail.innerHTML = '<div class="loading">' + t('empty.loading') + '</div>';
  sess_loadDetail(sessionId);
}

async function sess_loadDetail(sessionId) {
  var sid = escapeSQLString(sessionId);
  var results = await Promise.all([
    query(
      "SELECT ts, event_type, tool_name, tool_input, tool_result, " +
      "tool_use_id, agent_id, agent_type, notification_type, \"message\" " +
      "FROM tma1_hook_events WHERE session_id = '" + sid + "' ORDER BY ts ASC"
    ),
    query(
      "SELECT ts, message_type, \"role\", content, model, tool_name, tool_use_id " +
      "FROM tma1_messages WHERE session_id = '" + sid + "' ORDER BY ts ASC"
    ).catch(function() { return null; }),
  ]);

  var hookEvents = rowsToObjects(results[0]);
  var messages = results[1] ? rowsToObjects(results[1]) : [];

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
  timeline.sort(function (a, b) { return a.ts - b.ts; });

  sessTimelineData = timeline;
  await loadPricing();
  sessCurrentStats = sess_computeStats(hookEvents, messages, timeline);

  // Try to get actual cost from OTel logs (more accurate than content-length estimate).
  try {
    var costRes = await query(
      "SELECT ROUND(SUM(json_get_float(log_attributes, 'cost_usd')), 4) AS cost " +
      "FROM opentelemetry_logs WHERE body = 'claude_code.api_request' " +
      "AND timestamp BETWEEN " +
      "'" + new Date(timeline[0].ts).toISOString() + "'::TIMESTAMP " +
      "AND '" + new Date(timeline[timeline.length - 1].ts + 60000).toISOString() + "'::TIMESTAMP"
    );
    var cr = rows(costRes);
    if (cr.length && cr[0][0] != null && Number(cr[0][0]) > 0) {
      sessCurrentStats.cost = Number(cr[0][0]);
      sessCurrentStats.costSource = 'otel';
    }
  } catch (e) { /* OTel logs not available, use estimate */ }

  renderSessionDetail(timeline, sessCurrentStats);
}

// ── Compute Stats (single pass) ────────────────────────────────────────

function sess_computeStats(hookEvents, messages, timeline) {
  var stats = {
    duration: 0, toolCount: 0, primaryModel: '', cost: 0,
    files: {},      // { path: { reads: N, writes: N } }
    context: { system: 5000, user: 0, tools: 0, reasoning: 0, subagent: 0 },
    agents: [],     // SubagentStart events
    gantt: [],      // { tool_name, start_ts, end_ts, failed }
  };

  // Duration from timeline bounds.
  if (timeline.length > 0) {
    var first = timeline[0].ts;
    var last = timeline[timeline.length - 1].ts;
    stats.duration = (last - first) / 1000;
  }

  // Tool pairs → file attention + gantt + tool count.
  for (var i = 0; i < timeline.length; i++) {
    var item = timeline[i];
    if (item.source === 'tool_pair') {
      stats.toolCount++;
      var tc = item.data;
      stats.gantt.push(tc);
      // Extract file paths (may be multiple for apply_patch).
      var fps = extractAllFilePaths(tc.tool_name, tc.tool_input);
      for (var fi = 0; fi < fps.length; fi++) {
        var fp = fps[fi];
        if (!stats.files[fp]) stats.files[fp] = { reads: 0, writes: 0 };
        if (tc.tool_name === 'Write' || tc.tool_name === 'Edit' || tc.tool_name === 'apply_patch') stats.files[fp].writes++;
        else stats.files[fp].reads++;
      }
      // Tool result tokens for context breakdown.
      var resultLen = (tc.tool_result || '').length;
      var mult = (tc.tool_name === 'Read' ? 1.0 : tc.tool_name === 'Grep' || tc.tool_name === 'Glob' ? 0.5 : 0.3);
      if (tc.tool_name === 'Agent' || tc.tool_name === 'Task') {
        stats.context.subagent += Math.round(resultLen / 4 * mult);
      } else {
        stats.context.tools += Math.round(resultLen / 4 * mult);
      }
    }
  }

  // Agent hierarchy: collect subagent events + per-agent tool counts.
  var agentToolCounts = {}; // agent_id → count
  for (var h = 0; h < hookEvents.length; h++) {
    var hev = hookEvents[h];
    if (hev.event_type === 'SubagentStart') stats.agents.push(hev);
    if (hev.event_type === 'PreToolUse') {
      var aid = hev.agent_id || '';
      agentToolCounts[aid] = (agentToolCounts[aid] || 0) + 1;
    }
  }
  stats.agentToolCounts = agentToolCounts;

  // Messages → context breakdown + model + cost.
  var inputTokens = 0;
  var outputTokens = 0;
  for (var m = 0; m < messages.length; m++) {
    var msg = messages[m];
    var contentLen = (msg.content || '').length;
    var tokens = Math.round(contentLen / 4);
    if (!stats.primaryModel && msg.model) stats.primaryModel = msg.model;

    if (msg.message_type === 'user') {
      stats.context.user += tokens;
      inputTokens += tokens;
    } else if (msg.message_type === 'tool_result') {
      // Tool results overcount tokens — content is raw text, actual tokens are fewer.
      inputTokens += Math.round(tokens * 0.3);
    } else if (msg.message_type === 'tool_use') {
      inputTokens += tokens;
    } else if (msg.message_type === 'assistant' || msg.message_type === 'thinking') {
      stats.context.reasoning += tokens;
      outputTokens += tokens;
    }
  }

  // Cost.
  var price = sess_lookupPrice(stats.primaryModel);
  stats.cost = inputTokens * price.input / 1000000 + outputTokens * price.output / 1000000;

  return stats;
}

function extractFilePath(toolName, inputStr) {
  if (!inputStr) return null;
  // Claude Code tools.
  try {
    var obj = JSON.parse(inputStr);
    if (toolName === 'Read' || toolName === 'Write' || toolName === 'Edit') return obj.file_path || obj.path || null;
    if (toolName === 'Grep') return obj.path || null;
  } catch (e) { /* not JSON */ }
  // Codex: apply_patch — extract file path from "*** Update File: /path" lines.
  if (toolName === 'apply_patch') {
    var m = inputStr.match(/\*\*\* (?:Update|Add|Delete) File: (.+)/);
    return m ? m[1].trim() : null;
  }
  return null;
}

// Extract ALL file paths from a tool input (for file heatmap — multiple files per apply_patch).
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

// ── Render Session Detail ──────────────────────────────────────────────

function renderSessionDetail(timeline, stats) {
  var detail = document.getElementById('sess-detail');
  if (!timeline.length) {
    detail.innerHTML = '<div class="loading">' + t('empty.no_data') + '</div>';
    return;
  }

  var html = '';

  // 1. KPI row.
  html += '<div class="sess-detail-kpi">';
  html += '<div class="sess-kpi"><span class="sess-kpi-label">' + t('sessions.kpi_session') + '</span><span class="sess-kpi-value" style="font-size:11px;font-family:monospace;word-break:break-all">' + escapeHTML(sessExpandedId || '') + '</span></div>';
  html += '<div class="sess-kpi"><span class="sess-kpi-label">' + t('sessions.kpi_duration') + '</span><span class="sess-kpi-value">' + fmtDurSec(stats.duration) + '</span></div>';
  html += '<div class="sess-kpi"><span class="sess-kpi-label">' + t('sessions.kpi_tools') + '</span><span class="sess-kpi-value">' + stats.toolCount + '</span></div>';
  var costLabel = stats.costSource === 'otel' ? t('sessions.kpi_cost') : t('sessions.kpi_cost') + ' ~';
  html += '<div class="sess-kpi"><span class="sess-kpi-label">' + costLabel + '</span><span class="sess-kpi-value cost">' + (stats.cost > 0 ? fmtCost(stats.cost) : '\u2014') + '</span></div>';
  html += '<div class="sess-kpi"><span class="sess-kpi-label">' + t('sessions.kpi_model') + '</span><span class="sess-kpi-value" style="font-size:13px">' + escapeHTML(stats.primaryModel || '\u2014') + '</span></div>';
  // Session is active if the last event is recent (within 10 min) and not a final Stop/SessionEnd.
  var lastEvent = timeline[timeline.length - 1];
  var lastIsEnd = lastEvent && lastEvent.source === 'hook' &&
    (lastEvent.data.event_type === 'SessionEnd' || lastEvent.data.event_type === 'Stop');
  var isRecent = lastEvent && (Date.now() - lastEvent.ts) < 10 * 60 * 1000;
  var isActive = isRecent && !lastIsEnd;
  html += '<div class="sess-kpi" style="margin-left:auto;display:flex;gap:6px">';
  if (isActive) {
    html += '<button class="filter-btn" onclick="AgentCanvas.open(\x27live\x27,{sessionId:\x27' + escapeJSString(sessExpandedId) + '\x27})">' + t('sessions.btn_live_canvas') + '</button>';
  }
  html += '<button class="filter-btn" onclick="AgentCanvas.open(\x27replay\x27,{timelineData:sessTimelineData,speed:1,sessionId:\x27' + escapeJSString(sessExpandedId) + '\x27})">\u25B6 ' + t('sessions.btn_replay') + '</button>';
  html += '</div>';
  html += '</div>';

  // 2. Context window breakdown.
  html += sess_renderContextBar(stats.context);

  // 3. File attention heatmap.
  html += sess_renderFileHeatmap(stats.files);

  // 4. Agent hierarchy (only if subagents exist).
  if (stats.agents.length > 0) html += sess_renderAgentTree(stats.agents, stats.agentToolCounts || {});

  // 5. Timeline gantt.
  if (stats.gantt.length > 0) html += sess_renderGantt(stats.gantt, timeline);

  // 6. Toolbar (filter + chips).
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

  // 7. Scrollable event timeline.
  html += '<div class="sess-timeline-scroll" id="sess-timeline-scroll">';
  html += '<div class="sess-timeline" id="sess-timeline-items">';
  for (var m = 0; m < timeline.length; m++) html += renderTimelineItem(timeline[m]);
  html += '</div></div>';

  detail.innerHTML = html;
  var scrollEl = document.getElementById('sess-timeline-scroll');
  if (scrollEl) scrollEl.scrollTop = scrollEl.scrollHeight;
}

// ── Feature 1: File Attention Heatmap ──────────────────────────────────

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
    // Show last 2-3 path segments for readability.
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

// ── Feature 3: Context Window Breakdown ────────────────────────────────

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

// ── Feature 4: Agent Hierarchy ─────────────────────────────────────────

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

// ── Feature 5: Timeline Gantt ──────────────────────────────────────────

function sess_renderGantt(ganttData, timeline) {
  if (!ganttData.length) return '';

  var sessionStart = timeline[0].ts;
  var sessionEnd = timeline[timeline.length - 1].ts;
  var duration = sessionEnd - sessionStart;
  if (duration <= 0) return '';

  // Group by tool name (swimlanes).
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

  // Time axis.
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

// ── Timeline filter (existing) ─────────────────────────────────────────

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

function renderTimelineItem(item) {
  if (item.source === 'tool_pair') return renderToolPair(item.data, item.ts);
  if (item.source === 'hook') return renderHookEvent(item.data, item.ts);
  return renderMessage(item.data, item.ts);
}

// ── Render: paired tool call ───────────────────────────────────────────

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
  // Try parsing as JSON for structured display.
  try {
    var obj = JSON.parse(result);
    if (typeof obj !== 'object' || obj === null) throw new Error('not an object');
    return '<div class="tl-result-structured">' + formatResultObj(toolName, obj) + '</div>';
  } catch (e) {
    // Not JSON — show as plain text.
    var text = result.length > 2000 ? result.slice(0, 2000) + '\u2026' : result;
    return '<pre>' + escapeHTML(text) + '</pre>';
  }
}

function formatResultObj(toolName, obj) {
  var html = '';
  // Bash: show stdout/stderr.
  if (obj.stdout != null || obj.stderr != null) {
    if (obj.stdout) html += '<div class="tl-result-field"><span class="tl-result-key">stdout</span><pre>' + escapeHTML(truncResultText(obj.stdout)) + '</pre></div>';
    if (obj.stderr) html += '<div class="tl-result-field"><span class="tl-result-key">stderr</span><pre class="tl-result-err">' + escapeHTML(truncResultText(obj.stderr)) + '</pre></div>';
    if (!html) html = '<div class="tl-result-field"><span class="tl-result-key">stdout</span><pre>' + t('sessions.no_data_result') + '</pre></div>';
    return html;
  }
  // Read: show file content snippet.
  if (obj.file && obj.file.content != null) {
    html += '<div class="tl-result-field"><span class="tl-result-key">content</span><pre>' + escapeHTML(truncResultText(obj.file.content)) + '</pre></div>';
    return html;
  }
  // Edit: show filePath + newString preview.
  if (obj.filePath) {
    html += '<div class="tl-result-field"><span class="tl-result-key">file</span> ' + escapeHTML(obj.filePath) + '</div>';
    if (obj.newString) html += '<div class="tl-result-field"><span class="tl-result-key">new</span><pre>' + escapeHTML(truncResultText(obj.newString)) + '</pre></div>';
    return html;
  }
  // Codex exec_command output.
  if (obj.output != null) {
    html += '<div class="tl-result-field"><span class="tl-result-key">output</span><pre>' + escapeHTML(truncResultText(typeof obj.output === 'string' ? obj.output : JSON.stringify(obj.output))) + '</pre></div>';
    return html;
  }
  // Generic: pretty-print JSON.
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

// ── Render: non-tool hook events ───────────────────────────────────────

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
  if (type === 'Notification') return '<div class="tl-item tl-notification"><span class="tl-time">' + time + '</span> <span class="tl-badge tl-badge-warn">\u26A0</span> ' + escapeHTML(ev.message || ev.notification_type || 'Notification') + '</div>';
  return '<div class="tl-item"><span class="tl-time">' + time + '</span> ' + escapeHTML(type) + '</div>';
}

// ── Render: conversation messages ──────────────────────────────────────

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
    query("SELECT session_id, ts, 'msg' AS src, message_type AS msg_type, '' AS tool_name, COALESCE(content, '') AS content FROM tma1_messages WHERE content LIKE '%" + escapeSQLString(q) + "%' AND ts > NOW() - INTERVAL '" + iv + "' ORDER BY ts DESC LIMIT 25").catch(function() { return null; }),
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
    html += '<div class="search-result-item clickable" onclick="sess_toggleDetail(\x27' + escapeJSString(d.session_id) + '\x27)">';
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
