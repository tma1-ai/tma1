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
    info.textContent = resultCount ? t('ui.focused_event') : t('ui.no_focused_event');
    prevBtn.disabled = true;
    nextBtn.disabled = true;
    return;
  }
  if (!resultCount) {
    info.textContent = t('pager.no_results');
    return;
  }
  var start = cdxEventsPage * cdxEventsPageSize + 1;
  var end = start + resultCount - 1;
  info.textContent = t('pager.page') + ' ' + (cdxEventsPage + 1) + ' \u00b7 ' + start + '-' + end;
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
    if (String(prevIdx) === String(idx)) return;
  }
  var attrs = clickedRow.dataset.attrs;
  var formatted = attrs;
  try { formatted = JSON.stringify(JSON.parse(attrs), null, 2); } catch (_) { /* ignore parse error */ }
  var detailRow = document.createElement('tr');
  detailRow.className = 'cdx-event-detail-row trace-detail-row';
  detailRow.dataset.idx = idx;
  detailRow.innerHTML = '<td colspan="7"><div class="trace-detail-inner">' +
    '<div class="detail-header"><h3>' + t('detail.event_details') + '</h3>' +
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
  var targetCall = callIdEnc || '';
  var targetTs = tsEnc ? decodeURIComponent(tsEnc) : '';
  if (!targetTs && !targetCall) return;

  document.querySelectorAll('#cdx-tabs .tab').forEach(function(t) { t.classList.remove('active'); });
  document.querySelectorAll('#view-codex .tab-content').forEach(function(t) { t.classList.remove('active'); });
  var tabBtn = document.querySelector('[data-cdxtab="cdx-sessions"]');
  if (tabBtn) tabBtn.classList.add('active');
  var tabEl = document.getElementById('tab-cdx-sessions');
  if (tabEl) tabEl.classList.add('active');

  var filterInput = document.getElementById('cdx-session-filter');
  if (filterInput) filterInput.value = targetCall || '';
  cdxSessionsPage = 0;
  await cdx_loadSessions();
  if (typeof updateHash === 'function') updateHash();

  // Find the turn containing the target timestamp and expand it
  if (!cdxSessionsData.length) return;
  var targetMs = targetTs ? tsToMs(targetTs) : 0;
  var matchIdx = -1;
  if (targetCall) {
    for (var i = 0; i < cdxSessionsData.length; i++) {
      if (cdxSessionsData[i].turnId === targetCall) { matchIdx = i; break; }
    }
  }
  if (matchIdx < 0 && targetMs) {
    for (var j = 0; j < cdxSessionsData.length; j++) {
      var g = cdxSessionsData[j];
      var evts = g.events;
      var first = tsToMs(evts[evts.length - 1]?.timestamp || g.timestamp);
      var last = tsToMs(evts[0]?.timestamp || g.timestamp);
      if (targetMs >= first && targetMs <= last) { matchIdx = j; break; }
    }
  }
  if (matchIdx < 0) return; // target not found in loaded data; don't expand an unrelated turn

  var page = Math.floor(matchIdx / cdxSessionsPageSize);
  if (page !== cdxSessionsPage) { cdxSessionsPage = page; cdx_renderSessions(); }
  var row = document.querySelector('#cdx-sessions-body tr:nth-child(' + (matchIdx - page * cdxSessionsPageSize + 1) + ')');
  if (row) {
    cdx_toggleSessionDetail(matchIdx, row);
    if (targetMs) {
      setTimeout(function() {
        var detail = document.querySelector('.cdx-session-detail-row');
        if (!detail) return;
        var divs = detail.querySelectorAll('.clickable');
        var best = null, bestDiff = Infinity;
        divs.forEach(function(div) {
          var spans = div.querySelectorAll('span');
          for (var s = 0; s < spans.length; s++) {
            var ts = Date.parse(spans[s].textContent);
            if (!isNaN(ts)) { var diff = Math.abs(ts - targetMs); if (diff < bestDiff) { bestDiff = diff; best = div; } break; }
          }
        });
        if (best) {
          best.style.background = 'var(--bg-secondary)';
          best.scrollIntoView({ behavior: 'smooth', block: 'center' });
        }
      }, 100);
    } else {
      row.scrollIntoView({ behavior: 'smooth', block: 'center' });
    }
  }
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
    var reqCount = Number(rows(results[2])?.[0]?.[0]) || 0;
    document.getElementById('cdx-val-cost').textContent = fmtCost(rows(results[0])?.[0]?.[0]);
    document.getElementById('cdx-val-tokens').textContent = fmtNum(rows(results[1])?.[0]?.[0]);
    document.getElementById('cdx-val-requests').textContent = fmtNum(reqCount);
    document.getElementById('cdx-val-latency').textContent = fmtDurMsPrecise(rows(results[3])?.[0]?.[0]);
    document.getElementById('cdx-val-ttft').textContent = fmtDurMsPrecise(rows(results[4])?.[0]?.[0]);
    return reqCount > 0;
  } catch (err) {
    var banner = document.getElementById('error-banner');
    banner.style.display = 'block';
    banner.textContent = 'Codex metrics error: ' + err.message;
    return false;
  }
}

async function cdx_loadOverview() {
  await Promise.all([
    cdx_loadTokenChart(),
    cdx_loadLatencyChart(),
    cdx_loadTurnStartupChart(),
    cdx_loadSpanDistribution(),
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
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, timestamp) AS t, " +
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
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, timestamp) AS t, " +
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
      "  SELECT date_bin('" + chartBucket() + "'::INTERVAL, greptime_timestamp) AS t, SUM(greptime_value) AS total_ms " +
      "  " + cdx_metricsFromWhere('codex_websocket_request_duration_ms_milliseconds_sum') + " " +
      "  GROUP BY t" +
      ") durations " +
      "JOIN (" +
      "  SELECT date_bin('" + chartBucket() + "'::INTERVAL, greptime_timestamp) AS t, SUM(greptime_value) AS cnt " +
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
        "SELECT date_bin('" + chartBucket() + "'::INTERVAL, greptime_timestamp) AS t, SUM(greptime_value) AS total_ms " +
        cdx_metricsFromWhere('codex_turn_ttft_duration_ms_milliseconds_sum') + " " +
        "GROUP BY t ORDER BY t"
      ),
      query(
        "SELECT date_bin('" + chartBucket() + "'::INTERVAL, greptime_timestamp) AS t, SUM(greptime_value) AS cnt " +
        cdx_metricsFromWhere('codex_turn_ttft_duration_ms_milliseconds_count') + " " +
        "GROUP BY t ORDER BY t"
      ),
      query(
        "SELECT date_bin('" + chartBucket() + "'::INTERVAL, greptime_timestamp) AS t, SUM(greptime_value) AS total_ms " +
        cdx_metricsFromWhere('codex_turn_ttfm_duration_ms_milliseconds_sum') + " " +
        "GROUP BY t ORDER BY t"
      ),
      query(
        "SELECT date_bin('" + chartBucket() + "'::INTERVAL, greptime_timestamp) AS t, SUM(greptime_value) AS cnt " +
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

var cdxSessionsPage = 0;
var cdxSessionsPageSize = 15;
var cdxSessionsHasNext = false;
var cdxSessionsData = [];

function cdx_prevSessionsPage() {
  if (cdxSessionsPage <= 0) return;
  cdxSessionsPage--;
  cdx_renderSessions();
}

function cdx_nextSessionsPage() {
  if (!cdxSessionsHasNext) return;
  cdxSessionsPage++;
  cdx_renderSessions();
}

function cdx_updateSessionsPager(shownCount) {
  var prevBtn = document.getElementById('cdx-sessions-prev-btn');
  var nextBtn = document.getElementById('cdx-sessions-next-btn');
  var info = document.getElementById('cdx-sessions-page-info');
  if (!prevBtn || !nextBtn || !info) return;

  var total = cdxSessionsData.length;
  cdxSessionsHasNext = (cdxSessionsPage + 1) * cdxSessionsPageSize < total;
  prevBtn.disabled = cdxSessionsPage <= 0;
  nextBtn.disabled = !cdxSessionsHasNext;
  if (!shownCount) {
    info.textContent = t('pager.no_results');
    return;
  }
  var start = cdxSessionsPage * cdxSessionsPageSize + 1;
  var end = start + shownCount - 1;
  info.textContent = t('pager.page') + ' ' + (cdxSessionsPage + 1) + ' \u00b7 ' + start + '-' + end + ' ' + t('pager.of') + ' ' + total;
}

async function cdx_loadSessions() {
  var tbody = document.getElementById('cdx-sessions-body');
  if (!tbody) return;
  tbody.innerHTML = '<tr><td colspan="7" class="loading">' + t('empty.loading') + '</td></tr>';
  var filterText = (document.getElementById('cdx-session-filter')?.value || '').trim();

  try {
    await loadPricing();
    var estCost = cdx_estimatedCostExpr();
    // Only fetch meaningful events (api_request, tool_result, tool_decision).
    // Excludes SSE streaming noise that has only duration_ms.
    var meaningfulPred =
      "(json_get_int(log_attributes, 'input_token_count') IS NOT NULL " +
      "OR json_get_string(log_attributes, 'tool_name') IS NOT NULL " +
      "OR json_get_string(log_attributes, 'decision') IS NOT NULL)";
    var rowLimit = sessionQueryLimit();
    var res = await query(
      "SELECT timestamp, " +
      "json_get_string(log_attributes, 'model') AS model, " +
      "json_get_int(log_attributes, 'input_token_count') AS input_tok, " +
      "json_get_int(log_attributes, 'output_token_count') AS output_tok, " +
      "json_get_int(log_attributes, 'cached_token_count') AS cached_tok, " +
      "json_get_float(log_attributes, 'duration_ms') AS duration_ms, " +
      "json_get_string(log_attributes, 'tool_name') AS tool_name, " +
      "json_get_string(log_attributes, 'success') AS success, " +
      "json_get_string(log_attributes, 'call_id') AS call_id, " +
      "json_get_string(log_attributes, 'decision') AS decision, " +
      estCost + " AS est_cost, " +
      "log_attributes " +
      cdx_logsFromWhere() + " AND " + meaningfulPred + " " +
      "ORDER BY timestamp DESC LIMIT " + rowLimit
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      tbody.innerHTML = '<tr><td colspan="7" class="loading">' + t('empty.no_events') + '</td></tr>';
      cdx_updateSessionsPager(0);
      return;
    }

    // Group by call_id; events without call_id use time-gap grouping
    var groups = [];
    var byCallId = {};
    var noCallId = [];
    data.forEach(function(d) {
      if (d.call_id) {
        if (!byCallId[d.call_id]) byCallId[d.call_id] = [];
        byCallId[d.call_id].push(d);
      } else {
        noCallId.push(d);
      }
    });

    // Build groups from call_id
    Object.keys(byCallId).forEach(function(callId) {
      var events = byCallId[callId];
      var reqEvent = null;
      var tools = 0;
      var tokens = 0;
      var cost = 0;
      var isErr = false;
      events.forEach(function(e) {
        var typ = cdx_eventType(e);
        if (typ === 'api_request') {
          reqEvent = e;
          tokens += (Number(e.input_tok) || 0) + (Number(e.output_tok) || 0);
          cost += Number(e.est_cost) || 0;
        }
        if (e.tool_name) tools++;
        if (e.success === 'false') isErr = true;
      });
      groups.push({
        turnId: callId,
        model: reqEvent?.model || events[0]?.model || '',
        tokens: tokens,
        tools: tools,
        cost: cost,
        durationMs: reqEvent?.duration_ms,
        isErr: isErr,
        timestamp: events[events.length - 1]?.timestamp || events[0]?.timestamp,
        events: events,
      });
    });

    // Time-gap grouping for events without call_id (30min gap = new session)
    if (noCallId.length) {
      var GAP_MS = 30 * 60 * 1000;
      var current = null;
      noCallId.forEach(function(d) {
        var ts = tsToMs(d.timestamp);
        if (!current || (current.lastTs - ts > GAP_MS)) {
          current = { turnId: 'turn-' + (groups.length + 1), model: '', tokens: 0, tools: 0, cost: 0, durationMs: null, isErr: false, timestamp: d.timestamp, lastTs: ts, events: [] };
          groups.push(current);
        }
        current.lastTs = ts;
        current.events.push(d);
        var typ = cdx_eventType(d);
        if (typ === 'api_request') {
          current.model = d.model || current.model;
          current.tokens += (Number(d.input_tok) || 0) + (Number(d.output_tok) || 0);
          current.cost += Number(d.est_cost) || 0;
          if (d.duration_ms != null) current.durationMs = d.duration_ms;
        }
        if (d.tool_name) current.tools++;
        if (d.success === 'false') current.isErr = true;
      });
    }

    // Apply filter
    groups = groups.filter(function(g) {
      if (!filterText) return true;
      return g.turnId.indexOf(filterText) >= 0 || (g.model || '').indexOf(filterText) >= 0;
    });

    // Sort by timestamp descending
    groups.sort(function(a, b) { return tsToMs(b.timestamp) - tsToMs(a.timestamp); });
    cdxSessionsData = groups;
    cdxSessionsPage = 0;
    cdx_renderSessions();

    // Show truncation warning if the query hit its row limit
    var warnEl = document.getElementById('cdx-sessions-truncation');
    if (warnEl) warnEl.style.display = data.length >= rowLimit ? 'block' : 'none';
  } catch (err) {
    tbody.innerHTML = '<tr><td colspan="7" class="loading">Error: ' + escapeHTML(err.message) + '</td></tr>';
    cdx_updateSessionsPager(0);
  }
}

function cdx_renderSessions() {
  var tbody = document.getElementById('cdx-sessions-body');
  if (!tbody) return;
  var start = cdxSessionsPage * cdxSessionsPageSize;
  var page = cdxSessionsData.slice(start, start + cdxSessionsPageSize);

  if (!page.length) {
    tbody.innerHTML = '<tr><td colspan="7" class="loading">' + t('empty.no_events') + '</td></tr>';
    cdx_updateSessionsPager(0);
    return;
  }

  tbody.innerHTML = page.map(function(g, i) {
    var globalIdx = start + i;
    var shortId = g.turnId.length > 16 ? g.turnId.substring(0, 16) + '\u2026' : g.turnId;
    var tokText = g.tokens > 0 ? fmtNum(g.tokens) : '\u2014';
    var badge = g.isErr ? 'badge-error' : 'badge-ok';
    var label = g.isErr ? 'ERROR' : 'OK';
    return '<tr class="clickable" onclick="cdx_toggleSessionDetail(' + globalIdx + ', this)" title="' + escapeHTML(g.turnId) + '">' +
      '<td>' + escapeHTML(shortId) + '</td>' +
      '<td>' + escapeHTML(g.model || '\u2014') + '</td>' +
      '<td>' + tokText + '</td>' +
      '<td>' + g.tools + '</td>' +
      '<td>' + fmtCost(g.cost) + '</td>' +
      '<td>' + fmtDurMs(g.durationMs) + '</td>' +
      '<td><span class="badge ' + badge + '">' + label + '</span></td>' +
      '</tr>';
  }).join('');
  cdx_updateSessionsPager(page.length);
}

function cdx_toggleSessionDetail(groupIdx, clickedRow) {
  var prev = document.querySelector('.cdx-session-detail-row');
  if (prev) {
    var prevIdx = prev.dataset.idx;
    prev.remove();
    if (String(prevIdx) === String(groupIdx)) return;
  }

  var g = cdxSessionsData[groupIdx];
  if (!g) return;
  // Filter out SSE noise and sort chronologically
  var events = g.events.filter(function(e) {
    return cdx_eventType(e) !== 'sse_event' && cdx_eventType(e) !== 'event';
  }).sort(function(a, b) { return tsToMs(a.timestamp) - tsToMs(b.timestamp); });

  var timeline = events.map(function(e, ei) {
    var typ = cdx_eventType(e);
    var parts = [];
    if (e.model) parts.push(e.model);
    if (e.tool_name) parts.push(e.tool_name);
    if (e.input_tok || e.output_tok) {
      var tokDetail = fmtNum(Number(e.input_tok) || 0) + ' in / ' + fmtNum(Number(e.output_tok) || 0) + ' out';
      if (e.cached_tok) tokDetail += ' / ' + fmtNum(e.cached_tok) + ' cached';
      parts.push(tokDetail);
    }
    if (e.duration_ms != null) parts.push(fmtDurMs(e.duration_ms));
    if (e.decision) parts.push('decision=' + e.decision);
    if (e.success != null) parts.push(e.success === 'true' ? 'ok' : 'failed');
    if (e.error) parts.push(e.error.length > 60 ? e.error.substring(0, 60) + '\u2026' : e.error);

    var badgeClass = 'badge-ok';
    if (e.success === 'false') badgeClass = 'badge-error';
    else if (typ === 'tool_result') badgeClass = 'badge-tool';
    else if (typ === 'api_request') badgeClass = 'badge-request';
    else if (typ === 'tool_decision') badgeClass = '';

    var rowId = 'cdx-sd-' + groupIdx + '-' + ei;
    // Render raw attributes as expandable detail
    var attrJson = '';
    try {
      var raw = typeof e.log_attributes === 'string' ? JSON.parse(e.log_attributes) : (e.log_attributes || {});
      attrJson = JSON.stringify(deepParseAttrs(raw), null, 2);
    } catch (_) {
      attrJson = String(e.log_attributes || '{}');
    }

    return '<div class="clickable" style="font-size:12px;padding:4px 0;border-bottom:1px solid var(--border)" ' +
      'onclick="var d=document.getElementById(\x27' + rowId + '\x27);var v=d.style.display===\x27none\x27;d.style.display=v?\x27block\x27:\x27none\x27;this.querySelector(\x27.expand-arrow\x27).textContent=v?\x27\\u25BC\x27:\x27\\u25B6\x27">' +
      '<span class="expand-arrow" style="color:var(--text-muted);font-size:10px;margin-right:4px;display:inline-block;width:10px">&#9654;</span>' +
      '<span style="color:var(--text-secondary);margin-right:6px">' + escapeHTML(fmtTime(e.timestamp)) + '</span>' +
      '<span class="badge ' + badgeClass + '" style="font-size:10px">' + escapeHTML(typ.toUpperCase()) + '</span> ' +
      escapeHTML(parts.join(' \u00b7 ')) +
      '</div>' +
      '<pre id="' + rowId + '" style="display:none;font-size:11px;color:var(--text-muted);' +
      'background:var(--bg-secondary);padding:8px;margin:0 0 4px;border-radius:4px;' +
      'overflow-x:auto;white-space:pre-wrap;word-break:break-all">' +
      escapeHTML(attrJson) + '</pre>';
  }).join('');
  if (!timeline) timeline = '<div style="font-size:12px;color:var(--text-muted);padding:8px 0">No meaningful events in this turn.</div>';

  var detailRow = document.createElement('tr');
  detailRow.className = 'cdx-session-detail-row trace-detail-row';
  detailRow.dataset.idx = groupIdx;
  detailRow.innerHTML = '<td colspan="7"><div class="trace-detail-inner">' +
    '<div class="detail-header"><h3>Turn: ' + escapeHTML(g.turnId) + '</h3>' +
    '<button class="close-btn" onclick="this.closest(\'.cdx-session-detail-row\').remove()">&times;</button></div>' +
    '<div style="margin-bottom:8px;font-size:13px">' +
    events.length + ' events \u00b7 ' +
    g.tools + ' tool calls \u00b7 ' +
    fmtNum(g.tokens) + ' tokens \u00b7 ' +
    fmtCost(g.cost) +
    '</div>' +
    '<div style="max-height:400px;overflow-y:auto">' + timeline + '</div>' +
    '</div></td>';
  clickedRow.after(detailRow);
}

// ===================================================================
// Codex view — Tools tab
// ===================================================================
async function cdx_loadToolsTab() {
  await Promise.all([
    cdx_loadToolsTable(),
    cdx_loadToolTrends(),
    cdx_loadToolFailures(),
  ]);
}

async function cdx_loadToolsTable() {
  var tbody = document.getElementById('cdx-tools-perf-body');
  if (!tbody) return;
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
    if (!data.length) {
      tbody.innerHTML = '<tr><td colspan="4" class="loading">' + t('empty.no_tool_data') + '</td></tr>';
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
    tbody.innerHTML = '<tr><td colspan="4" class="loading">' + t('error.load_tool_perf') + '</td></tr>';
  }
}

async function cdx_loadToolTrends() {
  try {
    var bucket = currentTimeRange === '1h' ? '5 minutes' : (currentTimeRange === '7d' || currentTimeRange === '30d' ? '1 hour' : '15 minutes');
    var res = await query(
      "SELECT date_bin('" + bucket + "'::INTERVAL, timestamp) AS t, " +
      "json_get_string(log_attributes, 'tool_name') AS tool, " +
      "COUNT(*) AS cnt " +
      cdx_logsFromWhere() + " " +
      "AND json_get_string(log_attributes, 'tool_name') IS NOT NULL " +
      "AND json_get_string(log_attributes, 'success') IS NOT NULL " +
      "GROUP BY t, tool ORDER BY t"
    );
    var raw = rowsToObjects(res);
    if (!raw.length) return;

    var toolTotals = {};
    raw.forEach(function(r) {
      toolTotals[r.tool] = (toolTotals[r.tool] || 0) + (Number(r.cnt) || 0);
    });
    var topTools = Object.keys(toolTotals).sort(function(a, b) { return toolTotals[b] - toolTotals[a]; }).slice(0, 6);

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
    renderChart('cdx-tool-trends-chart', data, seriesDefs, function(v) { return fmtNum(v); });
  } catch { /* no data */ }
}

async function cdx_loadToolFailures() {
  var tbody = document.getElementById('cdx-tools-failures-body');
  if (!tbody) return;
  try {
    var res = await query(
      "SELECT timestamp, " +
      "json_get_string(log_attributes, 'tool_name') AS tool, " +
      "json_get_string(log_attributes, 'error') AS error, " +
      "json_get_float(log_attributes, 'duration_ms') AS duration_ms, " +
      "log_attributes " +
      cdx_logsFromWhere() + " " +
      "AND json_get_string(log_attributes, 'tool_name') IS NOT NULL " +
      "AND json_get_string(log_attributes, 'success') = 'false' " +
      "ORDER BY timestamp DESC LIMIT 20"
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      tbody.innerHTML = '<tr><td colspan="4" class="loading">No recent failures.</td></tr>';
      return;
    }
    tbody.innerHTML = data.map(function(d, i) {
      var errText = d.error || 'unknown error';
      if (errText.length > 80) errText = errText.substring(0, 80) + '\u2026';
      var rowId = 'cdx-fail-' + i;
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
    tbody.innerHTML = '<tr><td colspan="4" class="loading">Failed to load failures.</td></tr>';
  }
}

function cdx_onTabChange(tab) {
  if (tab === 'cdx-overview') cdx_loadOverview();
  else if (tab === 'cdx-sessions') cdx_loadSessions();
  else if (tab === 'cdx-tools') cdx_loadToolsTab();
  else if (tab === 'cdx-cost') cdx_loadCostTab();
}
