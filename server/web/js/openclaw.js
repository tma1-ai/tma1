// openclaw.js — OpenClaw view: all oc_* functions
// Depends on: core.js, chart.js, i18n.js, metrics-explorer.js, traces.js (renderWaterfall)

// OpenClaw span filters
var OC_MODEL_SPAN = "span_name = 'openclaw.model.usage'";
var OC_MSG_SPAN = "span_name = 'openclaw.message.processed'";
var OC_ALL_SPANS = "span_name LIKE 'openclaw.%'";
var ocTracePage = 0;
var ocTracePageSize = 15;
var ocTraceHasNext = false;
var ocTraceColumnsPromise = null;

function oc_shortSpanName(name) {
  return (name || '').replace('openclaw.', '');
}

function oc_resetTracePaging() {
  ocTracePage = 0;
}

function oc_onTraceFilterChange() {
  oc_resetTracePaging();
  oc_loadTraces();
}

function oc_prevTracePage() {
  if (ocTracePage <= 0) return;
  ocTracePage--;
  oc_loadTraces();
}

function oc_nextTracePage() {
  if (!ocTraceHasNext) return;
  ocTracePage++;
  oc_loadTraces();
}

function oc_updateTracePager(resultCount) {
  var prevBtn = document.getElementById('oc-trace-prev-btn');
  var nextBtn = document.getElementById('oc-trace-next-btn');
  var info = document.getElementById('oc-trace-page-info');
  if (!prevBtn || !nextBtn || !info) return;

  prevBtn.disabled = ocTracePage <= 0;
  nextBtn.disabled = !ocTraceHasNext;
  if (!resultCount) {
    info.textContent = 'No results';
    return;
  }
  var start = ocTracePage * ocTracePageSize + 1;
  var end = start + resultCount - 1;
  info.textContent = 'Page ' + (ocTracePage + 1) + ' · ' + start + '-' + end;
}

async function oc_getTraceColumns() {
  if (!ocTraceColumnsPromise) {
    ocTraceColumnsPromise = query(
      "SELECT column_name FROM information_schema.columns " +
      "WHERE table_schema = 'public' AND table_name = 'opentelemetry_traces'"
    ).then(function(res) {
      var cols = {};
      rowsToObjects(res).forEach(function(r) { cols[r.column_name] = true; });
      return cols;
    }).catch(function() { return {}; });
  }
  return ocTraceColumnsPromise;
}

function oc_traceAttrSelect(columns, columnName, alias) {
  if (columns && columns[columnName]) return '"' + columnName + '" AS ' + alias;
  return 'NULL AS ' + alias;
}

// ===================================================================
// OpenClaw view — Cards
// ===================================================================
async function oc_loadCards() {
  var iv = intervalSQL();
  try {
    await loadPricing();
    var costExpr = costCaseSQL(
      '"span_attributes.openclaw.model"',
      '"span_attributes.openclaw.tokens.input"',
      '"span_attributes.openclaw.tokens.output"'
    );
    var results = await Promise.all([
      query(
        "SELECT ROUND(SUM(" + costExpr + "), 4) AS v " +
        "FROM opentelemetry_traces " +
        "WHERE " + OC_MODEL_SPAN + " AND timestamp > NOW() - INTERVAL '" + iv + "'"
      ),
      query(
        "SELECT SUM(CAST(\"span_attributes.openclaw.tokens.input\" AS DOUBLE) + " +
        "CAST(\"span_attributes.openclaw.tokens.output\" AS DOUBLE)) AS v " +
        "FROM opentelemetry_traces " +
        "WHERE " + OC_MODEL_SPAN + " AND timestamp > NOW() - INTERVAL '" + iv + "'"
      ),
      query(
        "SELECT COUNT(*) AS v FROM opentelemetry_traces " +
        "WHERE " + OC_MODEL_SPAN + " AND timestamp > NOW() - INTERVAL '" + iv + "'"
      ),
      query(
        "SELECT ROUND(AVG(duration_nano) / 1000000.0, 0) AS v FROM opentelemetry_traces " +
        "WHERE " + OC_MODEL_SPAN + " AND timestamp > NOW() - INTERVAL '" + iv + "'"
      ),
    ]);
    document.getElementById('oc-val-cost').textContent = fmtCost(rows(results[0])?.[0]?.[0]);
    document.getElementById('oc-val-tokens').textContent = fmtNum(rows(results[1])?.[0]?.[0]);
    document.getElementById('oc-val-requests').textContent = fmtNum(rows(results[2])?.[0]?.[0]);
    var latVal = rows(results[3])?.[0]?.[0];
    document.getElementById('oc-val-latency').textContent = fmtDurMs(latVal);
  } catch (err) {
    var banner = document.getElementById('error-banner');
    banner.style.display = 'block';
    banner.textContent = 'OpenClaw metrics error: ' + err.message;
  }
}

// ===================================================================
// OpenClaw view — Overview tab
// ===================================================================
async function oc_loadOverview() {
  await Promise.all([
    oc_loadTokenChart(),
    oc_loadCostChart(),
    oc_loadLatencyChart(),
    oc_loadChannelDistribution(),
    oc_loadCacheEfficiencyOverview(),
    oc_loadProviderDistribution(),
    oc_loadMessageFlowChart(),
    oc_loadContextWindowChart(),
    oc_loadOutcomeDistribution(),
    oc_loadSessionStateChart(),
    oc_loadQueueDepthChart(),
    oc_loadRunDurationChart(),
    oc_loadActivityHeatmap(),
    oc_loadMetricsExplorer(),
  ]);
}

async function oc_loadActivityHeatmap() {
  var el = document.getElementById('oc-activity-heatmap');
  if (!el) return;
  var cfg = heatmapConfig();
  try {
    var res = await query(
      "SELECT date_bin('" + cfg.bucket + "'::INTERVAL, timestamp) AS t, COUNT(*) AS cnt " +
      "FROM opentelemetry_traces " +
      "WHERE span_name LIKE 'openclaw.%' " +
      "  AND timestamp > NOW() - INTERVAL '" + cfg.interval + "' " +
      "GROUP BY t ORDER BY t"
    );
    renderHeatmap('oc-activity-heatmap', rowsToObjects(res));
  } catch {
    el.innerHTML = '<div class="chart-empty">' + t('error.load_activity') + '</div>';
  }
}

async function oc_loadTokenChart() {
  try {
    var res = await query(
      "SELECT date_bin('5 minutes'::INTERVAL, timestamp) AS t, " +
      "SUM(CAST(\"span_attributes.openclaw.tokens.input\" AS DOUBLE)) AS inp, " +
      "SUM(CAST(\"span_attributes.openclaw.tokens.output\" AS DOUBLE)) AS outp, " +
      "SUM(CAST(\"span_attributes.openclaw.tokens.cache_write\" AS DOUBLE)) AS cw " +
      "FROM opentelemetry_traces " +
      "WHERE " + OC_MODEL_SPAN + " AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;
    renderChart('oc-chart-tokens', data, [
      { label: t('chart.input_tokens'), key: 'inp', color: '#79c0ff' },
      { label: t('chart.output_tokens'), key: 'outp', color: '#f0883e' },
      { label: 'Cache Write', key: 'cw', color: '#3fb950' },
    ], function(v) { return fmtNum(v); });
  } catch { /* no data */ }
}

async function oc_loadCostChart() {
  try {
    await loadPricing();
    var costExpr = costCaseSQL(
      '"span_attributes.openclaw.model"',
      '"span_attributes.openclaw.tokens.input"',
      '"span_attributes.openclaw.tokens.output"'
    );
    var res = await query(
      "SELECT date_bin('5 minutes'::INTERVAL, timestamp) AS t, " +
      "SUM(" + costExpr + ") AS cost " +
      "FROM opentelemetry_traces " +
      "WHERE " + OC_MODEL_SPAN + " AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;
    renderChart('oc-chart-cost', data, [
      { label: t('chart.cost_usd'), key: 'cost', color: '#f0883e' },
    ], function(v) { return '$' + Number(v).toFixed(4); });
  } catch { /* no data */ }
}

async function oc_loadLatencyChart() {
  try {
    var res = await query(
      "SELECT date_bin('5 minutes'::INTERVAL, timestamp) AS t, " +
      "ROUND(APPROX_PERCENTILE_CONT(duration_nano, 0.50) / 1000000.0, 0) AS p50_ms, " +
      "ROUND(APPROX_PERCENTILE_CONT(duration_nano, 0.95) / 1000000.0, 0) AS p95_ms " +
      "FROM opentelemetry_traces " +
      "WHERE " + OC_MODEL_SPAN + " AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;
    renderChart('oc-chart-latency', data, [
      { label: t('chart.p50'), key: 'p50_ms', color: '#3fb950' },
      { label: t('chart.p95'), key: 'p95_ms', color: '#d2a8ff' },
    ], function(v) { return fmtDurMs(v); });
  } catch { /* no data */ }
}

async function oc_loadChannelDistribution() {
  try {
    var tc = getThemeColors();
    var res = await query(
      "SELECT \"span_attributes.openclaw.channel\" AS channel, COUNT(*) AS cnt " +
      "FROM opentelemetry_traces " +
      "WHERE " + OC_MSG_SPAN + " AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY channel ORDER BY cnt DESC LIMIT 10"
    );
    var data = rowsToObjects(res);
    var el = document.getElementById('oc-channel-dist');
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_data') + '</div>'; return; }
    var maxCnt = Math.max.apply(null, data.map(function(d) { return Number(d.cnt) || 0; }));
    el.innerHTML = data.map(function(d) {
      var pct = maxCnt > 0 ? (Number(d.cnt) / maxCnt * 100) : 0;
      return '<div class="bar-row">' +
        '<div class="bar-label">' + escapeHTML(d.channel || 'unknown') + '</div>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + pct + '%;background:' + tc.blue + '"></div></div>' +
        '<div class="bar-value">' + fmtNum(d.cnt) + '</div></div>';
    }).join('');
  } catch { /* no data */ }
}

async function oc_loadCacheEfficiencyOverview() {
  try {
    var tc = getThemeColors();
    var res = await query(
      "SELECT \"span_attributes.openclaw.model\" AS model, " +
      "SUM(CAST(\"span_attributes.openclaw.tokens.cache_read\" AS DOUBLE)) AS cache_read, " +
      "SUM(CAST(\"span_attributes.openclaw.tokens.cache_write\" AS DOUBLE)) AS cache_write " +
      "FROM opentelemetry_traces " +
      "WHERE " + OC_MODEL_SPAN + " AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY model"
    );
    var data = rowsToObjects(res);
    var el = document.getElementById('oc-cache-efficiency');
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_cache_data') + '</div>'; return; }
    el.innerHTML = data.map(function(d) {
      var cr = Number(d.cache_read) || 0;
      var cw = Number(d.cache_write) || 0;
      var total = cr + cw;
      var hitRate = total > 0 ? (cr / total * 100).toFixed(1) : '0.0';
      return '<div class="bar-row">' +
        '<div class="bar-label">' + escapeHTML(d.model || 'unknown') + '</div>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + hitRate + '%;background:' + tc.green + '"></div></div>' +
        '<div class="bar-value">' + hitRate + '% hit (' + fmtNum(cr) + '/' + fmtNum(total) + ')</div></div>';
    }).join('');
  } catch { /* no data */ }
}

async function oc_loadProviderDistribution() {
  try {
    var tc = getThemeColors();
    var res = await query(
      "SELECT \"span_attributes.openclaw.provider\" AS provider, COUNT(*) AS cnt " +
      "FROM opentelemetry_traces " +
      "WHERE " + OC_MODEL_SPAN + " AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY provider ORDER BY cnt DESC LIMIT 10"
    );
    var data = rowsToObjects(res);
    var el = document.getElementById('oc-provider-dist');
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_data') + '</div>'; return; }
    var maxCnt = Math.max.apply(null, data.map(function(d) { return Number(d.cnt) || 0; }));
    var colors = [tc.blue, tc.orange, tc.green, tc.purple, tc.red, tc.yellow];
    el.innerHTML = data.map(function(d, i) {
      var pct = maxCnt > 0 ? (Number(d.cnt) / maxCnt * 100) : 0;
      return '<div class="bar-row">' +
        '<div class="bar-label">' + escapeHTML(d.provider || 'unknown') + '</div>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + pct + '%;background:' + colors[i % colors.length] + '"></div></div>' +
        '<div class="bar-value">' + fmtNum(d.cnt) + '</div></div>';
    }).join('');
  } catch { /* no data */ }
}

// ===================================================================
// OpenClaw view — Overview: Metrics-based charts (from openclaw_* tables)
// ===================================================================

async function oc_loadMessageFlowChart() {
  try {
    var iv = intervalSQL();
    var results = await Promise.all([
      query(
        "SELECT date_bin('5 minutes'::INTERVAL, ts) AS t, SUM(greptime_value) AS v " +
        "FROM openclaw_message_processed_total " +
        "WHERE ts > NOW() - INTERVAL '" + iv + "' GROUP BY t ORDER BY t"
      ),
      query(
        "SELECT date_bin('5 minutes'::INTERVAL, ts) AS t, SUM(greptime_value) AS v " +
        "FROM openclaw_message_queued_total " +
        "WHERE ts > NOW() - INTERVAL '" + iv + "' GROUP BY t ORDER BY t"
      ),
    ]);
    var processed = rowsToObjects(results[0]);
    var queued = rowsToObjects(results[1]);
    var timeMap = {};
    processed.forEach(function(d) { timeMap[d.t] = { t: d.t, processed: d.v, queued: 0 }; });
    queued.forEach(function(d) {
      if (timeMap[d.t]) timeMap[d.t].queued = d.v;
      else timeMap[d.t] = { t: d.t, processed: 0, queued: d.v };
    });
    var data = Object.values(timeMap).sort(function(a, b) { return String(a.t) < String(b.t) ? -1 : 1; });
    if (!data.length) return;
    renderChart('oc-chart-msg-flow', data, [
      { label: 'Processed', key: 'processed', color: '#3fb950' },
      { label: 'Queued', key: 'queued', color: '#79c0ff' },
    ], function(v) { return fmtNum(v); });
  } catch { /* table may not exist */ }
}

async function oc_loadContextWindowChart() {
  try {
    var iv = intervalSQL();
    var results = await Promise.all([
      query(
        "SELECT date_bin('5 minutes'::INTERVAL, ts) AS t, AVG(greptime_value) AS v " +
        "FROM openclaw_context_tokens_sum " +
        "WHERE openclaw_context = 'used' AND ts > NOW() - INTERVAL '" + iv + "' " +
        "GROUP BY t ORDER BY t"
      ),
      query(
        "SELECT date_bin('5 minutes'::INTERVAL, ts) AS t, AVG(greptime_value) AS v " +
        "FROM openclaw_context_tokens_sum " +
        "WHERE openclaw_context = 'limit' AND ts > NOW() - INTERVAL '" + iv + "' " +
        "GROUP BY t ORDER BY t"
      ),
    ]);
    var used = rowsToObjects(results[0]);
    var limit = rowsToObjects(results[1]);
    var timeMap = {};
    used.forEach(function(d) { timeMap[d.t] = { t: d.t, used: d.v, ctx_limit: 0 }; });
    limit.forEach(function(d) {
      if (timeMap[d.t]) timeMap[d.t].ctx_limit = d.v;
      else timeMap[d.t] = { t: d.t, used: 0, ctx_limit: d.v };
    });
    var data = Object.values(timeMap).sort(function(a, b) { return String(a.t) < String(b.t) ? -1 : 1; });
    if (!data.length) return;
    renderChart('oc-chart-context', data, [
      { label: 'Used', key: 'used', color: '#f0883e' },
      { label: 'Limit', key: 'ctx_limit', color: '#d2a8ff' },
    ], function(v) { return fmtNum(v); });
  } catch { /* table may not exist */ }
}

async function oc_loadOutcomeDistribution() {
  try {
    var tc = getThemeColors();
    var res = await query(
      "SELECT openclaw_outcome AS outcome, SUM(greptime_value) AS cnt " +
      "FROM openclaw_message_processed_total " +
      "WHERE ts > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY outcome ORDER BY cnt DESC"
    );
    var data = rowsToObjects(res);
    var el = document.getElementById('oc-outcome-dist');
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_data') + '</div>'; return; }
    var maxCnt = Math.max.apply(null, data.map(function(d) { return Number(d.cnt) || 0; }));
    var colors = { completed: tc.green, error: tc.red, timeout: tc.orange, cancelled: tc.yellow };
    el.innerHTML = data.map(function(d) {
      var pct = maxCnt > 0 ? (Number(d.cnt) / maxCnt * 100) : 0;
      var color = colors[d.outcome] || tc.blue;
      return '<div class="bar-row">' +
        '<div class="bar-label">' + escapeHTML(d.outcome || 'unknown') + '</div>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + pct + '%;background:' + color + '"></div></div>' +
        '<div class="bar-value">' + fmtNum(d.cnt) + '</div></div>';
    }).join('');
  } catch { /* table may not exist */ }
}

async function oc_loadSessionStateChart() {
  try {
    var tc = getThemeColors();
    var res = await query(
      "SELECT openclaw_state AS state, openclaw_reason AS reason, SUM(greptime_value) AS cnt " +
      "FROM openclaw_session_state_total " +
      "WHERE ts > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY state, reason ORDER BY cnt DESC LIMIT 10"
    );
    var data = rowsToObjects(res);
    var el = document.getElementById('oc-session-state');
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_data') + '</div>'; return; }
    var maxCnt = Math.max.apply(null, data.map(function(d) { return Number(d.cnt) || 0; }));
    var stateColors = { active: tc.green, idle: tc.blue, stuck: tc.red, closed: tc.purple };
    el.innerHTML = data.map(function(d) {
      var pct = maxCnt > 0 ? (Number(d.cnt) / maxCnt * 100) : 0;
      var label = (d.state || 'unknown') + (d.reason ? ' (' + d.reason + ')' : '');
      var color = stateColors[d.state] || tc.orange;
      return '<div class="bar-row">' +
        '<div class="bar-label">' + escapeHTML(label) + '</div>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + pct + '%;background:' + color + '"></div></div>' +
        '<div class="bar-value">' + fmtNum(d.cnt) + '</div></div>';
    }).join('');
  } catch { /* table may not exist */ }
}

async function oc_loadQueueDepthChart() {
  try {
    var res = await query(
      "SELECT date_bin('5 minutes'::INTERVAL, ts) AS t, " +
      "AVG(greptime_value) AS avg_depth " +
      "FROM openclaw_queue_depth_sum " +
      "WHERE ts > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;
    renderChart('oc-chart-queue', data, [
      { label: 'Avg Queue Depth', key: 'avg_depth', color: '#d2a8ff' },
    ], function(v) { return fmtNum(v); });
  } catch { /* table may not exist */ }
}

async function oc_loadRunDurationChart() {
  try {
    var res = await query(
      "SELECT date_bin('5 minutes'::INTERVAL, ts) AS t, SUM(greptime_value) AS runs " +
      "FROM openclaw_run_duration_ms_milliseconds_count " +
      "WHERE ts > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;
    renderChart('oc-chart-run-duration', data, [
      { label: 'Completed Runs', key: 'runs', color: '#f0883e' },
    ], function(v) { return fmtNum(v); });
  } catch { /* table may not exist */ }
}

function oc_loadMetricsExplorer() {
  initMetricsExplorer('oc-metrics');
}

// ===================================================================
// OpenClaw view — Traces tab (all openclaw.* span types)
// ===================================================================
async function oc_loadTraces() {
  var spanFilter = document.getElementById('oc-trace-span-filter').value;
  var modelFilter = document.getElementById('oc-trace-model-filter').value;
  var channelFilter = document.getElementById('oc-trace-channel-filter').value;
  var sessionFilter = document.getElementById('oc-trace-session-filter').value;
  var outcomeFilter = document.getElementById('oc-trace-outcome-filter').value;
  var textFilter = document.getElementById('oc-trace-query-filter').value.trim();
  var iv = intervalSQL();

  try {
    var cols = await oc_getTraceColumns();
    var hasModelCol = !!cols['span_attributes.openclaw.model'];
    var hasChannelCol = !!cols['span_attributes.openclaw.channel'];
    var hasOutcomeCol = !!cols['span_attributes.openclaw.outcome'];
    var hasSessionCol = !!cols['span_attributes.openclaw.sessionKey'];
    var where = "WHERE " + OC_ALL_SPANS + " AND timestamp > NOW() - INTERVAL '" + iv + "'";
    if (spanFilter) where += " AND span_name = '" + escapeSQLString(spanFilter) + "'";
    if (modelFilter && hasModelCol) where += " AND \"span_attributes.openclaw.model\" = '" + escapeSQLString(modelFilter) + "'";
    if (channelFilter && hasChannelCol) where += " AND \"span_attributes.openclaw.channel\" = '" + escapeSQLString(channelFilter) + "'";
    if (sessionFilter && hasSessionCol) where += " AND \"span_attributes.openclaw.sessionKey\" = '" + escapeSQLString(sessionFilter) + "'";
    if (outcomeFilter && hasOutcomeCol) where += " AND \"span_attributes.openclaw.outcome\" = '" + escapeSQLString(outcomeFilter) + "'";
    if (textFilter) {
      var safe = escapeSQLString(textFilter);
      var terms = ["trace_id LIKE '%" + safe + "%'", "span_name LIKE '%" + safe + "%'"];
      if (hasModelCol) terms.push("\"span_attributes.openclaw.model\" LIKE '%" + safe + "%'");
      if (hasChannelCol) terms.push("\"span_attributes.openclaw.channel\" LIKE '%" + safe + "%'");
      if (hasOutcomeCol) terms.push("\"span_attributes.openclaw.outcome\" LIKE '%" + safe + "%'");
      where += " AND (" + terms.join(' OR ') + ")";
    }

    var modelSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.model', 'model');
    var channelSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.channel', 'channel');
    var sessionSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.sessionKey', 'session_key');
    var inputSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.tokens.input', 'input_tok');
    var outputSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.tokens.output', 'output_tok');
    var cacheReadSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.tokens.cache_read', 'cache_read');
    var outcomeSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.outcome', 'outcome');
    var limit = ocTracePageSize + 1;
    var offset = ocTracePage * ocTracePageSize;

    var res = await query(
      "SELECT timestamp, trace_id, span_name, span_status_code, " +
      modelSel + ", " +
      channelSel + ", " +
      sessionSel + ", " +
      inputSel + ", " +
      outputSel + ", " +
      cacheReadSel + ", " +
      outcomeSel + ", " +
      "ROUND(duration_nano / 1000000.0, 1) AS duration_ms " +
      "FROM opentelemetry_traces " +
      where + " ORDER BY timestamp DESC LIMIT " + limit + " OFFSET " + offset
    );
    var allRows = rowsToObjects(res);
    ocTraceHasNext = allRows.length > ocTracePageSize;
    var data = ocTraceHasNext ? allRows.slice(0, ocTracePageSize) : allRows;
    var tbody = document.getElementById('oc-traces-body');

    if (!data.length) {
      if (ocTracePage > 0) {
        ocTracePage--;
        return oc_loadTraces();
      }
      tbody.innerHTML = '<tr><td colspan="9" class="loading">' + t('empty.no_traces') + '</td></tr>';
      oc_updateTracePager(0);
      return;
    }

    tbody.innerHTML = data.map(function(d) {
      var shortName = oc_shortSpanName(d.span_name);
      var isErr = d.span_status_code === 'STATUS_CODE_ERROR';
      var statusHtml = isErr
        ? '<span class="badge badge-error">ERROR</span>'
        : (d.outcome ? escapeHTML(d.outcome) : '\u2014');
      return '<tr class="clickable" onclick="oc_toggleTraceDetail(this, \'' + escapeHTML(d.trace_id) + '\')">' +
      '<td>' + fmtTime(d.timestamp) + '</td>' +
      '<td><span class="badge' + (isErr ? ' badge-error' : '') + '">' + escapeHTML(shortName) + '</span></td>' +
      '<td>' + escapeHTML(d.model || '\u2014') + '</td>' +
      '<td>' + escapeHTML(d.channel || '\u2014') + '</td>' +
      '<td>' + fmtNum(d.input_tok) + '</td>' +
      '<td>' + fmtNum(d.output_tok) + '</td>' +
      '<td>' + fmtNum(d.cache_read) + '</td>' +
      '<td>' + fmtDurMs(d.duration_ms) + '</td>' +
      '<td>' + statusHtml + '</td>' +
      '</tr>';
    }).join('');

    // Populate filter dropdowns from data
    oc_populateFilter('oc-trace-span-filter', data, 'span_name', 'All span types');
    oc_populateFilter('oc-trace-model-filter', data, 'model', t('filter.all_models'));
    oc_populateFilter('oc-trace-channel-filter', data, 'channel', 'All channels');
    oc_populateFilter('oc-trace-session-filter', data, 'session_key', 'All sessions');
    oc_populateFilter('oc-trace-outcome-filter', data, 'outcome', 'All outcomes');
    oc_updateTracePager(data.length);
  } catch (err) {
    ocTraceHasNext = false;
    oc_updateTracePager(0);
    document.getElementById('oc-traces-body').innerHTML =
      '<tr><td colspan="9" class="loading">Error: ' + escapeHTML(err.message) + '</td></tr>';
  }
}

function oc_populateFilter(selectId, data, key, defaultLabel) {
  var select = document.getElementById(selectId);
  var current = select.value;
  var values = [];
  var seen = {};
  data.forEach(function(d) {
    var v = d[key];
    if (v && !seen[v]) { seen[v] = true; values.push(v); }
  });
  select.innerHTML = '<option value="">' + defaultLabel + '</option>' +
    values.map(function(v) {
      return '<option value="' + escapeHTML(v) + '"' + (v === current ? ' selected' : '') + '>' + escapeHTML(v) + '</option>';
    }).join('');
}

function oc_toggleTraceDetail(clickedRow, traceId) {
  var prev = document.querySelector('.oc-trace-detail-row');
  var prevParent = document.querySelector('#oc-traces-body tr.active-trace');
  if (prevParent) prevParent.classList.remove('active-trace');
  if (prev) {
    if (prev.dataset.traceId === traceId) { prev.remove(); return; }
    prev.remove();
  }

  clickedRow.classList.add('active-trace');

  var detailRow = document.createElement('tr');
  detailRow.className = 'oc-trace-detail-row trace-detail-row';
  detailRow.dataset.traceId = traceId;
  detailRow.innerHTML = '<td colspan="9"><div class="trace-detail-inner">' +
    '<div class="detail-header"><h3>' + t('detail.trace_detail') + '</h3>' +
    '<button class="close-btn" onclick="oc_closeTraceDetail()">&times;</button></div>' +
    '<div class="trace-meta" id="oc-trace-meta"></div>' +
    '<div class="waterfall-section"><h4>' + t('detail.span_waterfall') + '</h4>' +
    '<div id="oc-span-waterfall" class="loading">' + t('empty.loading') + '</div></div>' +
    '</div></td>';
  clickedRow.after(detailRow);

  oc_loadTraceDetailData(traceId);
}

async function oc_loadTraceDetailData(traceId) {
  var metaEl = document.getElementById('oc-trace-meta');
  var waterfallEl = document.getElementById('oc-span-waterfall');
  var tid = escapeSQLString(traceId);

  try {
    var cols = await oc_getTraceColumns();
    var modelSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.model', 'model');
    var channelSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.channel', 'channel');
    var providerSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.provider', 'provider');
    var sessionSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.sessionKey', 'session_key');
    var outcomeSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.outcome', 'outcome');
    var messageSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.messageId', 'message_id');
    var inputSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.tokens.input', 'input_tok');
    var outputSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.tokens.output', 'output_tok');
    var cacheReadSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.tokens.cache_read', 'cache_read');
    var cacheWriteSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.tokens.cache_write', 'cache_write');
    var totalSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.tokens.total', 'total_tok');

    var res = await query(
      "SELECT timestamp, span_id, parent_span_id, span_name, span_status_code, " +
      modelSel + ", " +
      channelSel + ", " +
      providerSel + ", " +
      sessionSel + ", " +
      outcomeSel + ", " +
      messageSel + ", " +
      inputSel + ", " +
      outputSel + ", " +
      cacheReadSel + ", " +
      cacheWriteSel + ", " +
      totalSel + ", " +
      "duration_nano, span_status_code AS status " +
      "FROM opentelemetry_traces " +
      "WHERE trace_id = '" + tid + "' " +
      "ORDER BY timestamp LIMIT 100"
    );
    var spans = rowsToObjects(res);

    if (spans.length > 0) {
      var root = spans[0];
      var totalInput = spans.reduce(function(s, x) { return s + (Number(x.input_tok) || 0); }, 0);
      var totalOutput = spans.reduce(function(s, x) { return s + (Number(x.output_tok) || 0); }, 0);
      var totalAll = spans.reduce(function(s, x) { return s + (Number(x.total_tok) || 0); }, 0);
      var models = [...new Set(spans.map(function(x) { return x.model; }).filter(Boolean))];
      var channels = [...new Set(spans.map(function(x) { return x.channel; }).filter(Boolean))];
      var providers = [...new Set(spans.map(function(x) { return x.provider; }).filter(Boolean))];
      var messageIds = [...new Set(spans.map(function(x) { return x.message_id; }).filter(Boolean))];
      var spanTypes = [...new Set(spans.map(function(x) { return x.span_name; }).filter(Boolean))];
      var hasErrors = spans.some(function(x) { return x.span_status_code === 'STATUS_CODE_ERROR'; });
      metaEl.innerHTML =
        metaItem('Trace ID', traceId) +
        metaItem('Span Types', spanTypes.map(oc_shortSpanName).join(', ') || '\u2014') +
        metaItem(t('table.model'), models.join(', ') || '\u2014') +
        metaItem('Channel', channels.join(', ') || '\u2014') +
        metaItem('Provider', providers.join(', ') || '\u2014') +
        metaItem('Session', root.session_key || '\u2014') +
        metaItem('Outcome', root.outcome || '\u2014') +
        (hasErrors ? metaItem('Errors', '<span class="badge badge-error">YES</span>') : '') +
        (messageIds.length ? metaItem('Message ID', messageIds.join(', ')) : '') +
        metaItem(t('detail.spans'), spans.length) +
        metaItem(t('detail.input_tokens'), fmtNum(totalInput)) +
        metaItem(t('detail.output_tokens'), fmtNum(totalOutput)) +
        metaItem('Total Tokens (incl. cache)', fmtNum(totalAll)) +
        metaItem(t('table.duration'), fmtMs(root.duration_nano)) +
        metaItem(t('table.started'), fmtTime(root.timestamp));

      renderWaterfall(waterfallEl, spans);
    } else {
      metaEl.innerHTML = metaItem('Trace ID', traceId);
      waterfallEl.innerHTML = '<div class="loading">' + t('error.no_spans') + '</div>';
    }
  } catch {
    waterfallEl.innerHTML = '<div class="loading">' + t('error.load_spans') + '</div>';
  }

}

function oc_closeTraceDetail() {
  var row = document.querySelector('.oc-trace-detail-row');
  var parent = document.querySelector('#oc-traces-body tr.active-trace');
  if (parent) parent.classList.remove('active-trace');
  if (row) row.remove();
}

// ===================================================================
// OpenClaw view — Cost tab
// ===================================================================
async function oc_loadCostTab() {
  await loadPricing();
  await Promise.all([
    oc_loadCostByModel(),
    oc_loadCostByChannel(),
    oc_loadExpensiveRequests(),
    oc_loadCacheSavings(),
    oc_loadModelComparison(),
  ]);
}

async function oc_loadCostByModel() {
  try {
    var tc = getThemeColors();
    var costExpr = costCaseSQL(
      '"span_attributes.openclaw.model"',
      '"span_attributes.openclaw.tokens.input"',
      '"span_attributes.openclaw.tokens.output"'
    );
    var res = await query(
      "SELECT \"span_attributes.openclaw.model\" AS model, " +
      "ROUND(SUM(" + costExpr + "), 4) AS total_cost " +
      "FROM opentelemetry_traces " +
      "WHERE " + OC_MODEL_SPAN + " AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY model ORDER BY total_cost DESC LIMIT 10"
    );
    var data = rowsToObjects(res);
    var el = document.getElementById('oc-cost-by-model');
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

async function oc_loadCostByChannel() {
  try {
    var tc = getThemeColors();
    var costExpr = costCaseSQL(
      '"span_attributes.openclaw.model"',
      '"span_attributes.openclaw.tokens.input"',
      '"span_attributes.openclaw.tokens.output"'
    );
    var res = await query(
      "SELECT \"span_attributes.openclaw.channel\" AS channel, " +
      "ROUND(SUM(" + costExpr + "), 4) AS total_cost " +
      "FROM opentelemetry_traces " +
      "WHERE " + OC_MODEL_SPAN + " " +
      "  AND \"span_attributes.openclaw.channel\" IS NOT NULL " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY channel ORDER BY total_cost DESC LIMIT 10"
    );
    var data = rowsToObjects(res);
    var el = document.getElementById('oc-cost-by-channel');
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_data') + '</div>'; return; }
    var maxCost = Math.max.apply(null, data.map(function(d) { return Number(d.total_cost) || 0; }));
    el.innerHTML = data.map(function(d) {
      var pct = maxCost > 0 ? (Number(d.total_cost) / maxCost * 100) : 0;
      return '<div class="bar-row">' +
        '<div class="bar-label">' + escapeHTML(d.channel || 'unknown') + '</div>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + pct + '%;background:' + tc.blue + '"></div></div>' +
        '<div class="bar-value">' + fmtCost(d.total_cost) + '</div></div>';
    }).join('');
  } catch { /* ignore */ }
}

async function oc_loadExpensiveRequests() {
  try {
    var costExpr = costCaseSQL(
      '"span_attributes.openclaw.model"',
      '"span_attributes.openclaw.tokens.input"',
      '"span_attributes.openclaw.tokens.output"'
    );
    var res = await query(
      "SELECT timestamp, trace_id, " +
      "\"span_attributes.openclaw.model\" AS model, " +
      "\"span_attributes.openclaw.tokens.input\" AS input_tok, " +
      "\"span_attributes.openclaw.tokens.output\" AS output_tok, " +
      "ROUND(" + costExpr + ", 4) AS est_cost_usd, " +
      "ROUND(duration_nano / 1000000.0, 1) AS duration_ms " +
      "FROM opentelemetry_traces " +
      "WHERE " + OC_MODEL_SPAN + " AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "ORDER BY est_cost_usd DESC LIMIT 10"
    );
    var data = rowsToObjects(res);
    var tbody = document.getElementById('oc-expensive-body');
    if (!data.length) { tbody.innerHTML = '<tr><td colspan="6" class="loading">' + t('empty.no_data') + '</td></tr>'; return; }
    tbody.innerHTML = data.map(function(d) {
      return '<tr class="clickable" onclick="oc_switchToTrace(\'' + escapeHTML(d.trace_id) + '\')">' +
      '<td>' + fmtTime(d.timestamp) + '</td>' +
      '<td>' + escapeHTML(d.model || 'unknown') + '</td>' +
      '<td>' + fmtNum(d.input_tok) + '</td>' +
      '<td>' + fmtNum(d.output_tok) + '</td>' +
      '<td>' + fmtCost(d.est_cost_usd) + '</td>' +
      '<td>' + fmtDurMs(d.duration_ms) + '</td></tr>';
    }).join('');
  } catch { /* ignore */ }
}

async function oc_loadCacheSavings() {
  try {
    await loadPricing();
    var el = document.getElementById('oc-cache-savings');
    var res = await query(
      "SELECT \"span_attributes.openclaw.model\" AS model, " +
      "SUM(CAST(\"span_attributes.openclaw.tokens.cache_read\" AS DOUBLE)) AS cache_read " +
      "FROM opentelemetry_traces " +
      "WHERE " + OC_MODEL_SPAN + " AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY model"
    );
    var data = rowsToObjects(res);
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_cache_data') + '</div>'; return; }

    var totalSavings = 0;
    var details = data.map(function(d) {
      var cacheRead = Number(d.cache_read) || 0;
      var price = defaultPrice.i;
      modelPricing.forEach(function(m) {
        if ((d.model || '').indexOf(m.p) >= 0) price = m.i;
      });
      var savings = cacheRead * price / 1000000.0;
      totalSavings += savings;
      return { model: d.model, tokens: cacheRead, savings: savings };
    });

    var html = '<div style="font-size:24px;font-weight:700;color:var(--green);margin-bottom:12px">' +
      'Estimated savings: $' + totalSavings.toFixed(4) + '</div>';
    var tc = getThemeColors();
    var maxSav = Math.max.apply(null, details.map(function(d) { return d.savings; }));
    html += details.map(function(d) {
      var pct = maxSav > 0 ? (d.savings / maxSav * 100) : 0;
      return '<div class="bar-row">' +
        '<div class="bar-label">' + escapeHTML(d.model || 'unknown') + '</div>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + pct + '%;background:' + tc.green + '"></div></div>' +
        '<div class="bar-value">$' + d.savings.toFixed(4) + ' (' + fmtNum(d.tokens) + ' tok)</div></div>';
    }).join('');
    el.innerHTML = html;
  } catch { /* ignore */ }
}

async function oc_loadModelComparison() {
  try {
    var costExpr = costCaseSQL(
      '"span_attributes.openclaw.model"',
      '"span_attributes.openclaw.tokens.input"',
      '"span_attributes.openclaw.tokens.output"'
    );
    var res = await query(
      "SELECT \"span_attributes.openclaw.model\" AS model, " +
      "COUNT(*) AS reqs, " +
      "ROUND(AVG(duration_nano) / 1000000.0, 0) AS avg_lat, " +
      "ROUND(AVG(" + costExpr + "), 6) AS avg_cost " +
      "FROM opentelemetry_traces " +
      "WHERE " + OC_MODEL_SPAN + " AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY model ORDER BY reqs DESC"
    );
    var data = rowsToObjects(res);
    var tbody = document.getElementById('oc-model-compare-body');
    if (!data.length) { tbody.innerHTML = '<tr><td colspan="4" class="loading">' + t('empty.no_data') + '</td></tr>'; return; }
    tbody.innerHTML = data.map(function(d) {
      return '<tr>' +
        '<td>' + escapeHTML(d.model || 'unknown') + '</td>' +
        '<td>' + fmtNum(d.reqs) + '</td>' +
        '<td>' + fmtDurMs(d.avg_lat) + '</td>' +
        '<td>' + fmtCost(d.avg_cost) + '</td></tr>';
    }).join('');
  } catch { /* ignore */ }
}

function oc_filterBySession(sessionKey) {
  document.querySelectorAll('#oc-tabs .tab').forEach(function(t) { t.classList.remove('active'); });
  document.querySelectorAll('#view-openclaw .tab-content').forEach(function(t) { t.classList.remove('active'); });
  document.querySelector('[data-octab="oc-traces"]').classList.add('active');
  document.getElementById('tab-oc-traces').classList.add('active');
  oc_resetTracePaging();
  // Clear other filters, set session filter
  document.getElementById('oc-trace-span-filter').value = '';
  document.getElementById('oc-trace-model-filter').value = '';
  document.getElementById('oc-trace-channel-filter').value = '';
  document.getElementById('oc-trace-outcome-filter').value = '';
  document.getElementById('oc-trace-query-filter').value = '';
  var sessionSelect = document.getElementById('oc-trace-session-filter');
  // Ensure the session key is available as an option
  var found = false;
  for (var i = 0; i < sessionSelect.options.length; i++) {
    if (sessionSelect.options[i].value === sessionKey) { found = true; break; }
  }
  if (!found) {
    var opt = document.createElement('option');
    opt.value = sessionKey;
    opt.textContent = sessionKey;
    sessionSelect.appendChild(opt);
  }
  sessionSelect.value = sessionKey;
  oc_loadTraces();
}

async function oc_switchToTrace(traceId) {
  document.querySelectorAll('#oc-tabs .tab').forEach(function(t) { t.classList.remove('active'); });
  document.querySelectorAll('#view-openclaw .tab-content').forEach(function(t) { t.classList.remove('active'); });
  document.querySelector('[data-octab="oc-traces"]').classList.add('active');
  document.getElementById('tab-oc-traces').classList.add('active');
  oc_resetTracePaging();
  // Reset all filters and use query filter to locate the specific trace
  document.getElementById('oc-trace-span-filter').value = '';
  document.getElementById('oc-trace-model-filter').value = '';
  document.getElementById('oc-trace-channel-filter').value = '';
  document.getElementById('oc-trace-session-filter').value = '';
  document.getElementById('oc-trace-outcome-filter').value = '';
  var q = document.getElementById('oc-trace-query-filter');
  if (q) q.value = traceId;
  await oc_loadTraces();
  // Find and expand the target trace row
  var targetRow = null;
  document.querySelectorAll('#oc-traces-body tr.clickable').forEach(function(row) {
    if (row.getAttribute('onclick') && row.getAttribute('onclick').indexOf(traceId) !== -1) {
      targetRow = row;
    }
  });
  if (targetRow) {
    oc_toggleTraceDetail(targetRow, traceId);
    targetRow.scrollIntoView({ behavior: 'smooth', block: 'center' });
  }
}

// ===================================================================
// OpenClaw view — Search tab
// ===================================================================
async function oc_doSearch() {
  var term = document.getElementById('oc-search-input').value.trim();
  if (!term) return;
  var el = document.getElementById('oc-search-results');
  el.innerHTML = '<div class="loading">' + t('empty.searching') + '</div>';

  try {
    var safeTerm = escapeSQLString(term);
    var res = await query(
      "SELECT timestamp, trace_id, span_name, span_status_code, " +
      "\"span_attributes.openclaw.model\" AS model, " +
      "\"span_attributes.openclaw.channel\" AS channel " +
      "FROM opentelemetry_traces " +
      "WHERE " + OC_ALL_SPANS +
      "  AND (\"span_attributes.openclaw.model\" LIKE '%" + safeTerm + "%' " +
      "    OR \"span_attributes.openclaw.channel\" LIKE '%" + safeTerm + "%' " +
      "    OR span_name LIKE '%" + safeTerm + "%') " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "ORDER BY timestamp DESC LIMIT 50"
    );
    var data = rowsToObjects(res);
    if (!data.length) { el.innerHTML = '<div class="loading">' + t('empty.no_results') + '</div>'; return; }
    el.innerHTML = data.map(function(d) {
      var isErr = d.span_status_code === 'STATUS_CODE_ERROR';
      return '<div class="search-result-item" onclick="oc_switchToTrace(\'' + escapeHTML(d.trace_id) + '\')">' +
      '<div class="search-result-meta">' +
      '<span>' + fmtTime(d.timestamp) + '</span>' +
      '<span class="badge' + (isErr ? ' badge-error' : '') + '">' + escapeHTML(oc_shortSpanName(d.span_name)) + '</span>' +
      '<span>' + escapeHTML(d.model || '') + '</span>' +
      '<span>' + escapeHTML(d.channel || '') + '</span>' +
      '</div>' +
      '<div class="search-result-content">' + escapeHTML(d.model || 'unknown') + ' &middot; ' + escapeHTML(d.channel || '') + '</div>' +
      '</div>';
    }).join('');
  } catch (err) {
    el.innerHTML = '<div class="loading">' + t('error.search') + escapeHTML(err.message) + '</div>';
  }
}

async function oc_loadAnomalies() {
  var el = document.getElementById('oc-anomaly-list');
  el.innerHTML = '<div class="loading">' + t('empty.loading_anomalies') + '</div>';

  try {
    // Fetch anomalies: non-completed outcomes, high token usage, error spans, stuck sessions
    var res = await query(
      "SELECT trace_id, timestamp, span_name, span_status_code, " +
      "\"span_attributes.openclaw.model\" AS model, " +
      "\"span_attributes.openclaw.channel\" AS channel, " +
      "\"span_attributes.openclaw.tokens.input\" AS input_tok, " +
      "\"span_attributes.openclaw.tokens.output\" AS output_tok, " +
      "\"span_attributes.openclaw.outcome\" AS outcome, " +
      "ROUND(duration_nano / 1000000.0, 1) AS duration_ms " +
      "FROM opentelemetry_traces " +
      "WHERE " + OC_ALL_SPANS +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "  AND (span_status_code = 'STATUS_CODE_ERROR' " +
      "    OR span_name = 'openclaw.session.stuck' " +
      "    OR (\"span_attributes.openclaw.outcome\" IS NOT NULL AND \"span_attributes.openclaw.outcome\" != 'completed') " +
      "    OR CAST(\"span_attributes.openclaw.tokens.input\" AS DOUBLE) > 10000) " +
      "ORDER BY timestamp DESC LIMIT 20"
    );
    var data = rowsToObjects(res);
    if (!data.length) { el.innerHTML = '<div class="loading">' + t('empty.no_anomalies') + '</div>'; return; }
    el.innerHTML = data.map(function(d) {
      var reason;
      var severity = '';
      if (d.span_name === 'openclaw.webhook.error') {
        reason = 'Webhook Error';
        severity = 'badge-error';
      } else if (d.span_name === 'openclaw.session.stuck') {
        reason = 'Session Stuck';
        severity = 'badge-error';
      } else if (d.span_status_code === 'STATUS_CODE_ERROR') {
        reason = 'Span Error (' + oc_shortSpanName(d.span_name) + ')';
        severity = 'badge-error';
      } else if (d.outcome && d.outcome !== 'completed') {
        reason = 'Outcome: ' + d.outcome;
        severity = 'warn';
      } else {
        reason = t('anomaly.high_token');
        severity = 'warn';
      }
      return '<div class="anomaly-item ' + severity + '" onclick="oc_switchToTrace(\'' + escapeHTML(d.trace_id) + '\')">' +
        '<div class="anomaly-reason">' + reason + '</div>' +
        '<div style="font-size:13px">' +
        '<span class="badge">' + escapeHTML(oc_shortSpanName(d.span_name)) + '</span> &middot; ' +
        escapeHTML(d.model || '') +
        (d.channel ? ' &middot; ' + escapeHTML(d.channel) : '') +
        (d.input_tok ? ' &middot; ' + fmtNum(d.input_tok) + ' in / ' + fmtNum(d.output_tok) + ' out' : '') +
        (d.duration_ms ? ' &middot; ' + fmtDurMs(d.duration_ms) : '') +
        ' &middot; ' + fmtTime(d.timestamp) +
        '</div></div>';
    }).join('');
  } catch {
    el.innerHTML = '<div class="loading">' + t('error.load_anomalies') + '</div>';
  }
}

function oc_loadSearch() {
  oc_loadAnomalies();
}

// ===================================================================
// OpenClaw view — Security tab (behavioral anomaly monitoring)
// ===================================================================
async function oc_loadSecurityTab() {
  await Promise.all([
    oc_loadSecuritySummary(),
    oc_loadChannelErrorRate(),
    oc_loadSessionHealth(),
    oc_loadWebhookTimeline(),
    oc_loadTokenAnomalies(),
    oc_loadChannelActivity(),
  ]);
}

async function oc_loadSecuritySummary() {
  var iv = intervalSQL();
  try {
    var cols = await oc_getTraceColumns();
    var hasOutcome = !!cols['span_attributes.openclaw.outcome'];
    var hasInputTok = !!cols['span_attributes.openclaw.tokens.input'];

    var queries = [
      query(
        "SELECT COUNT(*) AS v FROM opentelemetry_traces " +
        "WHERE span_name = 'openclaw.webhook.error' AND timestamp > NOW() - INTERVAL '" + iv + "'"
      ),
      query(
        "SELECT COUNT(*) AS v FROM opentelemetry_traces " +
        "WHERE span_name = 'openclaw.session.stuck' AND timestamp > NOW() - INTERVAL '" + iv + "'"
      ),
    ];
    if (hasOutcome) {
      queries.push(query(
        "SELECT COUNT(*) AS v FROM opentelemetry_traces " +
        "WHERE span_name = 'openclaw.message.processed' " +
        "AND \"span_attributes.openclaw.outcome\" IS NOT NULL " +
        "AND \"span_attributes.openclaw.outcome\" != 'completed' " +
        "AND timestamp > NOW() - INTERVAL '" + iv + "'"
      ));
    } else {
      queries.push(Promise.resolve(null));
    }
    if (hasInputTok) {
      queries.push(query(
        "SELECT COUNT(*) AS v FROM opentelemetry_traces " +
        "WHERE span_name = 'openclaw.model.usage' " +
        "AND CAST(\"span_attributes.openclaw.tokens.input\" AS DOUBLE) > 10000 " +
        "AND timestamp > NOW() - INTERVAL '" + iv + "'"
      ));
    } else {
      queries.push(Promise.resolve(null));
    }

    var results = await Promise.all(queries);
    document.getElementById('oc-sec-webhook-errors').textContent = fmtNum(rows(results[0])?.[0]?.[0]);
    document.getElementById('oc-sec-stuck-sessions').textContent = fmtNum(rows(results[1])?.[0]?.[0]);
    document.getElementById('oc-sec-failed-outcomes').textContent = results[2] ? fmtNum(rows(results[2])?.[0]?.[0]) : '0';
    document.getElementById('oc-sec-token-anomalies').textContent = results[3] ? fmtNum(rows(results[3])?.[0]?.[0]) : '0';
  } catch { /* no data */ }
}

async function oc_loadChannelErrorRate() {
  var el = document.getElementById('oc-sec-channel-errors');
  try {
    var cols = await oc_getTraceColumns();
    var channelSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.channel', 'channel');
    var iv = intervalSQL();
    var res = await query(
      "SELECT " + channelSel + ", " +
      "COUNT(*) AS total, " +
      "SUM(CASE WHEN span_status_code = 'STATUS_CODE_ERROR' THEN 1 ELSE 0 END) AS errors " +
      "FROM opentelemetry_traces " +
      "WHERE " + OC_ALL_SPANS + " AND timestamp > NOW() - INTERVAL '" + iv + "' " +
      "GROUP BY channel ORDER BY errors DESC LIMIT 10"
    );
    var data = rowsToObjects(res);
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_data') + '</div>'; return; }
    var maxTotal = Math.max.apply(null, data.map(function(d) { return Number(d.total) || 0; }));
    el.innerHTML = data.map(function(d) {
      var total = Number(d.total) || 0;
      var errors = Number(d.errors) || 0;
      var errRate = total > 0 ? (errors / total * 100) : 0;
      var pct = maxTotal > 0 ? (total / maxTotal * 100) : 0;
      var color = errRate >= 20 ? 'var(--red)' : (errRate >= 5 ? 'var(--yellow)' : 'var(--green)');
      return '<div class="bar-row">' +
        '<div class="bar-label">' + escapeHTML(d.channel || 'unknown') + '</div>' +
        '<div class="bar-track bar-track-stacked">' +
        '<div class="bar-fill" style="width:' + pct + '%;background:' + color + '"></div>' +
        '</div>' +
        '<div class="bar-value">' + errors + '/' + total + ' (' + errRate.toFixed(1) + '%)</div></div>';
    }).join('');
  } catch { el.innerHTML = '<div class="chart-empty">' + t('empty.no_data') + '</div>'; }
}

async function oc_loadSessionHealth() {
  var tbody = document.getElementById('oc-sec-session-body');
  try {
    var cols = await oc_getTraceColumns();
    var sessionSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.sessionKey', 'session_key');
    var channelSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.channel', 'channel');
    var iv = intervalSQL();
    var res = await query(
      "SELECT timestamp, trace_id, " + sessionSel + ", " + channelSel + ", " +
      "span_name, ROUND(duration_nano / 1e9, 1) AS duration_s " +
      "FROM opentelemetry_traces " +
      "WHERE span_name IN ('openclaw.session.stuck', 'openclaw.webhook.error') " +
      "AND timestamp > NOW() - INTERVAL '" + iv + "' " +
      "ORDER BY timestamp DESC LIMIT 30"
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      tbody.innerHTML = '<tr><td colspan="5" class="loading">' + t('empty.no_session_issues') + '</td></tr>';
      return;
    }
    tbody.innerHTML = data.map(function(d) {
      var shortName = oc_shortSpanName(d.span_name);
      var isErr = d.span_name === 'openclaw.webhook.error';
      var onclick = d.session_key
        ? 'oc_filterBySession(\'' + escapeHTML(d.session_key) + '\')'
        : 'oc_switchToTrace(\'' + escapeHTML(d.trace_id) + '\')';
      return '<tr class="clickable" onclick="' + onclick + '">' +
        '<td>' + fmtTime(d.timestamp) + '</td>' +
        '<td>' + escapeHTML(d.session_key || '\u2014') + '</td>' +
        '<td><span class="badge' + (isErr ? ' badge-error' : '') + '">' + escapeHTML(shortName) + '</span></td>' +
        '<td>' + escapeHTML(d.channel || '\u2014') + '</td>' +
        '<td>' + (d.duration_s != null ? d.duration_s + 's' : '\u2014') + '</td></tr>';
    }).join('');
  } catch {
    tbody.innerHTML = '<tr><td colspan="5" class="loading">' + t('empty.no_data') + '</td></tr>';
  }
}

async function oc_loadWebhookTimeline() {
  var el = document.getElementById('oc-sec-webhook-timeline');
  try {
    var cols = await oc_getTraceColumns();
    var channelSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.channel', 'channel');
    var sessionSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.sessionKey', 'session_key');
    var iv = intervalSQL();
    var res = await query(
      "SELECT timestamp, trace_id, " + channelSel + ", " + sessionSel + ", " +
      "ROUND(duration_nano / 1e9, 1) AS duration_s " +
      "FROM opentelemetry_traces " +
      "WHERE span_name = 'openclaw.webhook.error' " +
      "AND timestamp > NOW() - INTERVAL '" + iv + "' " +
      "ORDER BY timestamp DESC LIMIT 20"
    );
    var data = rowsToObjects(res);
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_webhook_errors') + '</div>'; return; }
    el.innerHTML = data.map(function(d) {
      return '<div class="anomaly-item badge-error" onclick="oc_switchToTrace(\'' + escapeHTML(d.trace_id) + '\')">' +
        '<div class="anomaly-reason">Webhook Error</div>' +
        '<div style="font-size:13px">' +
        (d.channel ? escapeHTML(d.channel) + ' &middot; ' : '') +
        (d.session_key ? 'session: ' + escapeHTML(d.session_key) + ' &middot; ' : '') +
        (d.duration_s != null ? d.duration_s + 's &middot; ' : '') +
        fmtTime(d.timestamp) +
        '</div></div>';
    }).join('');
  } catch { el.innerHTML = '<div class="chart-empty">' + t('empty.no_data') + '</div>'; }
}

async function oc_loadTokenAnomalies() {
  var el = document.getElementById('oc-sec-token-anomalies-list');
  try {
    var cols = await oc_getTraceColumns();
    if (!cols['span_attributes.openclaw.tokens.input']) {
      el.innerHTML = '<div class="chart-empty">' + t('empty.no_token_anomalies') + '</div>';
      return;
    }
    var modelSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.model', 'model');
    var channelSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.channel', 'channel');
    var iv = intervalSQL();
    var res = await query(
      "SELECT timestamp, trace_id, " + modelSel + ", " + channelSel + ", " +
      "CAST(\"span_attributes.openclaw.tokens.input\" AS BIGINT) AS input_tok, " +
      "CAST(\"span_attributes.openclaw.tokens.output\" AS BIGINT) AS output_tok " +
      "FROM opentelemetry_traces " +
      "WHERE span_name = 'openclaw.model.usage' " +
      "AND CAST(\"span_attributes.openclaw.tokens.input\" AS DOUBLE) > 10000 " +
      "AND timestamp > NOW() - INTERVAL '" + iv + "' " +
      "ORDER BY CAST(\"span_attributes.openclaw.tokens.input\" AS DOUBLE) DESC LIMIT 20"
    );
    var data = rowsToObjects(res);
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_token_anomalies') + '</div>'; return; }
    el.innerHTML = data.map(function(d) {
      return '<div class="anomaly-item warn" onclick="oc_switchToTrace(\'' + escapeHTML(d.trace_id) + '\')">' +
        '<div class="anomaly-reason">' + t('anomaly.high_token') + ' (' + fmtNum(d.input_tok) + ' input)</div>' +
        '<div style="font-size:13px">' +
        escapeHTML(d.model || 'unknown') +
        (d.channel ? ' &middot; ' + escapeHTML(d.channel) : '') +
        ' &middot; ' + fmtNum(d.input_tok) + ' in / ' + fmtNum(d.output_tok) + ' out' +
        ' &middot; ' + fmtTime(d.timestamp) +
        '</div></div>';
    }).join('');
  } catch { el.innerHTML = '<div class="chart-empty">' + t('empty.no_data') + '</div>'; }
}

async function oc_loadChannelActivity() {
  try {
    var iv = intervalSQL();
    var res = await query(
      "SELECT date_bin('5 minutes'::INTERVAL, ts) AS t, " +
      "SUM(greptime_value) AS msg_count " +
      "FROM openclaw_message_queued_total " +
      "WHERE ts > NOW() - INTERVAL '" + iv + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;
    renderChart('oc-sec-channel-activity', data, [
      { label: 'Messages/5min', key: 'msg_count', color: '#79c0ff' },
    ], function(v) { return fmtNum(v); });
  } catch { /* table may not exist */ }
}

function oc_onTabChange(tab) {
  if (tab === 'oc-overview') oc_loadOverview();
  else if (tab === 'oc-traces') oc_loadTraces();
  else if (tab === 'oc-cost') oc_loadCostTab();
  else if (tab === 'oc-security') oc_loadSecurityTab();
  else if (tab === 'oc-search') oc_loadSearch();
}
