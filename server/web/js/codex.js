// codex.js — Codex view: cdx_* functions
// Depends on: core.js, chart.js

var cdxEventsPage = 0;
var cdxEventsPageSize = 20;
var cdxEventsHasNext = false;
var cdxPendingEventFocus = null;
var cdxPinnedEventTimestamp = null;

function cdx_hasMetricTable(name) {
  return Array.isArray(dataSources?.codexMetrics) && dataSources.codexMetrics.includes(name);
}

function cdx_logsFromWhere() {
  return "FROM opentelemetry_logs " +
    "WHERE timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
    "  AND scope_name LIKE 'codex_%'";
}

function cdx_metricsFromWhere(tableName) {
  return "FROM " + tableName + " " +
    "WHERE greptime_timestamp > NOW() - INTERVAL '" + intervalSQL() + "'";
}

function cdx_requestPredicate() {
  return "json_get_int(log_attributes, 'input_token_count') IS NOT NULL";
}

function cdx_eventFilterPredicate(filter) {
  if (filter === 'requests') return cdx_requestPredicate();
  if (filter === 'tools') {
    return "json_get_string(log_attributes, 'tool_name') IS NOT NULL " +
      "AND json_get_string(log_attributes, 'success') IS NOT NULL";
  }
  if (filter === 'decisions') return "json_get_string(log_attributes, 'decision') IS NOT NULL";
  return "1=1";
}

function cdx_estimatedCostExpr() {
  var modelExpr = "COALESCE(json_get_string(log_attributes, 'model'), 'unknown')";
  var inputExpr = "COALESCE(json_get_int(log_attributes, 'input_token_count'), 0)";
  var outputExpr = "COALESCE(json_get_int(log_attributes, 'output_token_count'), 0)";
  return costCaseSQL(modelExpr, inputExpr, outputExpr);
}

function cdx_resetEventsPaging() {
  cdxEventsPage = 0;
}

function cdx_onEventsFilterChange() {
  cdxPinnedEventTimestamp = null;
  cdx_resetEventsPaging();
  cdx_loadEvents();
}

function cdx_prevEventsPage() {
  if (cdxEventsPage <= 0) return;
  cdxEventsPage--;
  cdx_loadEvents(false);
}

function cdx_nextEventsPage() {
  if (!cdxEventsHasNext) return;
  cdxEventsPage++;
  cdx_loadEvents(false);
}

function cdx_updateEventsPager(resultCount) {
  var prevBtn = document.getElementById('cdx-events-prev-btn');
  var nextBtn = document.getElementById('cdx-events-next-btn');
  var info = document.getElementById('cdx-events-page-info');
  if (!prevBtn || !nextBtn || !info) return;

  prevBtn.disabled = cdxEventsPage <= 0;
  nextBtn.disabled = !cdxEventsHasNext;
  if (cdxPinnedEventTimestamp) {
    info.textContent = resultCount ? 'Focused event' : 'No focused event';
    prevBtn.disabled = true;
    nextBtn.disabled = true;
    return;
  }
  if (!resultCount) {
    info.textContent = 'No results';
    return;
  }
  var start = cdxEventsPage * cdxEventsPageSize + 1;
  var end = start + resultCount - 1;
  info.textContent = 'Page ' + (cdxEventsPage + 1) + ' · ' + start + '-' + end;
}

function cdx_parseAttrs(raw) {
  if (!raw) return {};
  if (typeof raw === 'object') return raw;
  try { return JSON.parse(raw); } catch { return {}; }
}

function cdx_eventType(row) {
  if (row.input_tok != null || row.output_tok != null) return 'api_request';
  if (row.tool_name && row.success != null) return 'tool_result';
  if (row.decision != null) return 'tool_decision';
  if (row.duration_ms != null) return 'sse_event';
  return 'event';
}

function cdx_toggleEventDetail(clickedRow, idx) {
  var prev = document.querySelector('.cdx-event-detail-row');
  if (prev) {
    var prevIdx = prev.dataset.idx;
    prev.remove();
    if (prevIdx == idx) return;
  }
  var attrs = clickedRow.dataset.attrs;
  var formatted = attrs;
  try { formatted = JSON.stringify(JSON.parse(attrs), null, 2); } catch (e) {}
  var detailRow = document.createElement('tr');
  detailRow.className = 'cdx-event-detail-row trace-detail-row';
  detailRow.dataset.idx = idx;
  detailRow.innerHTML = '<td colspan="7"><div class="trace-detail-inner">' +
    '<div class="detail-header"><h3>Event Details</h3>' +
    '<button class="close-btn" onclick="this.closest(\'.cdx-event-detail-row\').remove()">&times;</button></div>' +
    '<pre style="font-size:12px;color:var(--text-muted);overflow-x:auto;white-space:pre-wrap;word-break:break-all">' +
    escapeHTML(formatted) + '</pre></div></td>';
  clickedRow.after(detailRow);
}

function cdx_focusPendingEvent() {
  if (!cdxPendingEventFocus) return;
  var pending = cdxPendingEventFocus;
  var target = null;
  document.querySelectorAll('#cdx-events-body tr.clickable').forEach(function(row) {
    if (target) return;
    var ts = row.dataset.eventTs || '';
    var callId = row.dataset.callId || '';
    var tsMatch = ts === pending.timestamp;
    var callMatch = !pending.callId || callId === pending.callId;
    if (tsMatch && callMatch) target = row;
  });
  if (!target) return;
  var idx = Number(target.dataset.idx);
  if (!Number.isNaN(idx)) cdx_toggleEventDetail(target, idx);
  target.scrollIntoView({ behavior: 'smooth', block: 'center' });
  cdxPendingEventFocus = null;
}

async function cdx_switchToEvent(tsEnc, callIdEnc) {
  var targetTs = tsEnc || '';
  var targetCall = callIdEnc || '';
  if (!targetTs) return;

  document.querySelectorAll('#cdx-tabs .tab').forEach(function(t) { t.classList.remove('active'); });
  document.querySelectorAll('#view-codex .tab-content').forEach(function(t) { t.classList.remove('active'); });
  var tabBtn = document.querySelector('[data-cdxtab="cdx-events"]');
  if (tabBtn) tabBtn.classList.add('active');
  var tabEl = document.getElementById('tab-cdx-events');
  if (tabEl) tabEl.classList.add('active');
  var filter = document.getElementById('cdx-event-filter');
  if (filter) filter.value = 'requests';
  cdx_resetEventsPaging();
  cdxPinnedEventTimestamp = targetTs;
  cdxEventsPage = 0;

  cdxPendingEventFocus = {
    timestamp: targetTs,
    callId: targetCall || '',
  };
  await cdx_loadEvents(false);
  if (typeof updateHash === 'function') updateHash();
}

async function cdx_loadCards() {
  await loadPricing();
  var base = cdx_logsFromWhere();
  var reqPred = cdx_requestPredicate();
  var estCost = cdx_estimatedCostExpr();
  try {
    var requestSQL = "SELECT COUNT(*) AS v " + base + " AND " + reqPred;
    var latencySQL = "SELECT ROUND(AVG(duration_nano) / 1000000.0, 2) AS v " +
      "FROM opentelemetry_traces WHERE timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "  AND service_name = 'codex_cli_rs' AND span_name = 'stream_request'";
    var ttftSQL = cdx_hasMetricTable('codex_turn_ttft_duration_ms_milliseconds_sum') &&
        cdx_hasMetricTable('codex_turn_ttft_duration_ms_milliseconds_count')
      ? "SELECT ROUND(SUM(s.greptime_value) / NULLIF(SUM(c.greptime_value), 0), 2) AS v " +
        "FROM codex_turn_ttft_duration_ms_milliseconds_sum s " +
        "JOIN codex_turn_ttft_duration_ms_milliseconds_count c " +
        "ON s.model = c.model " +
        " AND s.service_version = c.service_version " +
        " AND s.greptime_timestamp = c.greptime_timestamp " +
        "WHERE s.greptime_timestamp > NOW() - INTERVAL '" + intervalSQL() + "'"
      : "SELECT NULL AS v";
    var results = await Promise.all([
      query("SELECT ROUND(SUM(" + estCost + "), 4) AS v " + base + " AND " + reqPred),
      query("SELECT SUM(COALESCE(json_get_int(log_attributes, 'input_token_count'), 0) + " +
        "COALESCE(json_get_int(log_attributes, 'output_token_count'), 0)) AS v " + base + " AND " + reqPred),
      query(requestSQL),
      query(latencySQL),
      query(ttftSQL),
    ]);
    document.getElementById('cdx-val-cost').textContent = fmtCost(rows(results[0])?.[0]?.[0]);
    document.getElementById('cdx-val-tokens').textContent = fmtNum(rows(results[1])?.[0]?.[0]);
    document.getElementById('cdx-val-requests').textContent = fmtNum(rows(results[2])?.[0]?.[0]);
    document.getElementById('cdx-val-latency').textContent = fmtDurMsPrecise(rows(results[3])?.[0]?.[0]);
    document.getElementById('cdx-val-ttft').textContent = fmtDurMsPrecise(rows(results[4])?.[0]?.[0]);
  } catch (err) {
    var banner = document.getElementById('error-banner');
    banner.style.display = 'block';
    banner.textContent = 'Codex metrics error: ' + err.message;
  }
}

async function cdx_loadOverview() {
  await Promise.all([
    cdx_loadTokenChart(),
    cdx_loadLatencyChart(),
    cdx_loadTurnStartupChart(),
    cdx_loadSpanDistribution(),
    cdx_loadRequestOutcome(),
    cdx_loadToolPerformance(),
    cdx_loadActivityHeatmap(),
    cdx_loadMetricsExplorer(),
  ]);
}

async function cdx_loadActivityHeatmap() {
  var el = document.getElementById('cdx-activity-heatmap');
  if (!el) return;
  var cfg = heatmapConfig();
  try {
    var res = await query(
      "SELECT date_bin('" + cfg.bucket + "'::INTERVAL, timestamp) AS t, COUNT(*) AS cnt " +
      cdx_logsFromWhere() + " AND " + cdx_requestPredicate() +
      " AND timestamp > NOW() - INTERVAL '" + cfg.interval + "' " +
      "GROUP BY t ORDER BY t"
    );
    renderHeatmap('cdx-activity-heatmap', rowsToObjects(res));
  } catch {
    el.innerHTML = '<div class="chart-empty">' + t('error.load_activity') + '</div>';
  }
}

function cdx_loadMetricsExplorer() {
  initMetricsExplorer('cdx-metrics');
}

async function cdx_loadTokenChart() {
  var el = document.getElementById('cdx-chart-tokens');
  if (!el) return;
  try {
    var res = await query(
      "SELECT date_bin('5 minutes'::INTERVAL, timestamp) AS t, " +
      "SUM(COALESCE(json_get_int(log_attributes, 'input_token_count'), 0)) AS input_tok, " +
      "SUM(COALESCE(json_get_int(log_attributes, 'output_token_count'), 0)) AS output_tok, " +
      "SUM(COALESCE(json_get_int(log_attributes, 'cached_token_count'), 0)) AS cached_tok " +
      cdx_logsFromWhere() + " AND " + cdx_requestPredicate() + " " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      el.innerHTML = '<div class="chart-empty">No token data.</div>';
      return;
    }
    renderChart('cdx-chart-tokens', data, [
      { label: 'Input', key: 'input_tok', color: '#79c0ff' },
      { label: 'Output', key: 'output_tok', color: '#f0883e' },
      { label: 'Cached', key: 'cached_tok', color: '#3fb950' },
    ], function(v) { return fmtNum(v); });
  } catch {
    el.innerHTML = '<div class="chart-empty">Failed to load token data.</div>';
  }
}

async function cdx_loadLatencyChart() {
  var el = document.getElementById('cdx-chart-latency');
  if (!el) return;
  try {
    var res = await query(
      "SELECT date_bin('5 minutes'::INTERVAL, timestamp) AS t, " +
      "ROUND(APPROX_PERCENTILE_CONT(duration_nano / 1000000.0, 0.50), 0) AS p50_ms, " +
      "ROUND(APPROX_PERCENTILE_CONT(duration_nano / 1000000.0, 0.95), 0) AS p95_ms " +
      "FROM opentelemetry_traces " +
      "WHERE timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "  AND service_name = 'codex_cli_rs' AND span_name = 'stream_request' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (data.length) {
      renderChart('cdx-chart-latency', data, [
        { label: 'P50', key: 'p50_ms', color: '#3fb950' },
        { label: 'P95', key: 'p95_ms', color: '#d2a8ff' },
      ], function(v) { return fmtDurMs(v); });
      return;
    }
  } catch { /* ignore */ }

  if (!cdx_hasMetricTable('codex_websocket_request_duration_ms_milliseconds_sum') ||
      !cdx_hasMetricTable('codex_websocket_request_duration_ms_milliseconds_count')) {
    return;
  }
  try {
    var metricRes = await query(
      "SELECT durations.t, " +
      "ROUND(durations.total_ms / NULLIF(counts.cnt, 0), 2) AS avg_ms " +
      "FROM (" +
      "  SELECT date_bin('5 minutes'::INTERVAL, greptime_timestamp) AS t, SUM(greptime_value) AS total_ms " +
      "  " + cdx_metricsFromWhere('codex_websocket_request_duration_ms_milliseconds_sum') + " " +
      "  GROUP BY t" +
      ") durations " +
      "JOIN (" +
      "  SELECT date_bin('5 minutes'::INTERVAL, greptime_timestamp) AS t, SUM(greptime_value) AS cnt " +
      "  " + cdx_metricsFromWhere('codex_websocket_request_duration_ms_milliseconds_count') + " " +
      "  GROUP BY t" +
      ") counts ON durations.t = counts.t " +
      "ORDER BY durations.t"
    );
    var metricData = rowsToObjects(metricRes);
    if (!metricData.length) {
      el.innerHTML = '<div class="chart-empty">No latency data.</div>';
      return;
    }
    renderChart('cdx-chart-latency', metricData, [
      { label: 'Avg', key: 'avg_ms', color: '#3fb950' },
    ], function(v) { return fmtDurMs(v); });
  } catch {
    el.innerHTML = '<div class="chart-empty">Failed to load latency data.</div>';
  }
}

async function cdx_loadTurnStartupChart() {
  var el = document.getElementById('cdx-chart-turn-startup');
  if (!el) return;
  if (!cdx_hasMetricTable('codex_turn_ttft_duration_ms_milliseconds_sum') ||
      !cdx_hasMetricTable('codex_turn_ttft_duration_ms_milliseconds_count') ||
      !cdx_hasMetricTable('codex_turn_ttfm_duration_ms_milliseconds_sum') ||
      !cdx_hasMetricTable('codex_turn_ttfm_duration_ms_milliseconds_count')) {
    el.innerHTML = '<div class="chart-empty">No TTFT/TTFM metric data.</div>';
    return;
  }
  try {
    var results = await Promise.all([
      query(
        "SELECT date_bin('5 minutes'::INTERVAL, greptime_timestamp) AS t, SUM(greptime_value) AS total_ms " +
        cdx_metricsFromWhere('codex_turn_ttft_duration_ms_milliseconds_sum') + " " +
        "GROUP BY t ORDER BY t"
      ),
      query(
        "SELECT date_bin('5 minutes'::INTERVAL, greptime_timestamp) AS t, SUM(greptime_value) AS cnt " +
        cdx_metricsFromWhere('codex_turn_ttft_duration_ms_milliseconds_count') + " " +
        "GROUP BY t ORDER BY t"
      ),
      query(
        "SELECT date_bin('5 minutes'::INTERVAL, greptime_timestamp) AS t, SUM(greptime_value) AS total_ms " +
        cdx_metricsFromWhere('codex_turn_ttfm_duration_ms_milliseconds_sum') + " " +
        "GROUP BY t ORDER BY t"
      ),
      query(
        "SELECT date_bin('5 minutes'::INTERVAL, greptime_timestamp) AS t, SUM(greptime_value) AS cnt " +
        cdx_metricsFromWhere('codex_turn_ttfm_duration_ms_milliseconds_count') + " " +
        "GROUP BY t ORDER BY t"
      ),
    ]);
    var byTime = {};
    rowsToObjects(results[0]).forEach(function(row) {
      var key = String(row.t);
      byTime[key] = byTime[key] || { t: row.t };
      byTime[key].ttft_total = Number(row.total_ms) || 0;
    });
    rowsToObjects(results[1]).forEach(function(row) {
      var key = String(row.t);
      byTime[key] = byTime[key] || { t: row.t };
      byTime[key].ttft_cnt = Number(row.cnt) || 0;
    });
    rowsToObjects(results[2]).forEach(function(row) {
      var key = String(row.t);
      byTime[key] = byTime[key] || { t: row.t };
      byTime[key].ttfm_total = Number(row.total_ms) || 0;
    });
    rowsToObjects(results[3]).forEach(function(row) {
      var key = String(row.t);
      byTime[key] = byTime[key] || { t: row.t };
      byTime[key].ttfm_cnt = Number(row.cnt) || 0;
    });
    var data = Object.keys(byTime).sort().map(function(key) {
      var row = byTime[key];
      return {
        t: row.t,
        ttft_ms: row.ttft_cnt ? row.ttft_total / row.ttft_cnt : null,
        ttfm_ms: row.ttfm_cnt ? row.ttfm_total / row.ttfm_cnt : null,
      };
    }).filter(function(row) { return row.ttft_ms != null || row.ttfm_ms != null; });
    if (!data.length) {
      el.innerHTML = '<div class="chart-empty">No TTFT/TTFM metric data.</div>';
      return;
    }
    renderChart('cdx-chart-turn-startup', data, [
      { label: 'TTFT', key: 'ttft_ms', color: '#58a6ff' },
      { label: 'TTFM', key: 'ttfm_ms', color: '#f0883e' },
    ], function(v) { return fmtDurMs(v); });
  } catch {
    el.innerHTML = '<div class="chart-empty">Failed to load TTFT/TTFM metrics.</div>';
  }
}

async function cdx_loadSpanDistribution() {
  var el = document.getElementById('cdx-span-dist');
  if (!el) return;
  try {
    var tc = getThemeColors();
    var res = await query(
      "SELECT span_name, COUNT(*) AS cnt " +
      "FROM opentelemetry_traces " +
      "WHERE timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "  AND service_name = 'codex_cli_rs' " +
      "GROUP BY span_name ORDER BY cnt DESC LIMIT 12"
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      el.innerHTML = '<div class="chart-empty">No Codex span data.</div>';
      return;
    }
    var maxCnt = Math.max.apply(null, data.map(function(d) { return Number(d.cnt) || 0; }));
    el.innerHTML = data.map(function(d) {
      var cnt = Number(d.cnt) || 0;
      var pct = maxCnt > 0 ? (cnt / maxCnt * 100) : 0;
      return '<div class="bar-row">' +
        '<div class="bar-label" title="' + escapeHTML(d.span_name) + '">' + escapeHTML(d.span_name || 'unknown') + '</div>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + pct + '%;background:' + tc.blue + '"></div></div>' +
        '<div class="bar-value">' + fmtNum(cnt) + '</div></div>';
    }).join('');
  } catch {
    el.innerHTML = '<div class="chart-empty">Failed to load span distribution.</div>';
  }
}

async function cdx_loadRequestOutcome() {
  var el = document.getElementById('cdx-request-outcome');
  if (!el) return;
  if (!cdx_hasMetricTable('codex_websocket_request_total')) {
    el.innerHTML = '<div class="chart-empty">No request outcome metrics.</div>';
    return;
  }
  try {
    var tc = getThemeColors();
    var res = await query(
      "SELECT COALESCE(success, 'unknown') AS success, SUM(greptime_value) AS cnt " +
      cdx_metricsFromWhere('codex_websocket_request_total') + " " +
      "GROUP BY success ORDER BY cnt DESC"
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      el.innerHTML = '<div class="chart-empty">No request outcome metrics.</div>';
      return;
    }
    var maxCnt = Math.max.apply(null, data.map(function(d) { return Number(d.cnt) || 0; }));
    el.innerHTML = data.map(function(d) {
      var cnt = Number(d.cnt) || 0;
      var pct = maxCnt > 0 ? (cnt / maxCnt * 100) : 0;
      var isSuccess = String(d.success) === 'true';
      return '<div class="bar-row">' +
        '<div class="bar-label">' + escapeHTML(isSuccess ? 'success' : d.success || 'unknown') + '</div>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + pct + '%;background:' + (isSuccess ? tc.green : tc.orange) + '"></div></div>' +
        '<div class="bar-value">' + fmtNum(cnt) + '</div></div>';
    }).join('');
  } catch {
    el.innerHTML = '<div class="chart-empty">Failed to load request outcome metrics.</div>';
  }
}

async function cdx_loadToolPerformance() {
  try {
    var res;
    if (cdx_hasMetricTable('codex_tool_call_total') &&
        cdx_hasMetricTable('codex_tool_call_duration_ms_milliseconds_sum') &&
        cdx_hasMetricTable('codex_tool_call_duration_ms_milliseconds_count')) {
      res = await query(
        "SELECT totals.tool_name, totals.cnt, totals.ok_cnt, " +
        "ROUND(durations.total_ms / NULLIF(duration_counts.cnt, 0), 2) AS avg_ms " +
        "FROM (" +
        "  SELECT tool AS tool_name, " +
        "         SUM(greptime_value) AS cnt, " +
        "         SUM(CASE WHEN success = 'true' THEN greptime_value ELSE 0 END) AS ok_cnt " +
        "  " + cdx_metricsFromWhere('codex_tool_call_total') + " " +
        "  GROUP BY tool" +
        ") totals " +
        "LEFT JOIN (" +
        "  SELECT tool AS tool_name, SUM(greptime_value) AS total_ms " +
        "  " + cdx_metricsFromWhere('codex_tool_call_duration_ms_milliseconds_sum') + " " +
        "  GROUP BY tool" +
        ") durations ON totals.tool_name = durations.tool_name " +
        "LEFT JOIN (" +
        "  SELECT tool AS tool_name, SUM(greptime_value) AS cnt " +
        "  " + cdx_metricsFromWhere('codex_tool_call_duration_ms_milliseconds_count') + " " +
        "  GROUP BY tool" +
        ") duration_counts ON totals.tool_name = duration_counts.tool_name " +
        "ORDER BY totals.cnt DESC LIMIT 15"
      );
    } else {
      res = await query(
        "SELECT json_get_string(log_attributes, 'tool_name') AS tool_name, " +
        "COUNT(*) AS cnt, " +
        "SUM(CASE WHEN json_get_string(log_attributes, 'success') = 'true' THEN 1 ELSE 0 END) AS ok_cnt, " +
        "ROUND(AVG(json_get_float(log_attributes, 'duration_ms')), 2) AS avg_ms " +
        cdx_logsFromWhere() + " " +
        "AND json_get_string(log_attributes, 'tool_name') IS NOT NULL " +
        "AND json_get_string(log_attributes, 'success') IS NOT NULL " +
        "GROUP BY tool_name ORDER BY cnt DESC LIMIT 15"
      );
    }
    var data = rowsToObjects(res);
    var tbody = document.getElementById('cdx-tool-perf-body');
    if (!data.length) {
      tbody.innerHTML = '<tr><td colspan="4" class="loading">No tool data.</td></tr>';
      return;
    }
    tbody.innerHTML = data.map(function(d) {
      var cnt = Number(d.cnt) || 0;
      var okCnt = Number(d.ok_cnt) || 0;
      var rate = cnt > 0 ? (okCnt / cnt * 100).toFixed(1) + '%' : '0%';
      return '<tr>' +
        '<td>' + escapeHTML(d.tool_name || 'unknown') + '</td>' +
        '<td>' + fmtNum(cnt) + '</td>' +
        '<td>' + rate + '</td>' +
        '<td>' + fmtDurMs(d.avg_ms) + '</td>' +
        '</tr>';
    }).join('');
  } catch {
    document.getElementById('cdx-tool-perf-body').innerHTML =
      '<tr><td colspan="4" class="loading">Failed to load tool data.</td></tr>';
  }
}

async function cdx_loadEvents() {
  var filter = document.getElementById('cdx-event-filter')?.value || '';
  var predicate = cdx_eventFilterPredicate(filter);
  if (cdxPinnedEventTimestamp) {
    predicate += " AND timestamp = " + cdxPinnedEventTimestamp;
  }
  var limit = cdxEventsPageSize + 1;
  var offset = cdxPinnedEventTimestamp ? 0 : cdxEventsPage * cdxEventsPageSize;
  try {
    var res = await query(
      "SELECT timestamp, trace_id, span_id, severity_text, " +
      "json_get_string(log_attributes, 'model') AS model, " +
      "json_get_int(log_attributes, 'input_token_count') AS input_tok, " +
      "json_get_int(log_attributes, 'output_token_count') AS output_tok, " +
      "json_get_int(log_attributes, 'cached_token_count') AS cached_tok, " +
      "json_get_float(log_attributes, 'duration_ms') AS duration_ms, " +
      "json_get_string(log_attributes, 'tool_name') AS tool_name, " +
      "json_get_string(log_attributes, 'success') AS success, " +
      "json_get_string(log_attributes, 'decision') AS decision, " +
      "json_get_string(log_attributes, 'call_id') AS call_id, " +
      "log_attributes " +
      cdx_logsFromWhere() + " AND " + predicate + " " +
      "ORDER BY timestamp DESC LIMIT " + limit + " OFFSET " + offset
    );
    var allRows = rowsToObjects(res);
    cdxEventsHasNext = allRows.length > cdxEventsPageSize;
    var data = cdxEventsHasNext ? allRows.slice(0, cdxEventsPageSize) : allRows;
    var tbody = document.getElementById('cdx-events-body');
    if (!data.length) {
      if (cdxEventsPage > 0) {
        cdxEventsPage--;
        return cdx_loadEvents();
      }
      tbody.innerHTML = '<tr><td colspan="7" class="loading">No recent Codex events.</td></tr>';
      cdx_updateEventsPager(0);
      return;
    }

    tbody.innerHTML = data.map(function(d, i) {
      var typ = cdx_eventType(d);
      var totalTokens = (Number(d.input_tok) || 0) + (Number(d.output_tok) || 0);
      var tokText = totalTokens > 0 ? fmtNum(totalTokens) : '—';
      var isErr = d.success === 'false';
      var isWarn = !isErr && d.decision && d.decision.toLowerCase().indexOf('deny') >= 0;
      var badge = isErr ? 'badge-error' : (isWarn ? 'badge-warn' : 'badge-ok');
      var label = isErr ? 'ERROR' : (isWarn ? 'WARN' : 'OK');
      var attrsStr = typeof d.log_attributes === 'string' ? d.log_attributes : JSON.stringify(d.log_attributes || {});
      return '<tr class="clickable" onclick="cdx_toggleEventDetail(this, ' + i + ')" ' +
        'data-idx="' + i + '" ' +
        'data-event-ts="' + escapeHTML(String(d.timestamp || '')) + '" ' +
        'data-call-id="' + escapeHTML(d.call_id || '') + '" ' +
        'data-attrs="' + escapeHTML(attrsStr) + '">' +
        '<td>' + fmtTime(d.timestamp) + '</td>' +
        '<td>' + escapeHTML(typ) + '</td>' +
        '<td>' + escapeHTML(d.model || '—') + '</td>' +
        '<td>' + tokText + '</td>' +
        '<td>' + escapeHTML(d.tool_name || '—') + '</td>' +
        '<td>' + fmtDurMs(d.duration_ms) + '</td>' +
        '<td><span class="badge ' + badge + '">' + label + '</span></td>' +
        '</tr>';
    }).join('');

    cdx_updateEventsPager(data.length);
    cdx_focusPendingEvent();
  } catch (err) {
    cdxEventsHasNext = false;
    cdx_updateEventsPager(0);
    document.getElementById('cdx-events-body').innerHTML =
      '<tr><td colspan="7" class="loading">Error: ' + escapeHTML(err.message) + '</td></tr>';
  }
}

async function cdx_loadCostTab() {
  await loadPricing();
  await Promise.all([
    cdx_loadCostByModel(),
    cdx_loadExpensiveRequests(),
    cdx_loadCacheEfficiency(),
    cdx_loadModelComparison(),
  ]);
}

async function cdx_loadCostByModel() {
  try {
    var tc = getThemeColors();
    var estCost = cdx_estimatedCostExpr();
    var res = await query(
      "SELECT COALESCE(json_get_string(log_attributes, 'model'), 'unknown') AS model, " +
      "SUM(" + estCost + ") AS total_cost " +
      cdx_logsFromWhere() + " AND " + cdx_requestPredicate() + " " +
      "GROUP BY model ORDER BY total_cost DESC"
    );
    var data = rowsToObjects(res);
    var el = document.getElementById('cdx-cost-by-model');
    if (!data.length) {
      el.innerHTML = '<div class="chart-empty">No cost data.</div>';
      return;
    }
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

async function cdx_loadExpensiveRequests() {
  try {
    var estCost = cdx_estimatedCostExpr();
    var res = await query(
      "SELECT timestamp, " +
      "COALESCE(json_get_string(log_attributes, 'model'), 'unknown') AS model, " +
      "COALESCE(json_get_int(log_attributes, 'input_token_count'), 0) AS input_tok, " +
      "COALESCE(json_get_int(log_attributes, 'output_token_count'), 0) AS output_tok, " +
      "COALESCE(json_get_int(log_attributes, 'cached_token_count'), 0) AS cached_tok, " +
      "json_get_float(log_attributes, 'duration_ms') AS duration_ms, " +
      "json_get_string(log_attributes, 'call_id') AS call_id, " +
      estCost + " AS est_cost " +
      cdx_logsFromWhere() + " AND " + cdx_requestPredicate() + " " +
      "ORDER BY est_cost DESC LIMIT 10"
    );
    var data = rowsToObjects(res);
    var tbody = document.getElementById('cdx-expensive-body');
    if (!data.length) {
      tbody.innerHTML = '<tr><td colspan="6" class="loading">No request data.</td></tr>';
      return;
    }
    tbody.innerHTML = data.map(function(d) {
      var tsArg = escapeJSString(String(d.timestamp || ''));
      var callArg = escapeJSString(String(d.call_id || ''));
      return '<tr class="clickable" onclick="cdx_switchToEvent(\'' + tsArg + '\',\'' + callArg + '\')">' +
        '<td>' + fmtTime(d.timestamp) + '</td>' +
        '<td>' + escapeHTML(d.model || 'unknown') + '</td>' +
        '<td>' + fmtNum(d.input_tok) + '</td>' +
        '<td>' + fmtNum(d.output_tok) + '</td>' +
        '<td>' + fmtCost(d.est_cost) + '</td>' +
        '<td>' + fmtDurMs(d.duration_ms) + '</td>' +
        '</tr>';
    }).join('');
  } catch { /* ignore */ }
}

async function cdx_loadCacheEfficiency() {
  try {
    var tc = getThemeColors();
    var res = await query(
      "SELECT COALESCE(json_get_string(log_attributes, 'model'), 'unknown') AS model, " +
      "SUM(COALESCE(json_get_int(log_attributes, 'input_token_count'), 0)) AS input_tok, " +
      "SUM(COALESCE(json_get_int(log_attributes, 'cached_token_count'), 0)) AS cached_tok " +
      cdx_logsFromWhere() + " AND " + cdx_requestPredicate() + " " +
      "GROUP BY model ORDER BY input_tok DESC"
    );
    var data = rowsToObjects(res);
    var el = document.getElementById('cdx-cache-chart');
    if (!data.length) {
      el.innerHTML = '<div class="chart-empty">No cache data.</div>';
      return;
    }
    el.innerHTML = data.map(function(d) {
      var input = Number(d.input_tok) || 0;
      var cached = Number(d.cached_tok) || 0;
      var hitRate = input > 0 ? (cached / input * 100).toFixed(1) : '0.0';
      return '<div class="bar-row">' +
        '<div class="bar-label">' + escapeHTML(d.model || 'unknown') + '</div>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + hitRate + '%;background:' + tc.green + '"></div></div>' +
        '<div class="bar-value">' + hitRate + '% hit (' + fmtNum(cached) + '/' + fmtNum(input) + ')</div>' +
        '</div>';
    }).join('');
  } catch { /* ignore */ }
}

async function cdx_loadModelComparison() {
  try {
    var data;
    if (cdx_hasMetricTable('codex_websocket_request_total') &&
        cdx_hasMetricTable('codex_websocket_request_duration_ms_milliseconds_sum') &&
        cdx_hasMetricTable('codex_websocket_request_duration_ms_milliseconds_count')) {
      var estCost = cdx_estimatedCostExpr();
      var results = await Promise.all([
        query(
          "SELECT req.model, req.reqs, " +
          "ROUND(durations.total_ms / NULLIF(duration_counts.cnt, 0), 2) AS avg_ms " +
          "FROM (" +
          "  SELECT model, SUM(greptime_value) AS reqs " +
          "  " + cdx_metricsFromWhere('codex_websocket_request_total') + " " +
          "  GROUP BY model" +
          ") req " +
          "LEFT JOIN (" +
          "  SELECT model, SUM(greptime_value) AS total_ms " +
          "  " + cdx_metricsFromWhere('codex_websocket_request_duration_ms_milliseconds_sum') + " " +
          "  GROUP BY model" +
          ") durations ON req.model = durations.model " +
          "LEFT JOIN (" +
          "  SELECT model, SUM(greptime_value) AS cnt " +
          "  " + cdx_metricsFromWhere('codex_websocket_request_duration_ms_milliseconds_count') + " " +
          "  GROUP BY model" +
          ") duration_counts ON req.model = duration_counts.model " +
          "ORDER BY req.reqs DESC"
        ),
        query(
          "SELECT COALESCE(json_get_string(log_attributes, 'model'), 'unknown') AS model, " +
          "ROUND(AVG(" + estCost + "), 6) AS avg_cost " +
          cdx_logsFromWhere() + " AND " + cdx_requestPredicate() + " " +
          "GROUP BY model"
        ),
      ]);
      var metricRows = rowsToObjects(results[0]);
      var costRows = rowsToObjects(results[1]);
      var costByModel = {};
      costRows.forEach(function(row) {
        costByModel[row.model || 'unknown'] = row.avg_cost;
      });
      data = metricRows.map(function(row) {
        return {
          model: row.model,
          reqs: row.reqs,
          avg_ms: row.avg_ms,
          avg_cost: costByModel[row.model || 'unknown'],
        };
      });
    } else {
      var fallbackCost = cdx_estimatedCostExpr();
      var res = await query(
        "SELECT COALESCE(json_get_string(log_attributes, 'model'), 'unknown') AS model, " +
        "COUNT(*) AS reqs, " +
        "ROUND(AVG(json_get_float(log_attributes, 'duration_ms')), 2) AS avg_ms, " +
        "ROUND(AVG(" + fallbackCost + "), 6) AS avg_cost " +
        cdx_logsFromWhere() + " AND " + cdx_requestPredicate() + " " +
        "GROUP BY model ORDER BY reqs DESC"
      );
      data = rowsToObjects(res);
    }
    var tbody = document.getElementById('cdx-model-compare-body');
    if (!data.length) {
      tbody.innerHTML = '<tr><td colspan="4" class="loading">No model data.</td></tr>';
      return;
    }
    tbody.innerHTML = data.map(function(d) {
      return '<tr>' +
        '<td>' + escapeHTML(d.model || 'unknown') + '</td>' +
        '<td>' + fmtNum(d.reqs) + '</td>' +
        '<td>' + fmtDurMs(d.avg_ms) + '</td>' +
        '<td>' + fmtCost(d.avg_cost) + '</td>' +
        '</tr>';
    }).join('');
  } catch { /* ignore */ }
}

function cdx_onTabChange(tab) {
  if (tab === 'cdx-overview') cdx_loadOverview();
  else if (tab === 'cdx-events') cdx_loadEvents();
  else if (tab === 'cdx-cost') cdx_loadCostTab();
}
