// traces.js — Traces view: all load* functions
// Depends on: core.js, chart.js, i18n.js

var tracePage = 0;
var tracePageSize = 15;
var traceHasNext = false;

function resetTracePaging() { tracePage = 0; }
function prevTracePage() { if (tracePage <= 0) return; tracePage--; loadTraces(); }
function nextTracePage() { if (!traceHasNext) return; tracePage++; loadTraces(); }
function updateTracePager(resultCount) {
  var prevBtn = document.getElementById('trace-prev-btn');
  var nextBtn = document.getElementById('trace-next-btn');
  var info = document.getElementById('trace-page-info');
  if (!prevBtn || !nextBtn || !info) return;
  prevBtn.disabled = tracePage <= 0;
  nextBtn.disabled = !traceHasNext;
  if (!resultCount) { info.textContent = 'No results'; return; }
  var start = tracePage * tracePageSize + 1;
  var end = start + resultCount - 1;
  info.textContent = 'Page ' + (tracePage + 1) + ' \u00b7 ' + start + '-' + end;
}

// ===================================================================
// Global metrics cards (Traces view)
// ===================================================================
async function loadMetrics() {
  try {
    // Today's cost (dynamic pricing from tma1_model_pricing)
    var costRes = await query(
      "SELECT ROUND(SUM(" + costCaseSQL(
        '"span_attributes.gen_ai.request.model"',
        '"span_attributes.gen_ai.usage.input_tokens"',
        '"span_attributes.gen_ai.usage.output_tokens"'
      ) + "), 4) AS total " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL " +
      "AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "'"
    );
    var costVal = rows(costRes)?.[0]?.[0];
    document.getElementById('val-cost').textContent = fmtCost(costVal);

    // Today's tokens
    var tokenRes = await query(
      "SELECT SUM(CAST(\"span_attributes.gen_ai.usage.input_tokens\" AS DOUBLE) + " +
      "CAST(\"span_attributes.gen_ai.usage.output_tokens\" AS DOUBLE)) AS total " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL " +
      "AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "'"
    );
    var tokenVal = rows(tokenRes)?.[0]?.[0];
    document.getElementById('val-tokens').textContent = fmtNum(tokenVal);

    // Request count
    var reqRes = await query(
      "SELECT COUNT(1) AS total " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL " +
      "AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "'"
    );
    var reqVal = rows(reqRes)?.[0]?.[0];
    document.getElementById('val-requests').textContent = fmtNum(reqVal);

    // p95 latency (raw trace range query)
    var latRes = await query(
      "SELECT APPROX_PERCENTILE_CONT(duration_nano, 0.95) AS p95 " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL " +
      "AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "'"
    );
    var latVal = rows(latRes)?.[0]?.[0];
    document.getElementById('val-latency').textContent = latVal != null ? fmtMs(latVal) : '\u2014';

  } catch (err) {
    var banner = document.getElementById('error-banner');
    banner.style.display = 'block';
    banner.textContent = t('error.greptimedb') + err.message;
  }
}

// ===================================================================
// Overview tab — uPlot charts
// ===================================================================
async function loadOverviewCharts() {
  await Promise.all([
    loadTokenChart(),
    loadCostChart(),
    loadLatencyChart(),
    loadErrorChart(),
    loadToolDistribution(),
    loadMetricsExplorer(),
  ]);
}

async function loadTokenChart() {
  try {
    var res = await query(
      "SELECT date_bin('5 minutes'::INTERVAL, timestamp) AS t, " +
      "SUM(CAST(\"span_attributes.gen_ai.usage.input_tokens\" AS DOUBLE)) AS inp, " +
      "SUM(CAST(\"span_attributes.gen_ai.usage.output_tokens\" AS DOUBLE)) AS outp " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;
    renderChart('chart-tokens', data, [
      { label: t('chart.input_tokens'), key: 'inp', color: '#79c0ff' },
      { label: t('chart.output_tokens'), key: 'outp', color: '#f0883e' },
    ], function(v) { return fmtNum(v); });
  } catch { /* no data yet */ }
}

async function loadCostChart() {
  try {
    var res = await query(
      "SELECT date_bin('5 minutes'::INTERVAL, timestamp) AS t, " +
      "SUM(" + costCaseSQL(
        '"span_attributes.gen_ai.request.model"',
        '"span_attributes.gen_ai.usage.input_tokens"',
        '"span_attributes.gen_ai.usage.output_tokens"'
      ) + ") AS cost " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;
    renderChart('chart-cost', data, [
      { label: t('chart.cost_usd'), key: 'cost', color: '#f0883e' },
    ], function(v) { return '$' + Number(v).toFixed(4); });
  } catch { /* no data yet */ }
}

async function loadLatencyChart() {
  try {
    var res = await query(
      "SELECT date_bin('5 minutes'::INTERVAL, timestamp) AS t, " +
      "APPROX_PERCENTILE_CONT(duration_nano, 0.50) AS p50, " +
      "APPROX_PERCENTILE_CONT(duration_nano, 0.95) AS p95 " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL " +
      "AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (data.length) {
      renderChart('chart-latency', data, [
      { label: t('chart.p50'), key: 'p50', color: '#3fb950' },
      { label: t('chart.p95'), key: 'p95', color: '#d2a8ff' },
      ], function(v) { return fmtMs(v); });
    }
  } catch { /* no data yet */ }
}

async function loadErrorChart() {
  try {
    var res = await query(
      "SELECT date_bin('5 minutes'::INTERVAL, timestamp) AS t, " +
      "COUNT(1) AS total, " +
      "SUM(CASE WHEN span_status_code = 'STATUS_CODE_ERROR' THEN 1 ELSE 0 END) AS errors " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;
    renderChart('chart-errors', data, [
      { label: t('chart.total'), key: 'total', color: '#3fb950' },
      { label: t('chart.errors'), key: 'errors', color: '#f85149' },
    ], function(v) { return fmtNum(v); });
  } catch { /* no data yet */ }
}

// ===================================================================
// Traces tab
// ===================================================================
async function loadTraces() {
  var traceIdFilter = document.getElementById('trace-id-filter').value.trim();
  var modelFilter = document.getElementById('trace-model-filter').value;
  var statusFilter = document.getElementById('trace-status-filter').value;

  var where = "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL";
  if (traceIdFilter) {
    where += " AND trace_id = '" + escapeSQLString(traceIdFilter) + "'";
  } else {
    where += " AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "'";
  }
  if (modelFilter) where += " AND \"span_attributes.gen_ai.request.model\" = '" + escapeSQLString(modelFilter) + "'";
  if (statusFilter) where += " AND span_status_code = '" + escapeSQLString(statusFilter) + "'";

  var limit = tracePageSize + 1;
  var offset = tracePage * tracePageSize;

  try {
    var res = await query(
      "SELECT timestamp, trace_id, " +
      "\"span_attributes.gen_ai.request.model\" AS model, " +
      "\"span_attributes.gen_ai.usage.input_tokens\" AS input_tok, " +
      "\"span_attributes.gen_ai.usage.output_tokens\" AS output_tok, " +
      "ROUND(duration_nano / 1000000.0, 1) AS duration_ms, " +
      "span_status_code AS status, " +
      "\"span_attributes.gen_ai.response.finish_reasons\" AS finish_reason " +
      "FROM opentelemetry_traces " +
      where + " ORDER BY timestamp DESC LIMIT " + limit + " OFFSET " + offset
    );
    var allRows = rowsToObjects(res);
    traceHasNext = allRows.length > tracePageSize;
    var data = traceHasNext ? allRows.slice(0, tracePageSize) : allRows;
    var tbody = document.getElementById('traces-body');

    if (!data.length) {
      if (tracePage > 0) { tracePage--; return loadTraces(); }
      tbody.innerHTML = '<tr><td colspan="7" class="loading">' + t('empty.no_traces') + '</td></tr>';
      updateTracePager(0);
      return;
    }

    tbody.innerHTML = data.map(function(d, i) {
      return '<tr class="clickable" data-idx="' + i + '" onclick="toggleTraceDetail(this, \'' + escapeHTML(d.trace_id) + '\')">' +
      '<td>' + fmtTime(d.timestamp) + '</td>' +
      '<td>' + escapeHTML(d.model || 'unknown') + '</td>' +
      '<td>' + fmtNum(d.input_tok) + '</td>' +
      '<td>' + fmtNum(d.output_tok) + '</td>' +
      '<td>' + fmtDurMs(d.duration_ms) + '</td>' +
      '<td><span class="badge ' + (d.status === 'STATUS_CODE_ERROR' ? 'badge-error' : 'badge-ok') + '">' +
        (d.status === 'STATUS_CODE_ERROR' ? 'ERROR' : 'OK') + '</span></td>' +
      '<td>' + escapeHTML(d.finish_reason || '') + '</td>' +
      '</tr>';
    }).join('');

    // Populate model filter dropdown
    var models = [...new Set(data.map(function(d) { return d.model; }).filter(Boolean))];
    var select = document.getElementById('trace-model-filter');
    var current = select.value;
    select.innerHTML = '<option value="">' + t('filter.all_models') + '</option>' +
      models.map(function(m) { return '<option value="' + escapeHTML(m) + '"' +
        (m === current ? ' selected' : '') + '>' + escapeHTML(m) + '</option>'; }).join('');

    updateTracePager(data.length);
  } catch (err) {
    traceHasNext = false;
    updateTracePager(0);
    document.getElementById('traces-body').innerHTML =
      '<tr><td colspan="7" class="loading">Error: ' + escapeHTML(err.message) + '</td></tr>';
  }
}

function toggleTraceDetail(clickedRow, traceId) {
  var prev = document.querySelector('.trace-detail-row');
  var prevParent = document.querySelector('tr.active-trace');
  if (prevParent) prevParent.classList.remove('active-trace');
  if (prev) {
    if (prev.dataset.traceId === traceId) { prev.remove(); return; }
    prev.remove();
  }

  clickedRow.classList.add('active-trace');

  var detailRow = document.createElement('tr');
  detailRow.className = 'trace-detail-row';
  detailRow.dataset.traceId = traceId;
  detailRow.innerHTML = '<td colspan="7"><div class="trace-detail-inner">' +
    '<div class="detail-header"><h3>' + t('detail.trace_detail') + '</h3>' +
    '<button class="close-btn" onclick="closeTraceDetail()">&times;</button></div>' +
    '<div class="trace-meta" id="trace-meta"></div>' +
    '<div class="waterfall-section"><h4>' + t('detail.span_waterfall') + '</h4>' +
    '<div id="span-waterfall" class="loading">' + t('empty.loading') + '</div></div>' +
    '<div class="conversation-section"><h4>' + t('detail.conversation') + '</h4>' +
    '<div id="conversation-messages" class="loading">' + t('empty.loading') + '</div>' +
    '</div></div></td>';
  clickedRow.after(detailRow);

  loadTraceDetailData(traceId);
}

async function loadTraceDetailData(traceId) {
  var metaEl = document.getElementById('trace-meta');
  var waterfallEl = document.getElementById('span-waterfall');
  var convEl = document.getElementById('conversation-messages');
  var tid = escapeSQLString(traceId);

  // Load all spans for this trace
  try {
    var res = await query(
      "SELECT timestamp, span_id, parent_span_id, span_name, " +
      "\"span_attributes.gen_ai.request.model\" AS model, " +
      "\"span_attributes.gen_ai.usage.input_tokens\" AS input_tok, " +
      "\"span_attributes.gen_ai.usage.output_tokens\" AS output_tok, " +
      "duration_nano, span_status_code AS status " +
      "FROM opentelemetry_traces " +
      "WHERE trace_id = '" + tid + "' " +
      "ORDER BY timestamp LIMIT 100"
    );
    var spans = rowsToObjects(res);

    if (spans.length > 0) {
      // Meta summary from root/first span
      var root = spans[0];
      var totalInput = spans.reduce(function(s, x) { return s + (Number(x.input_tok) || 0); }, 0);
      var totalOutput = spans.reduce(function(s, x) { return s + (Number(x.output_tok) || 0); }, 0);
      var hasError = spans.some(function(x) { return x.status === 'STATUS_CODE_ERROR'; });
      var models = [...new Set(spans.map(function(x) { return x.model; }).filter(Boolean))];
      metaEl.innerHTML =
        metaItem('Trace ID', traceId) +
        metaItem(t('table.model'), models.join(', ') || 'unknown') +
        metaItem(t('detail.spans'), spans.length) +
        metaItem(t('detail.input_tokens'), fmtNum(totalInput)) +
        metaItem(t('detail.output_tokens'), fmtNum(totalOutput)) +
        metaItem(t('table.duration'), fmtMs(root.duration_nano)) +
        metaItem(t('table.status'), hasError ? 'ERROR' : 'OK') +
        metaItem(t('table.started'), fmtTime(root.timestamp));

      // Build tree + waterfall
      renderWaterfall(waterfallEl, spans);
    } else {
      metaEl.innerHTML = metaItem('Trace ID', traceId);
      waterfallEl.innerHTML = '<div class="loading">' + t('error.no_spans') + '</div>';
    }
  } catch {
    waterfallEl.innerHTML = '<div class="loading">' + t('error.load_spans') + '</div>';
  }

  // Conversation replay not available for trace-based agents
  convEl.innerHTML = '<div class="loading">' + t('empty.conv_not_available') + '</div>';
}

function renderWaterfall(container, spans) {
  var times = spans.map(function(s) { return new Date(s.timestamp).getTime(); });
  var durations = spans.map(function(s) { return Number(s.duration_nano) || 0; });
  var traceStart = Math.min.apply(null, times);
  var traceEnd = Math.max.apply(null, times.map(function(t, i) { return t + durations[i] / 1e6; }));
  var totalMs = Math.max(traceEnd - traceStart, 1);

  // Build parent-child tree
  var byId = {};
  var roots = [];
  spans.forEach(function(s, i) { byId[s.span_id] = { span: s, children: [], idx: i }; });
  spans.forEach(function(s) {
    var pid = s.parent_span_id;
    if (pid && byId[pid]) {
      byId[pid].children.push(byId[s.span_id]);
    } else {
      roots.push(byId[s.span_id]);
    }
  });

  // Flatten tree via DFS for display order
  var flat = [];
  function dfs(node, depth) {
    flat.push({ span: node.span, depth: depth, idx: node.idx });
    node.children.sort(function(a, b) {
      return new Date(a.span.timestamp).getTime() - new Date(b.span.timestamp).getTime();
    });
    node.children.forEach(function(c) { dfs(c, depth + 1); });
  }
  roots.forEach(function(r) { dfs(r, 0); });

  // Render rows
  var labelWidth = 220;
  container.innerHTML = '';
  container.className = 'waterfall';
  flat.forEach(function(item) {
    var s = item.span;
    var startMs = new Date(s.timestamp).getTime() - traceStart;
    var durMs = (Number(s.duration_nano) || 0) / 1e6;
    var leftPct = (startMs / totalMs * 100).toFixed(2);
    var widthPct = Math.max(durMs / totalMs * 100, 0.5).toFixed(2);
    var indent = item.depth * 16;
    var barClass = s.status === 'STATUS_CODE_ERROR' ? 'error' : 'ok';
    var name = s.span_name || s.model || 'span';

    var row = document.createElement('div');
    row.className = 'waterfall-row';
    row.innerHTML =
      '<div class="waterfall-label" style="width:' + labelWidth + 'px;padding-left:' + indent + 'px" title="' + escapeHTML(name) + '">' +
        (item.depth > 0 ? '<span style="color:var(--text-dim);margin-right:4px">\u2514</span>' : '') +
        escapeHTML(name) +
      '</div>' +
      '<div class="waterfall-track">' +
        '<div class="waterfall-bar ' + barClass + '" style="left:' + leftPct + '%;width:' + widthPct + '%">' +
          (durMs >= 10 ? fmtDurMs(durMs) : '') +
        '</div>' +
      '</div>' +
      '<div class="waterfall-dur">' + fmtDurMs(durMs) + '</div>';
    container.appendChild(row);
  });
}

function metaItem(label, value) {
  return '<div class="meta-item"><div class="meta-label">' + label +
    '</div><div class="meta-value">' + escapeHTML(String(value)) + '</div></div>';
}

function closeTraceDetail() {
  var row = document.querySelector('.trace-detail-row');
  var parent = document.querySelector('tr.active-trace');
  if (parent) parent.classList.remove('active-trace');
  if (row) row.remove();
}

// ===================================================================
// Cost tab
// ===================================================================
async function loadCostTab() {
  await Promise.all([
    loadCostByModel(),
    loadExpensiveConversations(),
    loadPerQuestionCost(),
    loadFinishReasons(),
    loadContextSnowball(),
    loadModelComparison(),
  ]);
}

async function loadCostByModel() {
  try {
    var costExpr = costCaseSQL(
      '"span_attributes.gen_ai.request.model"',
      '"span_attributes.gen_ai.usage.input_tokens"',
      '"span_attributes.gen_ai.usage.output_tokens"'
    );
    var res = await query(
      "SELECT \"span_attributes.gen_ai.request.model\" AS model, ROUND(SUM(" + costExpr +
      "), 4) AS total_cost " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY model ORDER BY total_cost DESC LIMIT 10"
    );
    var data = rowsToObjects(res);
    var el = document.getElementById('cost-by-model');
    var tc = getThemeColors();
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_cost_data') + '</div>'; return; }
    var maxCost = Math.max.apply(null, data.map(function(d) { return Number(d.total_cost) || 0; }));
    el.innerHTML = data.map(function(d) {
      var pct = maxCost > 0 ? (Number(d.total_cost) / maxCost * 100) : 0;
      return '<div class="bar-row">' +
        '<div class="bar-label">' + escapeHTML(d.model) + '</div>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + pct + '%;background:' + tc.orange + '"></div></div>' +
        '<div class="bar-value">' + fmtCost(d.total_cost) + '</div>' +
        '</div>';
    }).join('');
  } catch { /* ignore */ }
}

async function loadExpensiveConversations() {
  try {
    var costExpr = costCaseSQL(
      '"span_attributes.gen_ai.request.model"',
      '"span_attributes.gen_ai.usage.input_tokens"',
      '"span_attributes.gen_ai.usage.output_tokens"'
    );
    var res = await query(
      "SELECT trace_id, timestamp, " +
      "\"span_attributes.gen_ai.request.model\" AS model, " +
      "\"span_attributes.gen_ai.usage.input_tokens\" AS input_tok, " +
      "\"span_attributes.gen_ai.usage.output_tokens\" AS output_tok, " +
      "ROUND(" + costExpr + ", 4) AS est_cost_usd " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "ORDER BY est_cost_usd DESC LIMIT 10"
    );
    var data = rowsToObjects(res);
    var tbody = document.getElementById('expensive-body');
    if (!data.length) {
      tbody.innerHTML = '<tr><td colspan="5" class="loading">' + t('empty.no_data') + '</td></tr>';
      return;
    }
    tbody.innerHTML = data.map(function(d) {
      return '<tr class="clickable" onclick="switchToTrace(\'' + escapeHTML(d.trace_id) + '\')">' +
      '<td>' + fmtTime(d.timestamp) + '</td>' +
      '<td>' + escapeHTML(d.model || 'unknown') + '</td>' +
      '<td>' + fmtNum(d.input_tok) + '</td>' +
      '<td>' + fmtNum(d.output_tok) + '</td>' +
      '<td>' + fmtCost(d.est_cost_usd) + '</td>' +
      '</tr>';
    }).join('');
  } catch { /* ignore */ }
}

async function loadFinishReasons() {
  try {
    var res = await query(
      "SELECT \"span_attributes.gen_ai.response.finish_reasons\" AS reason, " +
      "COUNT(1) AS cnt " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY reason ORDER BY cnt DESC"
    );
    var data = rowsToObjects(res);
    var el = document.getElementById('finish-reason-chart');
    var tc = getThemeColors();
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_data') + '</div>'; return; }
    var maxCnt = Math.max.apply(null, data.map(function(d) { return Number(d.cnt) || 0; }));
    var colors = { stop: tc.green, length: tc.orange, tool_calls: tc.blue };
    el.innerHTML = data.map(function(d) {
      var pct = maxCnt > 0 ? (Number(d.cnt) / maxCnt * 100) : 0;
      var color = colors[d.reason] || tc.axisStroke;
      return '<div class="bar-row">' +
        '<div class="bar-label">' + escapeHTML(d.reason || 'unknown') + '</div>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + pct + '%;background:' + color + '"></div></div>' +
        '<div class="bar-value">' + fmtNum(d.cnt) + '</div>' +
        '</div>';
    }).join('');
  } catch { /* ignore */ }
}

async function loadModelComparison() {
  try {
    var costExpr = costCaseSQL(
      '"span_attributes.gen_ai.request.model"',
      '"span_attributes.gen_ai.usage.input_tokens"',
      '"span_attributes.gen_ai.usage.output_tokens"'
    );
    var res = await query(
      "SELECT \"span_attributes.gen_ai.request.model\" AS model, " +
      "COUNT(1) AS reqs, " +
      "SUM(CASE WHEN span_status_code = 'STATUS_CODE_ERROR' THEN 1 ELSE 0 END) AS errs, " +
      "ROUND(AVG(duration_nano) / 1000000.0, 0) AS avg_latency_ms, " +
      "SUM(" + costExpr + ") AS total_cost " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY model ORDER BY reqs DESC"
    );
    var data = rowsToObjects(res);
    var tbody = document.getElementById('model-compare-body');
    if (!data.length) {
      tbody.innerHTML = '<tr><td colspan="5" class="loading">' + t('empty.no_data') + '</td></tr>';
      return;
    }
    tbody.innerHTML = data.map(function(d) {
      var reqs = Number(d.reqs) || 0;
      var errs = Number(d.errs) || 0;
      var errRate = reqs > 0 ? ((errs / reqs) * 100).toFixed(1) + '%' : '0%';
      var avgCost = reqs > 0 ? fmtCost(Number(d.total_cost) / reqs) : '\u2014';
      return '<tr>' +
        '<td>' + escapeHTML(d.model) + '</td>' +
        '<td>' + fmtNum(reqs) + '</td>' +
        '<td>' + fmtDurMs(d.avg_latency_ms) + '</td>' +
        '<td>' + avgCost + '</td>' +
        '<td>' + errRate + '</td>' +
        '</tr>';
    }).join('');
  } catch { /* ignore */ }
}

function switchToTrace(traceId) {
  document.querySelectorAll('#view-traces .tab').forEach(function(t) { t.classList.remove('active'); });
  document.querySelectorAll('#view-traces .tab-content').forEach(function(t) { t.classList.remove('active'); });
  document.querySelector('[data-tab="traces"]').classList.add('active');
  document.getElementById('tab-traces').classList.add('active');
  document.getElementById('trace-id-filter').value = traceId;
  resetTracePaging();
  loadTraces().then(function() {
    var row = document.querySelector('#traces-body tr.clickable');
    if (row) toggleTraceDetail(row, traceId);
  });
}

// ===================================================================
// Search tab
// ===================================================================
async function doSearch() {
  var term = document.getElementById('search-input').value.trim();
  if (!term) return;
  var el = document.getElementById('search-results');
  el.innerHTML = '<div class="loading">' + t('empty.searching') + '</div>';

  try {
    var safeTerm = escapeSQLString(term);
    var res = await query(
      "SELECT timestamp, trace_id, span_name, " +
      "\"span_attributes.gen_ai.request.model\" AS model, " +
      "span_status_code " +
      "FROM opentelemetry_traces " +
      "WHERE (span_name LIKE '%" + safeTerm + "%' " +
      "  OR \"span_attributes.gen_ai.request.model\" LIKE '%" + safeTerm + "%') " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "ORDER BY timestamp DESC LIMIT 50"
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      el.innerHTML = '<div class="loading">' + t('empty.no_results') + '</div>';
      return;
    }
    el.innerHTML = data.map(function(d) {
      return '<div class="search-result-item" onclick="switchToTrace(\'' + escapeHTML(d.trace_id) + '\')">' +
      '<div class="search-result-meta">' +
      '<span>' + fmtTime(d.timestamp) + '</span>' +
      '<span>' + escapeHTML(d.span_name || '') + '</span>' +
      '<span>' + escapeHTML(d.model || '') + '</span>' +
      '<span>' + escapeHTML(d.trace_id) + '</span>' +
      '</div>' +
      '<div class="search-result-content">' + escapeHTML(d.span_name || '') + ' &middot; ' + escapeHTML(d.model || 'unknown') + '</div>' +
      '</div>';
    }).join('');
  } catch (err) {
    el.innerHTML = '<div class="loading">' + t('error.search') + escapeHTML(err.message) + '</div>';
  }
}

async function loadAnomalies() {
  var el = document.getElementById('anomaly-list');
  el.innerHTML = '<div class="loading">' + t('empty.loading_anomalies') + '</div>';

  try {
    var res = await query(
      "SELECT trace_id, timestamp, " +
      "\"span_attributes.gen_ai.request.model\" AS model, " +
      "\"span_attributes.gen_ai.usage.input_tokens\" AS input_tok, " +
      "\"span_attributes.gen_ai.usage.output_tokens\" AS output_tok, " +
      "span_status_code AS status, " +
      "ROUND(duration_nano / 1000000.0, 1) AS duration_ms " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "  AND (span_status_code = 'STATUS_CODE_ERROR' " +
      "    OR \"span_attributes.gen_ai.usage.input_tokens\" > 10000) " +
      "ORDER BY timestamp DESC LIMIT 20"
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      el.innerHTML = '<div class="loading">' + t('empty.no_anomalies') + '</div>';
      return;
    }
    el.innerHTML = data.map(function(d) {
      var reason = t('anomaly.high_token');
      if (d.status === 'STATUS_CODE_ERROR') reason = t('anomaly.error_response');
      return '<div class="anomaly-item" onclick="switchToTrace(\'' + escapeHTML(d.trace_id) + '\')">' +
        '<div class="anomaly-reason">' + reason + '</div>' +
        '<div style="font-size:13px">' +
        escapeHTML(d.model || 'unknown') + ' &middot; ' +
        fmtNum(d.input_tok) + ' in / ' + fmtNum(d.output_tok) + ' out &middot; ' +
        fmtDurMs(d.duration_ms) + ' &middot; ' +
        fmtTime(d.timestamp) +
        '</div>' +
        '</div>';
    }).join('');
  } catch {
    el.innerHTML = '<div class="loading">' + t('error.load_anomalies') + '</div>';
  }
}

// ===================================================================
// Tool Call Distribution (Overview tab)
// ===================================================================
async function loadToolDistribution() {
  try {
    var tc = getThemeColors();
    var res = await query(
      "SELECT span_name, COUNT(*) AS cnt, " +
      "SUM(CASE WHEN span_status_code = 'STATUS_CODE_ERROR' THEN 1 ELSE 0 END) AS errs " +
      "FROM opentelemetry_traces " +
      "WHERE span_name IS NOT NULL " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY span_name ORDER BY cnt DESC LIMIT 10"
    );
    var data = rowsToObjects(res);
    var el = document.getElementById('tool-distribution');
    if (!data.length) { el.innerHTML = '<div class="chart-empty">' + t('empty.no_tool_data') + '</div>'; return; }
    var maxCnt = Math.max.apply(null, data.map(function(d) { return Number(d.cnt) || 0; }));
    el.innerHTML = data.map(function(d) {
      var cnt = Number(d.cnt) || 0;
      var errs = Number(d.errs) || 0;
      var pct = maxCnt > 0 ? (cnt / maxCnt * 100) : 0;
      var errPct = cnt > 0 ? (errs / cnt * 100) : 0;
      return '<div class="bar-row">' +
        '<div class="bar-label" title="' + escapeHTML(d.span_name) + '">' + escapeHTML(d.span_name) + '</div>' +
        '<div class="bar-track bar-track-stacked">' +
          '<div class="bar-fill" style="width:' + pct + '%;background:' + tc.green + '"></div>' +
          (errs > 0 ? '<div class="bar-fill-error" style="width:' + (pct * errPct / 100) + '%;left:' + (pct - pct * errPct / 100) + '%"></div>' : '') +
        '</div>' +
        '<div class="bar-value">' + fmtNum(cnt) + (errs > 0 ? ' <span style="color:' + tc.red + '">(' + errs + ' err)</span>' : '') + '</div>' +
        '</div>';
    }).join('');
  } catch { /* ignore */ }
}

// ===================================================================
// Per-Question Cost (Cost tab)
// ===================================================================
async function loadPerQuestionCost() {
  try {
    var costExpr = costCaseSQL(
      '"span_attributes.gen_ai.request.model"',
      '"span_attributes.gen_ai.usage.input_tokens"',
      '"span_attributes.gen_ai.usage.output_tokens"'
    );
    var res = await query(
      "SELECT trace_id, " +
      "COUNT(*) AS llm_calls, " +
      "SUM(CAST(\"span_attributes.gen_ai.usage.input_tokens\" AS DOUBLE)) AS input_tokens, " +
      "SUM(CAST(\"span_attributes.gen_ai.usage.output_tokens\" AS DOUBLE)) AS output_tokens, " +
      "ROUND(SUM(" + costExpr + "), 4) AS est_cost_usd, " +
      "MIN(timestamp) AS started " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "GROUP BY trace_id " +
      "ORDER BY est_cost_usd DESC LIMIT 20"
    );
    var data = rowsToObjects(res);
    var tbody = document.getElementById('per-question-body');
    if (!data.length) {
      tbody.innerHTML = '<tr><td colspan="6" class="loading">' + t('empty.no_data') + '</td></tr>';
      return;
    }
    tbody.innerHTML = data.map(function(d) {
      return '<tr class="clickable" onclick="switchToTrace(\'' + escapeHTML(d.trace_id) + '\')">' +
      '<td>' + fmtTime(d.started) + '</td>' +
      '<td title="' + escapeHTML(d.trace_id) + '">' + escapeHTML((d.trace_id || '').substring(0, 8)) + '...</td>' +
      '<td>' + fmtNum(d.llm_calls) + '</td>' +
      '<td>' + fmtNum(d.input_tokens) + '</td>' +
      '<td>' + fmtNum(d.output_tokens) + '</td>' +
      '<td>' + fmtCost(d.est_cost_usd) + '</td>' +
      '</tr>';
    }).join('');
  } catch { /* ignore */ }
}

// ===================================================================
// Context Window Snowball (Cost tab)
// ===================================================================
async function loadContextSnowball() {
  try {
    var res = await query(
      "SELECT trace_id, " +
      "CAST(\"span_attributes.gen_ai.usage.input_tokens\" AS DOUBLE) AS input_tok, " +
      "timestamp " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NOT NULL " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "ORDER BY trace_id, timestamp"
    );
    var data = rowsToObjects(res);
    if (!data.length) return;

    // Group by trace_id
    var byTrace = {};
    data.forEach(function(d) {
      if (!byTrace[d.trace_id]) byTrace[d.trace_id] = [];
      byTrace[d.trace_id].push(Number(d.input_tok) || 0);
    });

    // Pick top 5 traces by total tokens
    var traceTotals = Object.entries(byTrace).map(function(entry) {
      return { tid: entry[0], total: entry[1].reduce(function(s, t) { return s + t; }, 0), toks: entry[1] };
    });
    traceTotals.sort(function(a, b) { return b.total - a.total; });
    var top5 = traceTotals.slice(0, 5);

    if (!top5.length || top5[0].toks.length < 2) return;

    var tc = getThemeColors();

    // Build uPlot data: X = turn index, Y = cumulative tokens per trace
    var maxLen = Math.max.apply(null, top5.map(function(t) { return t.toks.length; }));
    var xVals = Array.from({ length: maxLen }, function(_, i) { return i; });
    var uData = [xVals];
    var uSeries = [{}];

    var snowballColors = [tc.blue, tc.orange, tc.green, tc.purple, tc.red];

    top5.forEach(function(t, i) {
      var cum = 0;
      var cumArr = t.toks.map(function(v) { cum += v; return cum; });
      while (cumArr.length < maxLen) cumArr.push(null);
      uData.push(cumArr);
      uSeries.push({
        label: t.tid.substring(0, 8),
        stroke: snowballColors[i % snowballColors.length],
        width: 2,
      });
    });

    var container = document.getElementById('chart-snowball');
    container.innerHTML = '';
    var width = container.clientWidth - 32;
    var opts = {
      width: width,
      height: 220,
      cursor: { show: true },
      scales: { x: { time: false } },
      axes: [
        { stroke: tc.axisStroke, grid: { stroke: tc.gridStroke }, label: t('chart.turn') },
        { stroke: tc.axisStroke, grid: { stroke: tc.gridStroke },
          values: function(u, vals) { return vals.map(function(v) { return fmtNum(v); }); } },
      ],
      series: uSeries,
    };
    if (chartInstances['chart-snowball']) chartInstances['chart-snowball'].destroy();
    chartInstances['chart-snowball'] = new uPlot(opts, uData, container);
  } catch { /* ignore */ }
}

// ===================================================================
// Security tab
// ===================================================================
async function loadSecurityTab() {
  await Promise.all([
    loadSecuritySummary(),
    loadDangerousCommands(),
    loadInjectionAlerts(),
    loadToolTimeline(),
  ]);
}

async function loadSecuritySummary() {
  try {
    var res = await query(
      "SELECT COUNT(*) AS cnt FROM opentelemetry_traces " +
      "WHERE span_name IN ('exec', 'bash', 'shell', 'terminal', 'command', 'Bash', 'Execute') " +
      "AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "'"
    );
    document.getElementById('sec-shell-count').textContent = fmtNum(rows(res)?.[0]?.[0]);
  } catch { document.getElementById('sec-shell-count').textContent = '\u2014'; }

  try {
    var res2 = await query(
      "SELECT COUNT(*) AS cnt FROM opentelemetry_traces " +
      "WHERE span_name IN ('web_fetch', 'WebFetch', 'browser', 'http_request', 'fetch') " +
      "AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "'"
    );
    document.getElementById('sec-fetch-count').textContent = fmtNum(rows(res2)?.[0]?.[0]);
  } catch { document.getElementById('sec-fetch-count').textContent = '\u2014'; }

  document.getElementById('sec-injection-count').textContent = 'N/A';

  try {
    var res3 = await query(
      "SELECT COUNT(*) AS cnt FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.usage.input_tokens\" > 50000 " +
      "AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "'"
    );
    document.getElementById('sec-hightoken-count').textContent = fmtNum(rows(res3)?.[0]?.[0]);
  } catch { document.getElementById('sec-hightoken-count').textContent = '\u2014'; }
}

async function loadDangerousCommands() {
  var tbody = document.getElementById('dangerous-cmds-body');
  try {
    var res = await query(
      "SELECT timestamp, trace_id, span_name, " +
      "\"span_attributes.gen_ai.request.model\" AS model, " +
      "ROUND(duration_nano / 1000000.0, 1) AS duration_ms " +
      "FROM opentelemetry_traces " +
      "WHERE span_name IN ('exec', 'bash', 'shell', 'terminal', 'command', 'Bash', 'Execute') " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "ORDER BY timestamp DESC LIMIT 50"
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      tbody.innerHTML = '<tr><td colspan="5" class="loading">' + t('empty.no_dangerous') + '</td></tr>';
      return;
    }
    tbody.innerHTML = data.map(function(d) {
      return '<tr class="clickable" onclick="switchToTrace(\'' + escapeHTML(d.trace_id) + '\')">' +
      '<td>' + fmtTime(d.timestamp) + '</td>' +
      '<td title="' + escapeHTML(d.trace_id) + '">' + escapeHTML((d.trace_id || '').substring(0, 8)) + '...</td>' +
      '<td>' + escapeHTML(d.span_name) + '</td>' +
      '<td>' + escapeHTML(d.model || '\u2014') + '</td>' +
      '<td>' + fmtDurMs(d.duration_ms) + '</td>' +
      '</tr>';
    }).join('');
  } catch {
    tbody.innerHTML = '<tr><td colspan="5" class="loading">' + t('error.load_commands') + '</td></tr>';
  }
}

async function loadInjectionAlerts() {
  var el = document.getElementById('injection-alerts');
  el.innerHTML = '<div class="loading">' + t('empty.injection_na') + '</div>';
}

async function loadToolTimeline() {
  var tbody = document.getElementById('tool-timeline-body');
  try {
    var res = await query(
      "SELECT timestamp, trace_id, span_name, span_status_code, " +
      "ROUND(duration_nano / 1000000.0, 1) AS duration_ms " +
      "FROM opentelemetry_traces " +
      "WHERE \"span_attributes.gen_ai.system\" IS NULL " +
      "  AND span_name IS NOT NULL " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "ORDER BY timestamp DESC LIMIT 30"
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      tbody.innerHTML = '<tr><td colspan="5" class="loading">' + t('empty.no_tool_exec') + '</td></tr>';
      return;
    }
    tbody.innerHTML = data.map(function(d) {
      return '<tr class="clickable" onclick="switchToTrace(\'' + escapeHTML(d.trace_id) + '\')">' +
      '<td>' + fmtTime(d.timestamp) + '</td>' +
      '<td title="' + escapeHTML(d.trace_id) + '">' + escapeHTML((d.trace_id || '').substring(0, 8)) + '...</td>' +
      '<td>' + escapeHTML(d.span_name) + '</td>' +
      '<td><span class="badge ' + (d.span_status_code === 'STATUS_CODE_ERROR' ? 'badge-error' : 'badge-ok') + '">' +
        (d.span_status_code === 'STATUS_CODE_ERROR' ? 'ERROR' : 'OK') + '</span></td>' +
      '<td>' + fmtDurMs(d.duration_ms) + '</td>' +
      '</tr>';
    }).join('');
  } catch {
    tbody.innerHTML = '<tr><td colspan="5" class="loading">' + t('error.load_tools') + '</td></tr>';
  }
}

// ===================================================================
// Metrics Explorer (Overview tab) — delegates to metrics-explorer.js
// ===================================================================
function loadMetricsExplorer() {
  initMetricsExplorer('metrics');
}
