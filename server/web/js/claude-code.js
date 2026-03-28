// claude-code.js — Claude Code view: all cc_* functions
// Depends on: core.js, chart.js, i18n.js

var ccEventsPage = 0;
var ccEventsPageSize = 15;
var ccEventsHasNext = false;

var CC_TOOL_CATEGORIES = {
  'Read': 'file_read', 'View': 'file_read',
  'Glob': 'file_read', 'Grep': 'file_read',
  'Write': 'file_write', 'Edit': 'file_write', 'MultiEdit': 'file_write',
  'Bash': 'shell',
  'WebFetch': 'web', 'WebSearch': 'web',
  'Task': 'agent',
};
function ccToolCategory(name) {
  return CC_TOOL_CATEGORIES[name] || 'other';
}

var CC_CATEGORY_ICONS = {
  'file_read': '\u{1F4C4}', 'file_write': '\u270F\uFE0F',
  'shell': '\u{1F4BB}', 'web': '\u{1F310}', 'agent': '\u{1F916}', 'other': '\u{1F527}',
};

function cc_resetEventsPaging() {
  ccEventsPage = 0;
}

function cc_onEventsFilterChange() {
  cc_resetEventsPaging();
  cc_loadEvents();
}

function cc_prevEventsPage() {
  if (ccEventsPage <= 0) return;
  ccEventsPage--;
  cc_loadEvents(false);
}

function cc_nextEventsPage() {
  if (!ccEventsHasNext) return;
  ccEventsPage++;
  cc_loadEvents(false);
}

function cc_updateEventsPager(resultCount) {
  var prevBtn = document.getElementById('cc-events-prev-btn');
  var nextBtn = document.getElementById('cc-events-next-btn');
  var info = document.getElementById('cc-events-page-info');
  if (!prevBtn || !nextBtn || !info) return;

  prevBtn.disabled = ccEventsPage <= 0;
  nextBtn.disabled = !ccEventsHasNext;
  if (!resultCount) {
    info.textContent = t('pager.no_results');
    return;
  }
  var start = ccEventsPage * ccEventsPageSize + 1;
  var end = start + resultCount - 1;
  info.textContent = t('pager.page') + ' ' + (ccEventsPage + 1) + ' \u00b7 ' + start + '-' + end;
}

// ===================================================================
// Claude Code view — Cards
// ===================================================================
async function cc_loadCards() {
  var iv = intervalSQL();
  try {
    var results = await Promise.all([
      query("SELECT ROUND(SUM(json_get_float(log_attributes, 'cost_usd')), 4) AS v FROM opentelemetry_logs WHERE body = 'claude_code.api_request' AND timestamp > NOW() - INTERVAL '" + iv + "'"),
      query("SELECT SUM(COALESCE(json_get_int(log_attributes, 'input_tokens'),0) + COALESCE(json_get_int(log_attributes, 'output_tokens'),0) + COALESCE(json_get_int(log_attributes, 'cache_read_tokens'),0) + COALESCE(json_get_int(log_attributes, 'cache_creation_tokens'),0)) AS v FROM opentelemetry_logs WHERE body = 'claude_code.api_request' AND timestamp > NOW() - INTERVAL '" + iv + "'"),
      // Card display: api_request count only
      query("SELECT COUNT(*) AS v FROM opentelemetry_logs WHERE body = 'claude_code.api_request' AND timestamp > NOW() - INTERVAL '" + iv + "'"),
      query("SELECT ROUND(AVG(json_get_float(log_attributes, 'duration_ms')), 0) AS v FROM opentelemetry_logs WHERE body = 'claude_code.api_request' AND timestamp > NOW() - INTERVAL '" + iv + "'"),
      // Gating: count ANY claude_code event (broader than api_request)
      query("SELECT COUNT(*) AS v FROM opentelemetry_logs WHERE (body LIKE 'claude_code.%' OR scope_name = 'com.anthropic.claude_code.events') AND timestamp > NOW() - INTERVAL '" + iv + "'"),
    ]);
    var reqCount = Number(rows(results[2])?.[0]?.[0]) || 0;
    var anyCount = Number(rows(results[4])?.[0]?.[0]) || 0;
    document.getElementById('cc-val-cost').textContent = fmtCost(rows(results[0])?.[0]?.[0]);
    document.getElementById('cc-val-tokens').textContent = fmtNum(rows(results[1])?.[0]?.[0]);
    document.getElementById('cc-val-requests').textContent = fmtNum(reqCount);
    var latVal = rows(results[3])?.[0]?.[0];
    document.getElementById('cc-val-latency').textContent = fmtDurMs(latVal);
    return anyCount > 0;
  } catch (err) {
    // Fallback: check if any claude_code metric tables have data
    if (typeof dataSources !== 'undefined' && dataSources.ccMetrics) {
      for (var i = 0; i < dataSources.ccMetrics.length; i++) {
        try {
          var mr = await query("SELECT 1 FROM " + dataSources.ccMetrics[i] + " LIMIT 1");
          if ((rows(mr) || []).length > 0) return true;
        } catch (_) { /* table query failed */ }
      }
    }
    var banner = document.getElementById('error-banner');
    banner.style.display = 'block';
    banner.textContent = t('error.cc_metrics') + err.message;
    return false;
  }
}

// ===================================================================
// Claude Code view — Overview tab
// ===================================================================
async function cc_loadOverview() {
  await Promise.all([
    cc_loadTokenChart(),
    cc_loadCostChart(),
    cc_loadLatencyChart(),
    cc_loadErrorChart(),
    cc_loadActivityHeatmap(),
    cc_loadMetricsExplorer(),
  ]);
}

async function cc_loadTokenChart() {
  try {
    var res = await query(
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, timestamp) AS t, " +
      "SUM(json_get_int(log_attributes, 'input_tokens')) AS inp, " +
      "SUM(json_get_int(log_attributes, 'output_tokens')) AS outp, " +
      "SUM(json_get_int(log_attributes, 'cache_read_tokens')) AS cache_read, " +
      "SUM(json_get_int(log_attributes, 'cache_creation_tokens')) AS cache_create " +
      "FROM opentelemetry_logs " +
      "WHERE body = 'claude_code.api_request' AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;
    renderChart('cc-chart-tokens', data, [
      { label: t('chart.input_tokens'), key: 'inp', color: '#d2a8ff' },
      { label: t('chart.output_tokens'), key: 'outp', color: '#f0883e' },
      { label: t('chart.cache_read'), key: 'cache_read', color: '#3fb950' },
      { label: t('chart.cache_creation'), key: 'cache_create', color: '#79c0ff' },
    ], function(v) { return fmtNum(v); });
  } catch { /* no data */ }
}

async function cc_loadCostChart() {
  try {
    var res = await query(
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, timestamp) AS t, " +
      "SUM(json_get_float(log_attributes, 'cost_usd')) AS cost " +
      "FROM opentelemetry_logs " +
      "WHERE body = 'claude_code.api_request' AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;
    renderChart('cc-chart-cost', data, [
      { label: t('chart.cost_usd'), key: 'cost', color: '#f0883e' },
    ], function(v) { return '$' + Number(v).toFixed(4); });
  } catch { /* no data */ }
}

async function cc_loadLatencyChart() {
  try {
    var res = await query(
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, timestamp) AS t, " +
      "ROUND(APPROX_PERCENTILE_CONT(json_get_float(log_attributes, 'duration_ms'), 0.50), 0) AS p50_ms, " +
      "ROUND(APPROX_PERCENTILE_CONT(json_get_float(log_attributes, 'duration_ms'), 0.95), 0) AS p95_ms " +
      "FROM opentelemetry_logs " +
      "WHERE body = 'claude_code.api_request' AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;
    renderChart('cc-chart-latency', data, [
      { label: t('chart.p50'), key: 'p50_ms', color: '#3fb950' },
      { label: t('chart.p95'), key: 'p95_ms', color: '#d2a8ff' },
    ], function(v) { return fmtDurMs(v); });
  } catch { /* no data */ }
}

async function cc_loadErrorChart() {
  try {
    var res = await query(
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, timestamp) AS t, " +
      "SUM(CASE WHEN body = 'claude_code.api_request' THEN 1 ELSE 0 END) AS total, " +
      "SUM(CASE WHEN body = 'claude_code.api_error' THEN 1 ELSE 0 END) AS errors " +
      "FROM opentelemetry_logs " +
      "WHERE body IN ('claude_code.api_request', 'claude_code.api_error') " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;
    renderChart('cc-chart-errors', data, [
      { label: t('chart.total'), key: 'total', color: '#3fb950' },
      { label: t('chart.errors'), key: 'errors', color: '#f85149' },
    ], function(v) { return fmtNum(v); });
  } catch { /* no data */ }
}

async function cc_loadToolDistribution() {
  try {
    var tc = getThemeColors();
    var res = await query(
      "SELECT json_get_string(log_attributes, 'tool_name') AS tool, " +
      "COUNT(*) AS cnt, " +
      "SUM(CASE WHEN json_get_string(log_attributes, 'success') = 'false' THEN 1 ELSE 0 END) AS errs " +
      "FROM opentelemetry_logs " +
      "WHERE body = 'claude_code.tool_result' " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY tool ORDER BY cnt DESC LIMIT 15"
    );
    var data = rowsToObjects(res);
    var el = document.getElementById('cc-tool-distribution');
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_tool_data') + '</div>'; return; }
    var maxCnt = Math.max.apply(null, data.map(function(d) { return Number(d.cnt) || 0; }));
    el.innerHTML = data.map(function(d) {
      var cnt = Number(d.cnt) || 0;
      var errs = Number(d.errs) || 0;
      var pct = maxCnt > 0 ? (cnt / maxCnt * 100) : 0;
      var errPct = cnt > 0 ? (errs / cnt * 100) : 0;
      return '<div class="bar-row">' +
        '<div class="bar-label" title="' + escapeHTML(d.tool) + '">' + escapeHTML(d.tool || 'unknown') + '</div>' +
        '<div class="bar-track bar-track-stacked">' +
          '<div class="bar-fill" style="width:' + pct + '%;background:' + tc.green + '"></div>' +
          (errs > 0 ? '<div class="bar-fill-error" style="width:' + (pct * errPct / 100) + '%;left:' + (pct - pct * errPct / 100) + '%"></div>' : '') +
        '</div>' +
        '<div class="bar-value">' + fmtNum(cnt) + (errs > 0 ? ' <span style="color:' + tc.red + '">(' + errs + t('ui.err_suffix') + '</span>' : '') + '</div>' +
        '</div>';
    }).join('');
  } catch { /* no data */ }
}

async function cc_loadToolPerformance() {
  var tbody = document.getElementById('cc-tool-perf-body');
  try {
    var res = await query(
      "SELECT json_get_string(log_attributes, 'tool_name') AS tool, " +
      "COUNT(*) AS calls, " +
      "SUM(CASE WHEN json_get_string(log_attributes, 'success') = 'true' THEN 1 ELSE 0 END) AS ok, " +
      "ROUND(AVG(json_get_float(log_attributes, 'duration_ms')), 0) AS avg_ms, " +
      "ROUND(SUM(json_get_int(log_attributes, 'tool_result_size_bytes')) / 1024.0, 1) AS total_kb " +
      "FROM opentelemetry_logs " +
      "WHERE body = 'claude_code.tool_result' " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY tool ORDER BY calls DESC LIMIT 15"
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      tbody.innerHTML = '<tr><td colspan="5" class="loading">' + t('empty.no_tool_data') + '</td></tr>';
      return;
    }
    var tc = getThemeColors();
    tbody.innerHTML = data.map(function(d) {
      var calls = Number(d.calls) || 0;
      var ok = Number(d.ok) || 0;
      var rate = calls > 0 ? ((ok / calls) * 100).toFixed(1) : '0.0';
      var rateColor = Number(rate) >= 95 ? tc.green : Number(rate) >= 80 ? tc.yellow : tc.red;
      return '<tr>' +
        '<td>' + escapeHTML(d.tool || 'unknown') + '</td>' +
        '<td>' + fmtNum(calls) + '</td>' +
        '<td><span style="color:' + rateColor + '">' + rate + '%</span></td>' +
        '<td>' + fmtDurMs(d.avg_ms) + '</td>' +
        '<td>' + (d.total_kb != null ? d.total_kb + ' KB' : '\u2014') + '</td></tr>';
    }).join('');
  } catch {
    tbody.innerHTML = '<tr><td colspan="5" class="loading">' + t('error.load_tool_perf') + '</td></tr>';
  }
}

async function cc_loadActivityHeatmap() {
  var el = document.getElementById('cc-activity-heatmap');
  if (!el) return;
  var cfg = heatmapConfig();
  try {
    var res = await query(
      "SELECT date_bin('" + cfg.bucket + "'::INTERVAL, timestamp) AS t, COUNT(*) AS cnt " +
      "FROM opentelemetry_logs " +
      "WHERE body = 'claude_code.api_request' " +
      "  AND timestamp > NOW() - INTERVAL '" + cfg.interval + "' " +
      "GROUP BY t ORDER BY t"
    );
    renderHeatmap('cc-activity-heatmap', rowsToObjects(res));
  } catch {
    el.innerHTML = '<div class="chart-empty">' + t('error.load_activity') + '</div>';
  }
}

function cc_loadMetricsExplorer() {
  initMetricsExplorer('cc-metrics');
}

// ===================================================================
// Claude Code view — Events tab
// ===================================================================
async function cc_loadEvents(reloadPromptTraces) {
  if (reloadPromptTraces !== false) cc_loadPromptTraces();
  var filter = document.getElementById('cc-event-filter').value;
  var iv = intervalSQL();
  var limit = ccEventsPageSize + 1;
  var offset = ccEventsPage * ccEventsPageSize;
  var where = "WHERE body LIKE 'claude_code.%' AND timestamp > NOW() - INTERVAL '" + iv + "'";
  if (filter) where = "WHERE body = '" + escapeSQLString(filter) + "' AND timestamp > NOW() - INTERVAL '" + iv + "'";

  try {
    var res = await query(
      "SELECT timestamp, body, " +
      "json_get_string(log_attributes, 'model') AS model, " +
      "json_get_int(log_attributes, 'input_tokens') AS input_tok, " +
      "json_get_int(log_attributes, 'output_tokens') AS output_tok, " +
      "json_get_int(log_attributes, 'cache_read_tokens') AS cache_read, " +
      "json_get_int(log_attributes, 'cache_creation_tokens') AS cache_create, " +
      "json_get_float(log_attributes, 'cost_usd') AS cost_usd, " +
      "json_get_float(log_attributes, 'duration_ms') AS duration_ms, " +
      "json_get_string(log_attributes, 'tool_name') AS tool_name, " +
      "json_get_string(log_attributes, 'success') AS success, " +
      "json_get_string(log_attributes, 'error') AS error, " +
      "log_attributes " +
      "FROM opentelemetry_logs " +
      where + " ORDER BY timestamp DESC LIMIT " + limit + " OFFSET " + offset
    );
    var allRows = rowsToObjects(res);
    ccEventsHasNext = allRows.length > ccEventsPageSize;
    var data = ccEventsHasNext ? allRows.slice(0, ccEventsPageSize) : allRows;
    var tbody = document.getElementById('cc-events-body');
    if (!data.length) {
      if (ccEventsPage > 0) {
        ccEventsPage--;
        return cc_loadEvents(reloadPromptTraces);
      }
      tbody.innerHTML = '<tr><td colspan="8" class="loading">' + t('empty.no_events') + '</td></tr>';
      cc_updateEventsPager(0);
      return;
    }
    tbody.innerHTML = data.map(function(d, i) {
      var evtName = (d.body || '').replace('claude_code.', '');
      var tokens = (d.input_tok || d.output_tok) ? fmtNum((Number(d.input_tok) || 0) + (Number(d.output_tok) || 0)) : '\u2014';
      var cacheRead = Number(d.cache_read) || 0;
      var cacheCreate = Number(d.cache_create) || 0;
      var cacheDisplay = '\u2014';
      if (cacheRead || cacheCreate) {
        var total = cacheRead + cacheCreate;
        var hitPct = total > 0 ? ((cacheRead / total) * 100).toFixed(0) : '0';
        cacheDisplay = fmtNum(cacheRead) + ' / ' + fmtNum(cacheCreate) + ' <span style="color:var(--text-muted)">(' + hitPct + t('ui.cache_hit_pct') + '</span>';
      }
      var cost = d.cost_usd != null ? fmtCost(d.cost_usd) : '\u2014';
      var dur = fmtDurMs(d.duration_ms);
      var isErr = d.error || d.success === 'false' || evtName === 'api_error';
      var label = d.tool_name ? evtName + ' (' + d.tool_name + ')' : evtName;
      var parsedAttrs = cc_parseAttrs(d.log_attributes);
      var evtSeq = cc_attr(parsedAttrs, 'event.sequence');
      var sessionId = cc_attr(parsedAttrs, 'session.id');
      var attrsStr = typeof d.log_attributes === 'string' ? d.log_attributes : JSON.stringify(d.log_attributes || {});
      return '<tr class="clickable" onclick="cc_toggleEventDetail(this, ' + i + ')" data-idx="' + i + '" data-event-ts="' + escapeHTML(String(d.timestamp || '')) + '" data-event-seq="' + (evtSeq != null ? String(evtSeq) : '') + '" data-session-id="' + escapeHTML(sessionId || '') + '" data-attrs="' + escapeHTML(attrsStr) + '">' +
        '<td>' + fmtTime(d.timestamp) + '</td>' +
        '<td>' + escapeHTML(label) + '</td>' +
        '<td>' + escapeHTML(d.model || '\u2014') + '</td>' +
        '<td>' + tokens + '</td>' +
        '<td>' + cacheDisplay + '</td>' +
        '<td>' + cost + '</td>' +
        '<td>' + dur + '</td>' +
        '<td><span class="badge ' + (isErr ? 'badge-error' : 'badge-ok') + '">' + (isErr ? t('filter.error') : t('filter.ok')) + '</span></td>' +
        '</tr>';
    }).join('');
    cc_updateEventsPager(data.length);
  } catch (err) {
    ccEventsHasNext = false;
    cc_updateEventsPager(0);
    document.getElementById('cc-events-body').innerHTML =
      '<tr><td colspan="8" class="loading">' + t('ui.error_prefix') + escapeHTML(err.message) + '</td></tr>';
  }
}

function cc_toggleEventDetail(clickedRow, idx) {
  var prev = document.querySelector('.cc-event-detail-row');
  if (prev) {
    var prevIdx = prev.dataset.idx;
    prev.remove();
    if (String(prevIdx) === String(idx)) return;
  }
  var attrs = clickedRow.dataset.attrs;
  var formatted = attrs;
  try { formatted = JSON.stringify(JSON.parse(attrs), null, 2); } catch (_) { /* ignore parse error */ }
  var detailRow = document.createElement('tr');
  detailRow.className = 'cc-event-detail-row trace-detail-row';
  detailRow.dataset.idx = idx;
  detailRow.innerHTML = '<td colspan="8"><div class="trace-detail-inner">' +
    '<div class="detail-header"><h3>' + t('detail.event_details') + '</h3>' +
    '<button class="close-btn" onclick="this.closest(\'.cc-event-detail-row\').remove()">&times;</button></div>' +
    '<pre style="font-size:12px;color:var(--text-muted);overflow-x:auto;white-space:pre-wrap;word-break:break-all">' +
    escapeHTML(formatted) + '</pre></div></td>';
  clickedRow.after(detailRow);
}

function cc_parseAttrs(raw) {
  if (!raw) return {};
  if (typeof raw === 'object') return raw;
  try { return JSON.parse(raw); } catch { return {}; }
}

function cc_attr(attrs, key) {
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

function cc_sortPromptEvents(a, b) {
  if (a.seq != null && b.seq != null) return a.seq - b.seq;
  return tsToMs(a.timestamp) - tsToMs(b.timestamp);
}

function cc_togglePromptTraceDetail(item) {
  var detail = item.querySelector('.cc-prompt-detail');
  if (!detail) return;
  detail.style.display = detail.style.display === 'none' ? 'block' : 'none';
}

function cc_newTraceGroup(id, sessionId, latestTs) {
  return {
    promptId: String(id),
    sessionId: sessionId || null,
    events: [],
    models: {},
    cost: 0,
    errors: 0,
    reqs: 0,
    tools: 0,
    latestTs: latestTs,
  };
}

function cc_addEventToTraceGroup(g, e) {
  if (e.model) g.models[e.model] = true;
  if (e.body === 'claude_code.api_request') {
    g.reqs++;
    g.cost += e.cost || 0;
  }
  if (e.body === 'claude_code.api_error' || e.error) g.errors++;
  if (e.body === 'claude_code.tool_result') g.tools++;
  if (tsToMs(e.timestamp) > tsToMs(g.latestTs)) g.latestTs = e.timestamp;
  g.events.push(e);
}

async function cc_loadPromptTraces() {
  var container = document.getElementById('cc-prompt-traces');
  if (!container) return;
  container.innerHTML = '<div class="loading">' + t('empty.loading_prompt_traces') + '</div>';

  try {
    var iv = intervalSQL();
    var promptFilter = (document.getElementById('cc-prompt-trace-filter')?.value || '').trim();
    var res = await query(
      "SELECT timestamp, body, log_attributes " +
      "FROM opentelemetry_logs " +
      "WHERE body IN ('claude_code.user_prompt', 'claude_code.api_request', 'claude_code.api_error', 'claude_code.tool_result', 'claude_code.tool_decision') " +
      "  AND timestamp > NOW() - INTERVAL '" + iv + "' " +
      "ORDER BY timestamp DESC LIMIT " + sessionQueryLimit()
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      container.innerHTML = '<div class="chart-empty">' + t('empty.no_recent_cc_events') + '</div>';
      return;
    }

    var events = [];
    var hasPromptId = false;
    var hasSessionId = false;
    data.forEach(function(d) {
      var attrs = cc_parseAttrs(d.log_attributes);
      var promptId = cc_attr(attrs, 'prompt.id');
      var sessionId = cc_attr(attrs, 'session.id');
      var seqRaw = cc_attr(attrs, 'event.sequence');
      var seq = seqRaw == null ? null : Number(seqRaw);
      if (seq != null && Number.isNaN(seq)) seq = null;
      if (promptId) hasPromptId = true;
      if (sessionId) hasSessionId = true;
      events.push({
        timestamp: d.timestamp,
        body: d.body,
        seq: seq,
        promptId: promptId ? String(promptId) : null,
        sessionId: sessionId ? String(sessionId) : null,
        model: cc_attr(attrs, 'model'),
        tool: cc_attr(attrs, 'tool_name'),
        cost: Number(cc_attr(attrs, 'cost_usd')) || 0,
        duration: cc_attr(attrs, 'duration_ms'),
        error: cc_attr(attrs, 'error'),
      });
    });

    var groups = [];
    if (hasPromptId) {
      var byPrompt = {};
      events.forEach(function(e) {
        if (!e.promptId) return;
        if (!byPrompt[e.promptId]) byPrompt[e.promptId] = cc_newTraceGroup(e.promptId, e.sessionId, e.timestamp);
        cc_addEventToTraceGroup(byPrompt[e.promptId], e);
      });
      groups = Object.values(byPrompt);
    } else if (hasSessionId) {
      var bySession = {};
      events.forEach(function(e) {
        if (!e.sessionId) return;
        if (!bySession[e.sessionId]) bySession[e.sessionId] = [];
        bySession[e.sessionId].push(e);
      });
      Object.keys(bySession).forEach(function(sessionId) {
        var arr = bySession[sessionId].sort(cc_sortPromptEvents);
        var turn = 0;
        var current = null;
        arr.forEach(function(e) {
          if (!current || e.body === 'claude_code.user_prompt') {
            turn++;
            var traceLabel = sessionId + ' #' + turn;
            current = cc_newTraceGroup(traceLabel, sessionId, e.timestamp);
            groups.push(current);
          }
          cc_addEventToTraceGroup(current, e);
        });
      });
    }

    groups = groups.filter(function(g) {
      if (!promptFilter) return true;
      var sid = g.sessionId || '';
      return g.promptId.indexOf(promptFilter) >= 0 || sid.indexOf(promptFilter) >= 0;
    });
    groups.sort(function(a, b) { return tsToMs(b.latestTs) - tsToMs(a.latestTs); });

    if (!groups.length) {
      var hint;
      if (!hasPromptId && !hasSessionId) hint = t('empty.no_prompt_session_id');
      else if (hasPromptId) hint = t('empty.no_prompt_match');
      else hint = t('empty.no_session_match');
      container.innerHTML = '<div class="chart-empty">' + escapeHTML(hint) + '</div>';
      return;
    }

    if (!hasPromptId) {
      container.innerHTML = '<div class="loading">' + t('empty.no_prompt_id') + '</div>';
    } else {
      container.innerHTML = '';
    }

    var maxShow = 30;
    var shown = groups.slice(0, maxShow);
    container.innerHTML += shown.map(function(g) {
      var events = g.events.sort(cc_sortPromptEvents);
      var timeline = events.map(function(e) {
        var evt = (e.body || '').replace('claude_code.', '');
        var parts = [];
        if (e.seq != null) parts.push('#' + e.seq);
        parts.push(evt);
        if (e.model) parts.push('model=' + e.model);
        if (e.tool) parts.push('tool=' + e.tool);
        if (e.cost) parts.push('cost=' + fmtCost(e.cost));
        if (e.duration != null) parts.push('dur=' + fmtDurMs(Number(e.duration)));
        if (e.error) parts.push('error=' + e.error);
        return '<div style="font-size:12px;color:var(--text-muted);padding:3px 0">' +
          '<span style="color:var(--text-secondary)">' + escapeHTML(fmtTime(e.timestamp)) + '</span> · ' +
          escapeHTML(parts.join(' · ')) + '</div>';
      }).join('');
      var models = Object.keys(g.models);
      return '<div class="anomaly-item" style="cursor:pointer" onclick="cc_togglePromptTraceDetail(this)">' +
        '<div class="anomaly-reason">' + escapeHTML(g.promptId) + '</div>' +
        '<div style="font-size:13px">' +
        fmtTime(g.latestTs) + ' · ' +
        fmtNum(g.events.length) + ' ' + t('ui.events') + ' · ' +
        fmtNum(g.reqs) + ' ' + t('ui.requests') + ' · ' +
        fmtCost(g.cost) + ' · ' +
        g.errors + ' ' + t('ui.errors') +
        (models.length ? ' · ' + escapeHTML(models.slice(0, 3).join(', ')) : '') +
        '</div>' +
        '<div class="cc-prompt-detail" style="display:none;margin-top:8px">' + timeline + '</div>' +
        '</div>';
    }).join('');
    if (groups.length > maxShow) {
      container.innerHTML += '<div class="loading">' + t('empty.showing_latest_n').replace('{n}', maxShow) + '</div>';
    }
  } catch (err) {
    container.innerHTML = '<div class="chart-empty">' + t('error.load_prompt_traces') + ' ' + escapeHTML(err.message) + '</div>';
  }
}

// ===================================================================
// Claude Code view — Cost tab
// ===================================================================
async function cc_loadCostTab() {
  await Promise.all([
    cc_loadBurnRate(),
    cc_loadCostByModel(),
    cc_loadExpensiveRequests(),
    cc_loadCacheEfficiency(),
    cc_loadCCModelComparison(),
  ]);
}

async function cc_loadBurnRate() {
  try {
    var res = await query(
      "SELECT ROUND(SUM(json_get_float(log_attributes, 'cost_usd')), 4) AS cost_1h " +
      "FROM opentelemetry_logs " +
      "WHERE body = 'claude_code.api_request' " +
      "  AND timestamp > NOW() - INTERVAL '1 hour'"
    );
    var cost1h = Number(rows(res)?.[0]?.[0]) || 0;
    var container = document.getElementById('cc-burn-rate-cards');
    if (cost1h > 0) {
      container.style.display = '';
      document.getElementById('cc-burn-hour').textContent = '$' + cost1h.toFixed(4) + t('ui.per_hour');
      document.getElementById('cc-burn-day').textContent = '$' + (cost1h * 24).toFixed(2);
      document.getElementById('cc-burn-week').textContent = '$' + (cost1h * 24 * 7).toFixed(2);
    } else {
      container.style.display = 'none';
    }
  } catch { /* ignore */ }
}

async function cc_loadCostByModel() {
  try {
    var tc = getThemeColors();
    var res = await query(
      "SELECT json_get_string(log_attributes, 'model') AS model, " +
      "SUM(json_get_float(log_attributes, 'cost_usd')) AS total_cost " +
      "FROM opentelemetry_logs " +
      "WHERE body = 'claude_code.api_request' " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY model ORDER BY total_cost DESC"
    );
    var data = rowsToObjects(res);
    var el = document.getElementById('cc-cost-by-model');
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_cost_data') + '</div>'; return; }
    var maxCost = Math.max.apply(null, data.map(function(d) { return Number(d.total_cost) || 0; }));
    el.innerHTML = data.map(function(d) {
      var pct = maxCost > 0 ? (Number(d.total_cost) / maxCost * 100) : 0;
      return '<div class="bar-row">' +
        '<div class="bar-label">' + escapeHTML(d.model || 'unknown') + '</div>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + pct + '%;background:' + tc.orange + '"></div></div>' +
        '<div class="bar-value">' + fmtCost(d.total_cost) + '</div></div>';
    }).join('');
  } catch { /* ignore */ }
}

async function cc_loadExpensiveRequests() {
  try {
    var res = await query(
      "SELECT timestamp, " +
      "json_get_string(log_attributes, 'model') AS model, " +
      "json_get_int(log_attributes, 'input_tokens') AS input_tok, " +
      "json_get_int(log_attributes, 'output_tokens') AS output_tok, " +
      "json_get_float(log_attributes, 'cost_usd') AS cost_usd, " +
      "json_get_float(log_attributes, 'duration_ms') AS duration_ms, " +
      "log_attributes " +
      "FROM opentelemetry_logs " +
      "WHERE body = 'claude_code.api_request' " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "ORDER BY json_get_float(log_attributes, 'cost_usd') DESC LIMIT 10"
    );
    var data = rowsToObjects(res);
    var tbody = document.getElementById('cc-expensive-body');
    if (!data.length) { tbody.innerHTML = '<tr><td colspan="6" class="loading">' + t('empty.no_data') + '</td></tr>'; return; }
    tbody.innerHTML = data.map(function(d) {
      var attrs = cc_parseAttrs(d.log_attributes);
      var sid = cc_attr(attrs, 'session.id') || '';
      var tsMs = tsToMs(d.timestamp) || 0;
      var seq = cc_attr(attrs, 'event.sequence');
      var apiKey = seq != null ? 'seq:' + seq : String(tsMs);
      var onclick = sid ? ' class="clickable" onclick="sess_openDetail(\x27' + escapeJSString(sid) + '\x27,\x27claude_code\x27,' + tsMs + ',\x27' + escapeJSString(apiKey) + '\x27)"' : '';
      return '<tr' + onclick + '><td>' + fmtTime(d.timestamp) + '</td>' +
      '<td>' + escapeHTML(d.model || 'unknown') + '</td>' +
      '<td>' + fmtNum(d.input_tok) + '</td>' +
      '<td>' + fmtNum(d.output_tok) + '</td>' +
      '<td>' + fmtCost(d.cost_usd) + '</td>' +
      '<td>' + fmtDurMs(d.duration_ms) + '</td></tr>';
    }).join('');
  } catch { /* ignore */ }
}

async function cc_loadCacheEfficiency() {
  try {
    var tc = getThemeColors();
    var res = await query(
      "SELECT json_get_string(log_attributes, 'model') AS model, " +
      "SUM(json_get_int(log_attributes, 'cache_read_tokens')) AS cache_read, " +
      "SUM(json_get_int(log_attributes, 'cache_creation_tokens')) AS cache_create " +
      "FROM opentelemetry_logs " +
      "WHERE body = 'claude_code.api_request' " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY model"
    );
    var data = rowsToObjects(res);
    var el = document.getElementById('cc-cache-chart');
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_cache_data') + '</div>'; return; }
    el.innerHTML = data.map(function(d) {
      var cacheRead = Number(d.cache_read) || 0;
      var cacheCreate = Number(d.cache_create) || 0;
      var total = cacheRead + cacheCreate;
      var hitRate = total > 0 ? (cacheRead / total * 100).toFixed(1) : '0.0';
      return '<div class="bar-row">' +
        '<div class="bar-label">' + escapeHTML(d.model || 'unknown') + '</div>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + hitRate + '%;background:' + tc.green + '"></div></div>' +
        '<div class="bar-value">' + hitRate + t('ui.cache_hit_bar') + ' (' + fmtNum(cacheRead) + '/' + fmtNum(total) + ')</div></div>';
    }).join('');
  } catch { /* ignore */ }
}

async function cc_loadCCModelComparison() {
  try {
    var res = await query(
      "SELECT json_get_string(log_attributes, 'model') AS model, " +
      "COUNT(*) AS reqs, " +
      "ROUND(AVG(json_get_float(log_attributes, 'duration_ms')), 0) AS avg_lat, " +
      "ROUND(AVG(json_get_float(log_attributes, 'cost_usd')), 6) AS avg_cost " +
      "FROM opentelemetry_logs " +
      "WHERE body = 'claude_code.api_request' " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY model ORDER BY reqs DESC"
    );
    var data = rowsToObjects(res);

    var errMap = {};
    try {
      var errRes = await query(
        "SELECT json_get_string(log_attributes, 'model') AS model, COUNT(*) AS errs " +
        "FROM opentelemetry_logs " +
        "WHERE body = 'claude_code.api_error' " +
        "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
        "GROUP BY model"
      );
      rowsToObjects(errRes).forEach(function(d) { errMap[d.model] = Number(d.errs) || 0; });
    } catch { /* no errors */ }

    var tbody = document.getElementById('cc-model-compare-body');
    if (!data.length) { tbody.innerHTML = '<tr><td colspan="5" class="loading">' + t('empty.no_data') + '</td></tr>'; return; }
    tbody.innerHTML = data.map(function(d) {
      var reqs = Number(d.reqs) || 0;
      var errs = errMap[d.model] || 0;
      var errRate = reqs > 0 ? ((errs / reqs) * 100).toFixed(1) + '%' : '0%';
      return '<tr>' +
        '<td>' + escapeHTML(d.model || 'unknown') + '</td>' +
        '<td>' + fmtNum(reqs) + '</td>' +
        '<td>' + fmtDurMs(d.avg_lat) + '</td>' +
        '<td>' + fmtCost(d.avg_cost) + '</td>' +
        '<td>' + errRate + '</td></tr>';
    }).join('');
  } catch { /* ignore */ }
}

// ===================================================================
// Claude Code view — Search tab
// ===================================================================
async function cc_loadAnomalies() {
  var el = document.getElementById('cc-anomaly-list');
  el.innerHTML = '<div class="loading">' + t('empty.loading_anomalies') + '</div>';
  try {
    var iv = intervalSQL();
    var avgRes = await query(
      "SELECT AVG(json_get_float(log_attributes, 'cost_usd')) AS avg_cost " +
      "FROM opentelemetry_logs WHERE body = 'claude_code.api_request' " +
      "AND timestamp > NOW() - INTERVAL '" + iv + "'"
    );
    var avgCost = Number(rows(avgRes)?.[0]?.[0]) || 0;
    var threshold = Math.max(avgCost * 3, 0.01);

    var res = await query(
      "SELECT timestamp, " +
      "json_get_string(log_attributes, 'model') AS model, " +
      "json_get_float(log_attributes, 'cost_usd') AS cost_usd, " +
      "json_get_int(log_attributes, 'input_tokens') AS input_tok, " +
      "json_get_int(log_attributes, 'output_tokens') AS output_tok, " +
      "json_get_float(log_attributes, 'duration_ms') AS duration_ms, " +
      "json_get_string(log_attributes, 'error') AS error, " +
      "body, log_attributes " +
      "FROM opentelemetry_logs " +
      "WHERE ((body = 'claude_code.api_request' AND json_get_float(log_attributes, 'cost_usd') > " + threshold + ") " +
      "  OR body = 'claude_code.api_error') " +
      "  AND timestamp > NOW() - INTERVAL '" + iv + "' " +
      "ORDER BY timestamp DESC LIMIT 20"
    );
    var data = rowsToObjects(res);

    var slowToolItems = [];
    try {
      var toolAvgRes = await query(
        "SELECT json_get_string(log_attributes, 'tool_name') AS tool, " +
        "AVG(json_get_float(log_attributes, 'duration_ms')) AS avg_ms " +
        "FROM opentelemetry_logs " +
        "WHERE body = 'claude_code.tool_result' " +
        "  AND timestamp > NOW() - INTERVAL '" + iv + "' " +
        "GROUP BY tool"
      );
      var toolAvgs = {};
      rowsToObjects(toolAvgRes).forEach(function(r) { toolAvgs[r.tool] = Number(r.avg_ms) || 0; });

      var slowRes = await query(
        "SELECT timestamp, " +
        "json_get_string(log_attributes, 'tool_name') AS tool_name, " +
        "json_get_float(log_attributes, 'duration_ms') AS duration_ms, " +
        "log_attributes " +
        "FROM opentelemetry_logs " +
        "WHERE body = 'claude_code.tool_result' " +
        "  AND timestamp > NOW() - INTERVAL '" + iv + "' " +
        "ORDER BY json_get_float(log_attributes, 'duration_ms') DESC LIMIT 100"
      );
      rowsToObjects(slowRes).forEach(function(d) {
        var toolAvg = toolAvgs[d.tool_name] || 0;
        var dur = Number(d.duration_ms) || 0;
        if (toolAvg > 0 && dur > toolAvg * 3 && dur > 1000) {
          slowToolItems.push(d);
        }
      });
      slowToolItems = slowToolItems.slice(0, 10);
    } catch { /* ignore slow tool detection errors */ }

    if (!data.length && !slowToolItems.length) {
      el.innerHTML = '<div class="loading">' + t('empty.no_anomalies') + '</div>';
      return;
    }
    var html = data.map(function(d) {
      var reason = d.body === 'claude_code.api_error' ? t('anomaly.api_error') : t('anomaly.high_cost') + ' ($' + Number(d.cost_usd).toFixed(4) + ' > 3x avg $' + avgCost.toFixed(4) + ')';
      var severity = d.body === 'claude_code.api_error' ? '' : 'warn';
      var attrs = cc_parseAttrs(d.log_attributes);
      var sid = cc_attr(attrs, 'session.id') || '';
      var tsMs = tsToMs(d.timestamp) || 0;
      // High-cost api_request: pass event.sequence for precise API call matching.
      // api_error: no sequence, only timeline positioning.
      var seq = d.body === 'claude_code.api_request' ? cc_attr(attrs, 'event.sequence') : null;
      var apiKey = seq != null ? 'seq:' + seq : '';
      var fpArg = apiKey ? ',\x27' + escapeJSString(apiKey) + '\x27' : '';
      var onclick = sid ? ' onclick="sess_openDetail(\x27' + escapeJSString(sid) + '\x27,\x27claude_code\x27,' + tsMs + fpArg + ')"' : '';
      return '<div class="anomaly-item ' + severity + ' clickable"' + onclick + '>' +
        '<div class="anomaly-reason">' + reason + '</div>' +
        '<div style="font-size:13px">' +
        escapeHTML(d.model || 'unknown') + ' &middot; ' +
        fmtNum(d.input_tok) + ' in / ' + fmtNum(d.output_tok) + ' out &middot; ' +
        (d.duration_ms != null ? fmtDurMs(d.duration_ms) : '') +
        (d.error ? ' &middot; ' + escapeHTML(d.error) : '') +
        ' &middot; ' + fmtTime(d.timestamp) +
        '</div></div>';
    }).join('');

    slowToolItems.forEach(function(d) {
      var attrs = cc_parseAttrs(d.log_attributes);
      var sid = cc_attr(attrs, 'session.id') || '';
      var tsMs = tsToMs(d.timestamp) || 0;
      var onclick = sid ? ' onclick="sess_openDetail(\x27' + escapeJSString(sid) + '\x27,\x27claude_code\x27,' + tsMs + ')"' : '';
      html += '<div class="anomaly-item warn clickable"' + onclick + '>' +
        '<div class="anomaly-reason">' + t('anomaly.slow_tool') + '</div>' +
        '<div style="font-size:13px">' +
        escapeHTML(d.tool_name || 'unknown') + ' &middot; ' +
        fmtDurMs(d.duration_ms) + ' &middot; ' +
        fmtTime(d.timestamp) +
        '</div></div>';
    });
    el.innerHTML = html;
  } catch {
    el.innerHTML = '<div class="loading">' + t('error.load_anomalies') + '</div>';
  }
}

// ===================================================================
// Claude Code view — Tools tab
// ===================================================================
async function cc_loadToolsTab() {
  await Promise.all([
    cc_loadToolsTable(),
    cc_loadToolTrends(),
    cc_loadToolFailures(),
  ]);
}

async function cc_loadToolsTable() {
  var tbody = document.getElementById('cc-tools-perf-body');
  if (!tbody) return;
  try {
    var res = await query(
      "SELECT json_get_string(log_attributes, 'tool_name') AS tool, " +
      "COUNT(*) AS calls, " +
      "SUM(CASE WHEN json_get_string(log_attributes, 'success') = 'true' THEN 1 ELSE 0 END) AS ok, " +
      "ROUND(AVG(json_get_float(log_attributes, 'duration_ms')), 0) AS avg_ms, " +
      "ROUND(APPROX_PERCENTILE_CONT(json_get_float(log_attributes, 'duration_ms'), 0.95), 0) AS p95_ms " +
      "FROM opentelemetry_logs " +
      "WHERE body = 'claude_code.tool_result' " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY tool ORDER BY calls DESC LIMIT 20"
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      tbody.innerHTML = '<tr><td colspan="6" class="loading">' + t('empty.no_tool_data') + '</td></tr>';
      return;
    }
    var tc = getThemeColors();
    tbody.innerHTML = data.map(function(d) {
      var calls = Number(d.calls) || 0;
      var ok = Number(d.ok) || 0;
      var rate = calls > 0 ? ((ok / calls) * 100).toFixed(1) : '0.0';
      var rateColor = Number(rate) >= 95 ? tc.green : Number(rate) >= 80 ? tc.yellow : tc.red;
      var cat = ccToolCategory(d.tool);
      var icon = CC_CATEGORY_ICONS[cat] || '';
      return '<tr>' +
        '<td>' + escapeHTML(d.tool || 'unknown') + '</td>' +
        '<td>' + icon + ' ' + escapeHTML(cat) + '</td>' +
        '<td>' + fmtNum(calls) + '</td>' +
        '<td><span style="color:' + rateColor + '">' + rate + '%</span></td>' +
        '<td>' + fmtDurMs(d.avg_ms) + '</td>' +
        '<td>' + fmtDurMs(d.p95_ms) + '</td></tr>';
    }).join('');
  } catch {
    tbody.innerHTML = '<tr><td colspan="6" class="loading">' + t('error.load_tool_perf') + '</td></tr>';
  }
}

async function cc_loadToolTrends() {
  try {
    var bucket = currentTimeRange === '1h' ? '5 minutes' : (currentTimeRange === '7d' || currentTimeRange === '30d' ? '1 hour' : '15 minutes');
    var res = await query(
      "SELECT date_bin('" + bucket + "'::INTERVAL, timestamp) AS t, " +
      "json_get_string(log_attributes, 'tool_name') AS tool, " +
      "COUNT(*) AS cnt " +
      "FROM opentelemetry_logs " +
      "WHERE body = 'claude_code.tool_result' " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY t, tool ORDER BY t"
    );
    var raw = rowsToObjects(res);
    if (!raw.length) return;

    // Get top 6 tools by total count
    var toolTotals = {};
    raw.forEach(function(r) {
      toolTotals[r.tool] = (toolTotals[r.tool] || 0) + (Number(r.cnt) || 0);
    });
    var topTools = Object.keys(toolTotals).sort(function(a, b) { return toolTotals[b] - toolTotals[a]; }).slice(0, 6);

    // Pivot: time -> { t, tool1: cnt, tool2: cnt, ... }
    var timeMap = {};
    raw.forEach(function(r) {
      if (topTools.indexOf(r.tool) < 0) return;
      if (!timeMap[r.t]) timeMap[r.t] = { t: r.t };
      timeMap[r.t][r.tool] = Number(r.cnt) || 0;
    });
    var data = Object.values(timeMap).sort(function(a, b) { return String(a.t) < String(b.t) ? -1 : 1; });

    var seriesDefs = topTools.map(function(tool, i) {
      return { label: tool, key: tool, color: chartColors[i % chartColors.length] };
    });
    renderChart('cc-tool-trends-chart', data, seriesDefs, function(v) { return fmtNum(v); });
  } catch { /* no data */ }
}

async function cc_loadToolFailures() {
  var tbody = document.getElementById('cc-tools-failures-body');
  if (!tbody) return;
  try {
    var res = await query(
      "SELECT timestamp, " +
      "json_get_string(log_attributes, 'tool_name') AS tool, " +
      "json_get_string(log_attributes, 'error') AS error, " +
      "json_get_float(log_attributes, 'duration_ms') AS duration_ms, " +
      "log_attributes " +
      "FROM opentelemetry_logs " +
      "WHERE body = 'claude_code.tool_result' " +
      "  AND json_get_string(log_attributes, 'success') = 'false' " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "ORDER BY timestamp DESC LIMIT 20"
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      tbody.innerHTML = '<tr><td colspan="4" class="loading">' + t('empty.no_recent_failures') + '</td></tr>';
      return;
    }
    tbody.innerHTML = data.map(function(d, i) {
      var errText = d.error || t('sessions.error_unknown');
      if (errText.length > 80) errText = errText.substring(0, 80) + '\u2026';
      var rowId = 'cc-fail-' + i;
      var attrJson = '';
      try { var la = typeof d.log_attributes === 'string' ? JSON.parse(d.log_attributes) : (d.log_attributes || {}); attrJson = JSON.stringify(deepParseAttrs(la), null, 2); } catch { attrJson = String(d.log_attributes || ''); }
      return '<tr class="clickable" onclick="var d=document.getElementById(\x27' + rowId + '\x27);var v=d.style.display===\x27none\x27;d.style.display=v?\x27table-row\x27:\x27none\x27;this.querySelector(\x27.expand-arrow\x27).textContent=v?\x27\\u25BC\x27:\x27\\u25B6\x27">' +
        '<td><span class="expand-arrow" style="color:var(--text-muted);font-size:10px;margin-right:4px;display:inline-block;width:10px">&#9654;</span>' + fmtTime(d.timestamp) + '</td>' +
        '<td>' + escapeHTML(d.tool || 'unknown') + '</td>' +
        '<td style="max-width:300px;overflow:hidden;text-overflow:ellipsis" title="' + escapeHTML(d.error || '') + '">' + escapeHTML(errText) + '</td>' +
        '<td>' + fmtDurMs(d.duration_ms) + '</td></tr>' +
        '<tr id="' + rowId + '" style="display:none"><td colspan="4"><pre style="margin:0;padding:8px 12px;font-size:12px;background:var(--bg-secondary);border-radius:4px;white-space:pre-wrap;word-break:break-all">' + escapeHTML(attrJson) + '</pre></td></tr>';
    }).join('');
  } catch {
    tbody.innerHTML = '<tr><td colspan="4" class="loading">' + t('error.load_failures') + '</td></tr>';
  }
}
