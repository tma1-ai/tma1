// openclaw.js — OpenClaw view: all oc_* functions
// Depends on: core.js (setHealthFromData), chart.js, i18n.js, metrics-explorer.js, traces.js (renderWaterfall)

// OpenClaw span filters
var OC_MODEL_SPAN = "span_name = 'openclaw.model.usage'";
var OC_MSG_SPAN = "span_name = 'openclaw.message.processed'";
var OC_ALL_SPANS = "span_name LIKE 'openclaw.%'";
var ocTracePage = 0;
var ocTracePageSize = 15;
var ocTraceHasNext = false;
var ocTraceColumnsPromise = null;
var ocSessionsPage = 0;
var ocSessionsPageSize = 10;
var ocSessionsHasNext = false;
var ocSessionsData = [];

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
    info.textContent = t('pager.no_results');
    return;
  }
  var start = ocTracePage * ocTracePageSize + 1;
  var end = start + resultCount - 1;
  info.textContent = t('pager.page') + ' ' + (ocTracePage + 1) + ' \u00b7 ' + start + '-' + end;
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
    var reqCount = Number(rows(results[2])?.[0]?.[0]) || 0;
    document.getElementById('oc-val-cost').textContent = fmtCost(rows(results[0])?.[0]?.[0]);
    document.getElementById('oc-val-tokens').textContent = fmtNum(rows(results[1])?.[0]?.[0]);
    document.getElementById('oc-val-requests').textContent = fmtNum(reqCount);
    var latVal = rows(results[3])?.[0]?.[0];
    document.getElementById('oc-val-latency').textContent = fmtDurMs(latVal);

    // Health indicator (5-minute window)
    oc_updateHealthIndicator(reqCount);

    return reqCount > 0;
  } catch (err) {
    var banner = document.getElementById('error-banner');
    banner.style.display = 'block';
    banner.textContent = 'OpenClaw metrics error: ' + err.message;
    return false;
  }
}

async function oc_updateHealthIndicator(reqCount) {
  var el = document.getElementById('oc-health-indicator');
  if (!el) return;
  if (!reqCount) {
    el.className = 'health-indicator health-na';
    el.innerHTML = '<span class="health-dot"></span><span class="health-text">N/A</span>';
    return;
  }
  try {
    var res = await query(
      "SELECT COUNT(*) AS total, " +
      "SUM(CASE WHEN span_status_code = 'STATUS_CODE_ERROR' THEN 1 ELSE 0 END) AS errors, " +
      "ROUND(APPROX_PERCENTILE_CONT(duration_nano, 0.95) / 1000000.0, 0) AS p95_ms " +
      "FROM opentelemetry_traces " +
      "WHERE " + OC_MODEL_SPAN + " AND timestamp > NOW() - INTERVAL '5 minutes'"
    );
    var r = rowsToObjects(res)[0] || {};
    setHealthFromData(el, r);
  } catch {
    el.className = 'health-indicator health-na';
    el.innerHTML = '<span class="health-dot"></span><span class="health-text">N/A</span>';
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
    oc_loadSuccessRateChart(),
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
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, timestamp) AS t, " +
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
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, timestamp) AS t, " +
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
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, timestamp) AS t, " +
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

async function oc_loadSuccessRateChart() {
  try {
    var res = await query(
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, greptime_timestamp) AS t, " +
      "SUM(CASE WHEN openclaw_outcome = 'completed' THEN greptime_value ELSE 0 END) " +
      "/ NULLIF(SUM(greptime_value), 0) * 100 AS rate " +
      "FROM openclaw_message_processed_total " +
      "WHERE greptime_timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;
    renderChart('oc-chart-success-rate', data, [
      { label: 'Success Rate %', key: 'rate', color: '#3fb950' },
    ], function(v) { return Number(v).toFixed(1) + '%'; });
  } catch { /* table may not exist */ }
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
        "SELECT date_bin('" + chartBucket() + "'::INTERVAL, greptime_timestamp) AS t, SUM(greptime_value) AS v " +
        "FROM openclaw_message_processed_total " +
        "WHERE greptime_timestamp > NOW() - INTERVAL '" + iv + "' GROUP BY t ORDER BY t"
      ),
      query(
        "SELECT date_bin('" + chartBucket() + "'::INTERVAL, greptime_timestamp) AS t, SUM(greptime_value) AS v " +
        "FROM openclaw_message_queued_total " +
        "WHERE greptime_timestamp > NOW() - INTERVAL '" + iv + "' GROUP BY t ORDER BY t"
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
        "SELECT date_bin('" + chartBucket() + "'::INTERVAL, greptime_timestamp) AS t, AVG(greptime_value) AS v " +
        "FROM openclaw_context_tokens_sum " +
        "WHERE openclaw_context = 'used' AND greptime_timestamp > NOW() - INTERVAL '" + iv + "' " +
        "GROUP BY t ORDER BY t"
      ),
      query(
        "SELECT date_bin('" + chartBucket() + "'::INTERVAL, greptime_timestamp) AS t, AVG(greptime_value) AS v " +
        "FROM openclaw_context_tokens_sum " +
        "WHERE openclaw_context = 'limit' AND greptime_timestamp > NOW() - INTERVAL '" + iv + "' " +
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
      "WHERE greptime_timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
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
      "WHERE greptime_timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
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
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, greptime_timestamp) AS t, " +
      "AVG(greptime_value) AS avg_depth " +
      "FROM openclaw_queue_depth_sum " +
      "WHERE greptime_timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
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
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, greptime_timestamp) AS t, SUM(greptime_value) AS runs " +
      "FROM openclaw_run_duration_ms_milliseconds_count " +
      "WHERE greptime_timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
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
// OpenClaw view — Sessions tab
// ===================================================================
function oc_prevSessionsPage() {
  if (ocSessionsPage <= 0) return;
  ocSessionsPage--;
  oc_renderSessions();
}

function oc_nextSessionsPage() {
  if (!ocSessionsHasNext) return;
  ocSessionsPage++;
  oc_renderSessions();
}

function oc_updateSessionsPager(resultCount) {
  var prevBtn = document.getElementById('oc-sessions-prev-btn');
  var nextBtn = document.getElementById('oc-sessions-next-btn');
  var info = document.getElementById('oc-sessions-page-info');
  if (!prevBtn || !nextBtn || !info) return;
  prevBtn.disabled = ocSessionsPage <= 0;
  nextBtn.disabled = !ocSessionsHasNext;
  if (!resultCount) { info.textContent = t('pager.no_results'); return; }
  var start = ocSessionsPage * ocSessionsPageSize + 1;
  var end = start + resultCount - 1;
  info.textContent = t('pager.page') + ' ' + (ocSessionsPage + 1) + ' \u00b7 ' + start + '-' + end;
}

async function oc_loadSessions() {
  ocSessionsPage = 0;
  var tbody = document.getElementById('oc-sessions-body');
  tbody.innerHTML = '<tr><td colspan="7" class="loading">' + t('empty.loading') + '</td></tr>';

  var iv = intervalSQL();
  var filter = (document.getElementById('oc-session-filter').value || '').trim();

  try {
    var cols = await oc_getTraceColumns();
    var hasSessionKey = !!cols['span_attributes.openclaw.sessionKey'];
    var hasChannel = !!cols['span_attributes.openclaw.channel'];
    var hasInputTok = !!cols['span_attributes.openclaw.tokens.input'];
    var hasOutputTok = !!cols['span_attributes.openclaw.tokens.output'];

    if (hasSessionKey) {
      // GROUP BY sessionKey at SQL level
      var sessionSel = '"span_attributes.openclaw.sessionKey"';
      var channelSel = hasChannel
        ? 'MAX("span_attributes.openclaw.channel")' : 'NULL';
      var inputSel = hasInputTok
        ? 'SUM(CAST("span_attributes.openclaw.tokens.input" AS DOUBLE))' : '0';
      var outputSel = hasOutputTok
        ? 'SUM(CAST("span_attributes.openclaw.tokens.output" AS DOUBLE))' : '0';

      var where = "WHERE " + OC_ALL_SPANS + " AND timestamp > NOW() - INTERVAL '" + iv + "'";
      if (filter) {
        where += " AND " + sessionSel + " LIKE '%" + escapeSQLString(filter) + "%'";
      }

      await loadPricing();
      var costExpr = costCaseSQL(
        '"span_attributes.openclaw.model"',
        '"span_attributes.openclaw.tokens.input"',
        '"span_attributes.openclaw.tokens.output"'
      );

      var res = await query(
        "SELECT " + sessionSel + " AS session_key, " +
        channelSel + " AS channel, " +
        "MIN(timestamp) AS started_at, " +
        "MAX(timestamp) AS ended_at, " +
        "COUNT(*) AS span_count, " +
        "SUM(CASE WHEN span_name='openclaw.message.processed' THEN 1 ELSE 0 END) AS messages, " +
        inputSel + " AS input_tok, " +
        outputSel + " AS output_tok, " +
        "SUM(CASE WHEN span_status_code='STATUS_CODE_ERROR' THEN 1 ELSE 0 END) AS errors " +
        "FROM opentelemetry_traces " +
        where +
        " GROUP BY session_key ORDER BY started_at DESC LIMIT 200"
      );
      var data = rowsToObjects(res);

      // Estimate cost per session — query cost sums grouped by session
      var costData = [];
      if (data.length > 0) {
        try {
          var costRes = await query(
            "SELECT " + sessionSel + " AS session_key, " +
            "ROUND(SUM(" + costExpr + "), 4) AS total_cost " +
            "FROM opentelemetry_traces " +
            "WHERE " + OC_MODEL_SPAN + " AND timestamp > NOW() - INTERVAL '" + iv + "' " +
            "GROUP BY session_key"
          );
          costData = rowsToObjects(costRes);
        } catch { /* cost estimation failed */ }
      }
      var costMap = {};
      costData.forEach(function(c) { costMap[c.session_key] = Number(c.total_cost) || 0; });

      ocSessionsData = data.map(function(d) {
        var totalTok = (Number(d.input_tok) || 0) + (Number(d.output_tok) || 0);
        return {
          session_key: d.session_key || '\u2014',
          channel: d.channel || '\u2014',
          started_at: d.started_at,
          ended_at: d.ended_at,
          messages: Number(d.messages) || 0,
          tokens: totalTok,
          cost: costMap[d.session_key] || 0,
          errors: Number(d.errors) || 0,
        };
      });
    } else {
      // Fallback: group by trace_id
      var modelSel2 = oc_traceAttrSelect(cols, 'span_attributes.openclaw.model', 'model');
      var channelSel2 = oc_traceAttrSelect(cols, 'span_attributes.openclaw.channel', 'channel');
      var inputSel2 = oc_traceAttrSelect(cols, 'span_attributes.openclaw.tokens.input', 'input_tok');
      var outputSel2 = oc_traceAttrSelect(cols, 'span_attributes.openclaw.tokens.output', 'output_tok');

      var res2 = await query(
        "SELECT trace_id, " + modelSel2 + ", " + channelSel2 + ", " +
        inputSel2 + ", " + outputSel2 + ", " +
        "timestamp, span_name, span_status_code, " +
        "ROUND(duration_nano / 1000000.0, 1) AS duration_ms " +
        "FROM opentelemetry_traces " +
        "WHERE " + OC_ALL_SPANS + " AND timestamp > NOW() - INTERVAL '" + iv + "' " +
        "ORDER BY timestamp DESC LIMIT 2000"
      );
      var allSpans = rowsToObjects(res2);

      // Group by trace_id
      var traceMap = {};
      allSpans.forEach(function(s) {
        var tid = s.trace_id;
        if (!traceMap[tid]) {
          traceMap[tid] = {
            session_key: tid.substring(0, 12),
            channel: s.channel || '\u2014',
            started_at: s.timestamp,
            ended_at: s.timestamp,
            messages: 0,
            tokens: 0,
            cost: 0,
            errors: 0,
          };
        }
        var grp = traceMap[tid];
        if (s.timestamp < grp.started_at) grp.started_at = s.timestamp;
        if (s.timestamp > grp.ended_at) grp.ended_at = s.timestamp;
        if (s.span_name === 'openclaw.message.processed') grp.messages++;
        grp.tokens += (Number(s.input_tok) || 0) + (Number(s.output_tok) || 0);
        if (s.span_status_code === 'STATUS_CODE_ERROR') grp.errors++;
        if (s.channel && s.channel !== '\u2014') grp.channel = s.channel;
      });
      ocSessionsData = Object.values(traceMap).sort(function(a, b) {
        return String(b.started_at) < String(a.started_at) ? -1 : 1;
      });
    }

    oc_renderSessions();
  } catch (err) {
    tbody.innerHTML = '<tr><td colspan="7" class="loading">Error: ' + escapeHTML(err.message) + '</td></tr>';
    oc_updateSessionsPager(0);
  }
}

function oc_renderSessions() {
  var tbody = document.getElementById('oc-sessions-body');
  var start = ocSessionsPage * ocSessionsPageSize;
  var page = ocSessionsData.slice(start, start + ocSessionsPageSize + 1);
  ocSessionsHasNext = page.length > ocSessionsPageSize;
  if (ocSessionsHasNext) page = page.slice(0, ocSessionsPageSize);

  if (!page.length) {
    if (ocSessionsPage > 0) { ocSessionsPage--; oc_renderSessions(); return; }
    tbody.innerHTML = '<tr><td colspan="7" class="loading">' + t('empty.no_data') + '</td></tr>';
    oc_updateSessionsPager(0);
    return;
  }

  tbody.innerHTML = page.map(function(d, i) {
    var idx = start + i;
    var startMs = parseTimestamp(d.started_at);
    var endMs = parseTimestamp(d.ended_at);
    var durMs = (!isNaN(startMs) && !isNaN(endMs)) ? endMs - startMs : 0;
    var durStr = durMs < 1000 ? Math.round(durMs) + 'ms'
      : durMs < 60000 ? (durMs / 1000).toFixed(1) + 's'
      : (durMs / 60000).toFixed(1) + 'min';
    var errHtml = d.errors > 0
      ? '<span class="badge badge-error">' + d.errors + '</span>'
      : '0';
    return '<tr class="clickable" onclick="oc_toggleSessionDetail(this, ' + idx + ')">' +
      '<td title="' + escapeHTML(d.session_key) + '">' + escapeHTML(d.session_key.length > 16 ? d.session_key.substring(0, 16) + '\u2026' : d.session_key) + '</td>' +
      '<td>' + escapeHTML(d.channel) + '</td>' +
      '<td>' + fmtNum(d.messages) + '</td>' +
      '<td>' + fmtNum(d.tokens) + '</td>' +
      '<td>' + fmtCost(d.cost) + '</td>' +
      '<td>' + durStr + '</td>' +
      '<td>' + errHtml + '</td></tr>';
  }).join('');
  oc_updateSessionsPager(page.length);
}

async function oc_toggleSessionDetail(clickedRow, idx) {
  // Collapse if already expanded
  var prev = clickedRow.nextElementSibling;
  if (prev && prev.classList.contains('oc-session-detail-row')) {
    prev.remove();
    clickedRow.classList.remove('active-trace');
    return;
  }
  // Collapse any other open detail
  var existing = document.querySelector('.oc-session-detail-row');
  if (existing) {
    var ep = existing.previousElementSibling;
    if (ep) ep.classList.remove('active-trace');
    existing.remove();
  }

  clickedRow.classList.add('active-trace');
  var session = ocSessionsData[idx];
  if (!session) return;

  var detailRow = document.createElement('tr');
  detailRow.className = 'oc-session-detail-row trace-detail-row';
  detailRow.innerHTML = '<td colspan="7"><div class="trace-detail-inner" style="padding:12px">' +
    '<div class="loading">' + t('empty.loading') + '</div></div></td>';
  clickedRow.after(detailRow);

  // Fetch spans for this session
  var iv = intervalSQL();
  try {
    var cols = await oc_getTraceColumns();
    var hasSessionKey = !!cols['span_attributes.openclaw.sessionKey'];
    var modelSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.model', 'model');
    var channelSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.channel', 'channel');
    var providerSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.provider', 'provider');
    var inputSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.tokens.input', 'input_tok');
    var outputSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.tokens.output', 'output_tok');
    var cacheReadSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.tokens.cache_read', 'cache_read');
    var cacheWriteSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.tokens.cache_write', 'cache_write');
    var totalTokSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.tokens.total', 'total_tok');
    var outcomeSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.outcome', 'outcome');
    var messageSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.messageId', 'message_id');
    var sessionIdSel = oc_traceAttrSelect(cols, 'span_attributes.openclaw.sessionId', 'session_id');

    var where;
    if (hasSessionKey) {
      where = "WHERE " + OC_ALL_SPANS +
        " AND \"span_attributes.openclaw.sessionKey\" = '" + escapeSQLString(session.session_key) + "'" +
        " AND timestamp > NOW() - INTERVAL '" + iv + "'";
    } else {
      where = "WHERE " + OC_ALL_SPANS +
        " AND trace_id LIKE '" + escapeSQLString(session.session_key) + "%'" +
        " AND timestamp > NOW() - INTERVAL '" + iv + "'";
    }

    var res = await query(
      "SELECT timestamp, trace_id, span_name, span_status_code, " +
      modelSel + ", " + channelSel + ", " + providerSel + ", " +
      inputSel + ", " + outputSel + ", " + cacheReadSel + ", " +
      cacheWriteSel + ", " + totalTokSel + ", " +
      outcomeSel + ", " + messageSel + ", " + sessionIdSel + ", " +
      "ROUND(duration_nano / 1000000.0, 1) AS duration_ms " +
      "FROM opentelemetry_traces " +
      where + " ORDER BY timestamp LIMIT 100"
    );
    var spans = rowsToObjects(res);
    var inner = detailRow.querySelector('.trace-detail-inner');

    if (!spans.length) {
      inner.innerHTML = '<div class="loading">' + t('empty.no_data') + '</div>';
      return;
    }

    var html = '<div style="margin-bottom:8px"><strong>Session:</strong> ' + escapeHTML(session.session_key) +
      ' &middot; <strong>Spans:</strong> ' + spans.length + '</div>';
    html += '<div style="max-height:400px;overflow-y:auto">';
    html += spans.map(function(s, si) {
      var shortName = oc_shortSpanName(s.span_name);
      var isErr = s.span_status_code === 'STATUS_CODE_ERROR';
      var badgeClass = isErr ? ' badge-error' : '';
      var parts = [];
      if (s.model) parts.push(s.model);
      if (s.channel) parts.push(s.channel);
      if (s.input_tok || s.output_tok) parts.push(fmtNum(s.input_tok) + ' in / ' + fmtNum(s.output_tok) + ' out');
      if (s.duration_ms != null) parts.push(fmtDurMs(s.duration_ms));
      if (s.outcome) parts.push(s.outcome);
      if (isErr) parts.push('ERROR');

      var rowId = 'oc-sd-' + idx + '-' + si;
      // Build attribute detail
      var attrPairs = [];
      attrPairs.push(['span_name', s.span_name]);
      attrPairs.push(['trace_id', s.trace_id]);
      attrPairs.push(['status', s.span_status_code]);
      if (s.model) attrPairs.push(['model', s.model]);
      if (s.channel) attrPairs.push(['channel', s.channel]);
      if (s.provider) attrPairs.push(['provider', s.provider]);
      if (s.outcome) attrPairs.push(['outcome', s.outcome]);
      if (s.message_id) attrPairs.push(['messageId', s.message_id]);
      if (s.session_id) attrPairs.push(['sessionId', s.session_id]);
      if (s.input_tok) attrPairs.push(['tokens.input', s.input_tok]);
      if (s.output_tok) attrPairs.push(['tokens.output', s.output_tok]);
      if (s.cache_read) attrPairs.push(['tokens.cache_read', s.cache_read]);
      if (s.cache_write) attrPairs.push(['tokens.cache_write', s.cache_write]);
      if (s.total_tok) attrPairs.push(['tokens.total', s.total_tok]);
      if (s.duration_ms != null) attrPairs.push(['duration_ms', s.duration_ms]);
      var attrJson = '{\n' + attrPairs.map(function(p) { return '  "' + p[0] + '": ' + JSON.stringify(p[1]); }).join(',\n') + '\n}';

      return '<div class="clickable" style="font-size:12px;padding:4px 0;border-bottom:1px solid var(--border)" ' +
        'onclick="var d=document.getElementById(\x27' + rowId + '\x27);var v=d.style.display===\x27none\x27;d.style.display=v?\x27block\x27:\x27none\x27;this.querySelector(\x27.expand-arrow\x27).textContent=v?\x27\\u25BC\x27:\x27\\u25B6\x27">' +
        '<span class="expand-arrow" style="color:var(--text-muted);font-size:10px;margin-right:4px;display:inline-block;width:10px">&#9654;</span>' +
        '<span style="color:var(--text-secondary);margin-right:6px">' + escapeHTML(fmtTime(s.timestamp)) + '</span>' +
        '<span class="badge' + badgeClass + '" style="font-size:10px">' + escapeHTML(shortName) + '</span> ' +
        escapeHTML(parts.join(' \u00b7 ')) +
        '</div>' +
        '<pre id="' + rowId + '" style="display:none;font-size:11px;color:var(--text-muted);' +
        'background:var(--bg-secondary);padding:8px;margin:0 0 4px;border-radius:4px;' +
        'overflow-x:auto;white-space:pre-wrap;word-break:break-all">' +
        escapeHTML(attrJson) + '</pre>';
    }).join('');
    html += '</div>';

    // Link to filter traces by this session
    if (hasSessionKey) {
      html += '<div style="margin-top:8px">' +
        '<button class="filter-btn primary" onclick="oc_filterBySession(\'' + escapeJSString(session.session_key) + '\')">' +
        t('ui.view_in_traces') + '</button></div>';
    }

    inner.innerHTML = html;
  } catch (err) {
    var inner2 = detailRow.querySelector('.trace-detail-inner');
    inner2.innerHTML = '<div class="loading">Error: ' + escapeHTML(err.message) + '</div>';
  }
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
      return '<tr class="clickable" onclick="oc_toggleTraceDetail(this, \'' + escapeJSString(d.trace_id) + '\')">' +
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
      return '<tr class="clickable" onclick="oc_switchToTrace(\'' + escapeJSString(d.trace_id) + '\')">' +
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
        reason = t('anomaly.webhook_error');
        severity = 'badge-error';
      } else if (d.span_name === 'openclaw.session.stuck') {
        reason = t('anomaly.session_stuck');
        severity = 'badge-error';
      } else if (d.span_status_code === 'STATUS_CODE_ERROR') {
        reason = t('anomaly.span_error') + ' (' + oc_shortSpanName(d.span_name) + ')';
        severity = 'badge-error';
      } else if (d.outcome && d.outcome !== 'completed') {
        reason = t('anomaly.outcome') + ': ' + d.outcome;
        severity = 'warn';
      } else {
        reason = t('anomaly.high_token');
        severity = 'warn';
      }
      return '<div class="anomaly-item ' + severity + '" onclick="oc_switchToTrace(\'' + escapeJSString(d.trace_id) + '\')">' +
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
    oc_loadAnomalies(),
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
        ? 'oc_filterBySession(\'' + escapeJSString(d.session_key) + '\')'
        : 'oc_switchToTrace(\'' + escapeJSString(d.trace_id) + '\')';
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
      return '<div class="anomaly-item badge-error" onclick="oc_switchToTrace(\'' + escapeJSString(d.trace_id) + '\')">' +
        '<div class="anomaly-reason">' + t('anomaly.webhook_error') + '</div>' +
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
      return '<div class="anomaly-item warn" onclick="oc_switchToTrace(\'' + escapeJSString(d.trace_id) + '\')">' +
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
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, greptime_timestamp) AS t, " +
      "SUM(greptime_value) AS msg_count " +
      "FROM openclaw_message_queued_total " +
      "WHERE greptime_timestamp > NOW() - INTERVAL '" + iv + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;
    renderChart('oc-sec-channel-activity', data, [
      { label: 'Messages/' + chartBucket().replace(' ', ''), key: 'msg_count', color: '#79c0ff' },
    ], function(v) { return fmtNum(v); });
  } catch { /* table may not exist */ }
}

function oc_onTabChange(tab) {
  if (tab === 'oc-overview') oc_loadOverview();
  else if (tab === 'oc-sessions') oc_loadSessions();
  else if (tab === 'oc-traces') oc_loadTraces();
  else if (tab === 'oc-cost') oc_loadCostTab();
  else if (tab === 'oc-security') oc_loadSecurityTab();
}
