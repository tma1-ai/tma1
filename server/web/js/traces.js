// traces.js — Traces view: all load* functions
// Depends on: core.js (setHealthFromData), chart.js, i18n.js

var tracePage = 0;
var tracePageSize = 15;
var traceHasNext = false;
var genaiTraceColumnsPromise = null;

function genai_getTraceColumns() {
  if (!genaiTraceColumnsPromise) {
    genaiTraceColumnsPromise = query(
      "SELECT column_name FROM information_schema.columns " +
      "WHERE table_schema = 'public' AND table_name = 'opentelemetry_traces' " +
      "AND column_name LIKE 'span_attributes.gen_ai.%'"
    ).then(function(res) {
      var cols = {};
      rowsToObjects(res).forEach(function(r) { cols[r.column_name] = true; });
      return cols;
    }).catch(function() { return {}; });
  }
  return genaiTraceColumnsPromise;
}

function genai_traceAttrSelect(columns, columnName, alias) {
  if (columns && columns[columnName]) return '"' + columnName + '" AS ' + alias;
  return 'NULL AS ' + alias;
}

// Build a SQL WHERE fragment that identifies GenAI spans.
// Checks both the deprecated gen_ai.system and the new gen_ai.provider.name,
// depending on which columns exist in the schema.
function genai_systemWhere(cols) {
  var hasSys = cols && cols['span_attributes.gen_ai.system'];
  var hasProv = cols && cols['span_attributes.gen_ai.provider.name'];
  if (hasSys && hasProv)
    return '("span_attributes.gen_ai.system" IS NOT NULL OR "span_attributes.gen_ai.provider.name" IS NOT NULL)';
  if (hasProv)
    return '"span_attributes.gen_ai.provider.name" IS NOT NULL';
  // Fall back to deprecated column (or no data at all)
  return '"span_attributes.gen_ai.system" IS NOT NULL';
}

// Inverse: WHERE fragment that excludes GenAI spans (for tool-only queries).
function genai_systemWhereNull(cols) {
  var hasSys = cols && cols['span_attributes.gen_ai.system'];
  var hasProv = cols && cols['span_attributes.gen_ai.provider.name'];
  if (hasSys && hasProv)
    return '("span_attributes.gen_ai.system" IS NULL AND "span_attributes.gen_ai.provider.name" IS NULL)';
  if (hasProv)
    return '"span_attributes.gen_ai.provider.name" IS NULL';
  return '"span_attributes.gen_ai.system" IS NULL';
}

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
  if (!resultCount) { info.textContent = t('pager.no_results'); return; }
  var start = tracePage * tracePageSize + 1;
  var end = start + resultCount - 1;
  info.textContent = t('pager.page') + ' ' + (tracePage + 1) + ' \u00b7 ' + start + '-' + end;
}

// ===================================================================
// Global metrics cards (Traces view)
// ===================================================================
async function loadMetrics() {
  // Reset column cache so each refresh cycle picks up newly created columns
  genaiTraceColumnsPromise = null;
  try {
    // Check which gen_ai columns exist (schema-on-write: columns only appear after first matching span)
    var colRes = await query(
      "SELECT column_name FROM information_schema.columns " +
      "WHERE table_schema = 'public' AND table_name = 'opentelemetry_traces' " +
      "AND column_name LIKE 'span_attributes.gen_ai.%'"
    );
    var colSet = {};
    rowsToObjects(colRes).forEach(function(r) { colSet[r.column_name] = true; });
    var hasSystem = !!colSet['span_attributes.gen_ai.system'];
    var hasProvider = !!colSet['span_attributes.gen_ai.provider.name'];
    var hasModel = !!colSet['span_attributes.gen_ai.request.model'];
    var hasInputTok = !!colSet['span_attributes.gen_ai.usage.input_tokens'];
    var hasOutputTok = !!colSet['span_attributes.gen_ai.usage.output_tokens'];

    // No gen_ai columns at all — no data yet, clean exit
    if (!hasSystem && !hasProvider) return false;

    var genaiWhere = genai_systemWhere(colSet);
    var iv = intervalSQL();
    var queries = [];

    // [0] Cost — needs model + token columns
    if (hasModel && hasInputTok && hasOutputTok) {
      queries.push(query(
        "SELECT ROUND(SUM(" + costCaseSQL(
          '"span_attributes.gen_ai.request.model"',
          '"span_attributes.gen_ai.usage.input_tokens"',
          '"span_attributes.gen_ai.usage.output_tokens"'
        ) + "), 4) AS total " +
        "FROM opentelemetry_traces " +
        "WHERE " + genaiWhere + " " +
        "AND timestamp > NOW() - INTERVAL '" + iv + "'"
      ));
    } else { queries.push(Promise.resolve(null)); }

    // [1] Tokens — needs token columns
    if (hasInputTok && hasOutputTok) {
      queries.push(query(
        "SELECT SUM(CAST(\"span_attributes.gen_ai.usage.input_tokens\" AS DOUBLE) + " +
        "CAST(\"span_attributes.gen_ai.usage.output_tokens\" AS DOUBLE)) AS total " +
        "FROM opentelemetry_traces " +
        "WHERE " + genaiWhere + " " +
        "AND timestamp > NOW() - INTERVAL '" + iv + "'"
      ));
    } else { queries.push(Promise.resolve(null)); }

    // [2] Request count — only needs system column (always true here)
    queries.push(query(
      "SELECT COUNT(1) AS total " +
      "FROM opentelemetry_traces " +
      "WHERE " + genaiWhere + " " +
      "AND timestamp > NOW() - INTERVAL '" + iv + "'"
    ));

    // [3] Avg latency — only needs system column + duration_nano
    queries.push(query(
      "SELECT AVG(duration_nano) AS avg_lat " +
      "FROM opentelemetry_traces " +
      "WHERE " + genaiWhere + " " +
      "AND timestamp > NOW() - INTERVAL '" + iv + "'"
    ));

    var results = await Promise.all(queries);
    document.getElementById('val-cost').textContent = results[0] ? fmtCost(rows(results[0])?.[0]?.[0]) : '\u2014';
    document.getElementById('val-tokens').textContent = results[1] ? fmtNum(rows(results[1])?.[0]?.[0]) : '\u2014';
    var reqCount = Number(rows(results[2])?.[0]?.[0]) || 0;
    document.getElementById('val-requests').textContent = fmtNum(reqCount);
    var latVal = results[3] ? rows(results[3])?.[0]?.[0] : null;
    document.getElementById('val-latency').textContent = latVal != null ? fmtMs(latVal) : '\u2014';

    // Health indicator (5-minute window)
    updateHealthIndicator('health-indicator', reqCount);

    return reqCount > 0;
  } catch (err) {
    var banner = document.getElementById('error-banner');
    banner.style.display = 'block';
    banner.textContent = t('error.greptimedb') + err.message;
    return false;
  }
}

async function updateHealthIndicator(elementId, reqCount) {
  var genaiWhere = genai_systemWhere(await genai_getTraceColumns());
  var el = document.getElementById(elementId);
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
      "WHERE " + genaiWhere + " " +
      "AND timestamp > NOW() - INTERVAL '5 minutes'"
    );
    var r = rowsToObjects(res)[0] || {};
    setHealthFromData(el, r);
  } catch {
    el.className = 'health-indicator health-na';
    el.innerHTML = '<span class="health-dot"></span><span class="health-text">N/A</span>';
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
  var genaiWhere = genai_systemWhere(await genai_getTraceColumns());
  try {
    var res = await query(
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, timestamp) AS t, " +
      "SUM(CAST(\"span_attributes.gen_ai.usage.input_tokens\" AS DOUBLE)) AS inp, " +
      "SUM(CAST(\"span_attributes.gen_ai.usage.output_tokens\" AS DOUBLE)) AS outp " +
      "FROM opentelemetry_traces " +
      "WHERE " + genaiWhere + " " +
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
  var genaiWhere = genai_systemWhere(await genai_getTraceColumns());
  try {
    var res = await query(
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, timestamp) AS t, " +
      "SUM(" + costCaseSQL(
        '"span_attributes.gen_ai.request.model"',
        '"span_attributes.gen_ai.usage.input_tokens"',
        '"span_attributes.gen_ai.usage.output_tokens"'
      ) + ") AS cost " +
      "FROM opentelemetry_traces " +
      "WHERE " + genaiWhere + " " +
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
  var genaiWhere = genai_systemWhere(await genai_getTraceColumns());
  try {
    var res = await query(
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, timestamp) AS t, " +
      "APPROX_PERCENTILE_CONT(duration_nano, 0.50) AS p50, " +
      "APPROX_PERCENTILE_CONT(duration_nano, 0.95) AS p95 " +
      "FROM opentelemetry_traces " +
      "WHERE " + genaiWhere + " " +
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
  var genaiWhere = genai_systemWhere(await genai_getTraceColumns());
  try {
    var res = await query(
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, timestamp) AS t, " +
      "COUNT(1) AS total, " +
      "SUM(CASE WHEN span_status_code = 'STATUS_CODE_ERROR' THEN 1 ELSE 0 END) AS errors " +
      "FROM opentelemetry_traces " +
      "WHERE " + genaiWhere + " " +
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
  var cols = await genai_getTraceColumns();

  var where = "WHERE " + genai_systemWhere(cols);
  if (traceIdFilter) {
    where += " AND trace_id = '" + escapeSQLString(traceIdFilter) + "'";
  } else {
    where += " AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "'";
  }
  if (modelFilter && cols['span_attributes.gen_ai.request.model']) {
    where += " AND \"span_attributes.gen_ai.request.model\" = '" + escapeSQLString(modelFilter) + "'";
  }
  if (statusFilter) where += " AND span_status_code = '" + escapeSQLString(statusFilter) + "'";

  var limit = tracePageSize + 1;
  var offset = tracePage * tracePageSize;

  try {
    var modelSel = genai_traceAttrSelect(cols, 'span_attributes.gen_ai.request.model', 'model');
    var inputSel = genai_traceAttrSelect(cols, 'span_attributes.gen_ai.usage.input_tokens', 'input_tok');
    var outputSel = genai_traceAttrSelect(cols, 'span_attributes.gen_ai.usage.output_tokens', 'output_tok');
    var finishSel = genai_traceAttrSelect(cols, 'span_attributes.gen_ai.response.finish_reasons', 'finish_reason');
    var res = await query(
      "SELECT timestamp, trace_id, " +
      modelSel + ", " +
      inputSel + ", " +
      outputSel + ", " +
      "ROUND(duration_nano / 1000000.0, 1) AS duration_ms, " +
      "span_status_code AS status, " +
      finishSel + " " +
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
      return '<tr class="clickable" data-idx="' + i + '" onclick="toggleTraceDetail(this, \'' + escapeJSString(d.trace_id) + '\')">' +
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
    '<div class="conversation-section">' +
    '<div class="conv-tabs">' +
    '<button class="conv-tab active" onclick="switchConvTab(this, \x27conv\x27)">' + t('detail.conversation') + '</button>' +
    '<button class="conv-tab" onclick="switchConvTab(this, \x27rawlogs\x27)">' + t('detail.raw_logs') + '</button>' +
    '</div>' +
    '<div id="conv-tab-conv" class="conv-tab-content active">' +
    '<div id="conversation-messages" class="loading">' + t('empty.loading') + '</div></div>' +
    '<div id="conv-tab-rawlogs" class="conv-tab-content">' +
    '<div id="raw-logs-content" class="loading">' + t('empty.loading') + '</div></div>' +
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
    var cols = await genai_getTraceColumns();
    var modelSel = genai_traceAttrSelect(cols, 'span_attributes.gen_ai.request.model', 'model');
    var inputSel = genai_traceAttrSelect(cols, 'span_attributes.gen_ai.usage.input_tokens', 'input_tok');
    var outputSel = genai_traceAttrSelect(cols, 'span_attributes.gen_ai.usage.output_tokens', 'output_tok');
    var res = await query(
      "SELECT timestamp, span_id, parent_span_id, span_name, " +
      modelSel + ", " +
      inputSel + ", " +
      outputSel + ", " +
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

  // Load conversation from opentelemetry_logs by trace_id
  try {
    var convRes = await query(
      "SELECT body FROM opentelemetry_logs " +
      "WHERE trace_id = '" + tid + "' ORDER BY timestamp LIMIT 100"
    );
    var messages = parseConversation(rowsToObjects(convRes));
    if (messages.length > 0) {
      convEl.classList.remove('loading');
      convEl.innerHTML = messages.map(function(m) {
        var cls = m.role === 'assistant' ? 'assistant' :
                  m.role === 'system' ? 'system' :
                  (m.role === 'tool' || m.role === 'function') ? 'tool' : 'user';
        return '<div class="conv-msg conv-' + cls + '">' +
          '<div class="conv-role">' + escapeHTML(m.role.toUpperCase()) + '</div>' +
          '<div class="conv-content">' + escapeHTML(m.content) + '</div></div>';
      }).join('');
    } else {
      convEl.innerHTML = '<div class="loading">' + t('empty.conv_not_available') + '</div>';
    }
  } catch {
    convEl.innerHTML = '<div class="loading">' + t('empty.conv_not_available') + '</div>';
  }
}

// Infer span type from span data for badge display
function inferSpanType(s) {
  var n = s.span_name || '';
  if (n === 'openclaw.model.usage') return 'llm';
  if (n.indexOf('openclaw.message.') === 0) return 'msg';
  if (n.indexOf('openclaw.webhook.') === 0) return 'webhook';
  if (n === 'openclaw.session.stuck') return 'session';
  if (n.indexOf('sessions_spawn') >= 0) return 'spawn';
  if (n.indexOf('subagent') >= 0) return 'subagent';
  if (n.indexOf('tool_result') >= 0 || n.indexOf('tool_call') >= 0 ||
      n === 'Bash' || n === 'Execute') return 'tool';
  // GenAI fallback: span with model + token data is likely LLM call
  if (s.model && (s.input_tok || s.output_tok)) return 'llm';
  return '';
}

var SPAN_BADGE_LABELS = {
  llm: 'llm', msg: 'msg', webhook: 'webhook', session: 'session',
  spawn: 'spawn', subagent: 'subagent', tool: 'tool',
};

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

  // Compute span type counts for summary
  var typeCounts = {};
  flat.forEach(function(item) {
    var st = inferSpanType(item.span);
    if (st) typeCounts[st] = (typeCounts[st] || 0) + 1;
  });

  // Render
  var labelWidth = 260;
  container.innerHTML = '';
  container.className = 'waterfall';

  // Type summary bar (if any types detected)
  var typeKeys = Object.keys(typeCounts);
  if (typeKeys.length > 0) {
    var summary = document.createElement('div');
    summary.className = 'waterfall-type-summary';
    summary.innerHTML = typeKeys.map(function(k) {
      return '<span class="span-badge span-badge-' + k + '">' +
        escapeHTML(SPAN_BADGE_LABELS[k] || k) + ' ' + typeCounts[k] + '</span>';
    }).join(' ');
    container.appendChild(summary);
  }

  flat.forEach(function(item) {
    var s = item.span;
    var startMs = new Date(s.timestamp).getTime() - traceStart;
    var durMs = (Number(s.duration_nano) || 0) / 1e6;
    var leftPct = (startMs / totalMs * 100).toFixed(2);
    var widthPct = Math.max(durMs / totalMs * 100, 0.5).toFixed(2);
    var indent = item.depth * 16;
    var barClass = (s.status === 'STATUS_CODE_ERROR' || s.span_status_code === 'STATUS_CODE_ERROR') ? 'error' : 'ok';
    var name = s.span_name || s.model || 'span';

    // Span type badge
    var spanType = inferSpanType(s);
    var badgeHtml = spanType
      ? '<span class="span-badge span-badge-' + spanType + '">' + escapeHTML(SPAN_BADGE_LABELS[spanType]) + '</span> '
      : '';

    // Token inline display
    var tokenHtml = '';
    if (s.input_tok || s.output_tok) {
      tokenHtml = ' <span class="span-tokens">' +
        fmtNum(s.input_tok || 0) + '\u2192' + fmtNum(s.output_tok || 0) + '</span>';
    }

    var row = document.createElement('div');
    row.className = 'waterfall-row waterfall-row-clickable';
    row.setAttribute('role', 'button');
    row.setAttribute('tabindex', '0');
    row.setAttribute('aria-expanded', 'false');
    row.innerHTML =
      '<div class="waterfall-label" style="width:' + labelWidth + 'px;padding-left:' + indent + 'px" title="' + escapeHTML(name) + '">' +
        (item.depth > 0 ? '<span style="color:var(--text-dim);margin-right:4px">\u2514</span>' : '') +
        badgeHtml + escapeHTML(name) + tokenHtml +
      '</div>' +
      '<div class="waterfall-track">' +
        '<div class="waterfall-bar ' + barClass + '" style="left:' + leftPct + '%;width:' + widthPct + '%">' +
          (durMs >= 10 ? fmtDurMs(durMs) : '') +
        '</div>' +
      '</div>' +
      '<div class="waterfall-dur">' + fmtDurMs(durMs) + '</div>';

    // Click/keyboard to expand span detail
    (function(spanData, rowEl) {
      function toggleDetail() {
        var existing = rowEl.nextElementSibling;
        if (existing && existing.classList.contains('waterfall-span-detail')) {
          existing.remove();
          rowEl.setAttribute('aria-expanded', 'false');
          return;
        }
        // Remove any other open detail
        container.querySelectorAll('.waterfall-span-detail').forEach(function(d) {
          d.remove();
          var prev = d.previousElementSibling;
          if (prev) prev.setAttribute('aria-expanded', 'false');
        });

        var detail = document.createElement('div');
        detail.className = 'waterfall-span-detail';
        var pairs = [];
        pairs.push(['span_name', spanData.span_name]);
        if (spanData.span_id) pairs.push(['span_id', spanData.span_id]);
        if (spanData.parent_span_id) pairs.push(['parent_span_id', spanData.parent_span_id]);
        pairs.push(['status', spanData.status || spanData.span_status_code || 'OK']);
        if (spanData.model) pairs.push(['model', spanData.model]);
        if (spanData.channel) pairs.push(['channel', spanData.channel]);
        if (spanData.provider) pairs.push(['provider', spanData.provider]);
        if (spanData.outcome) pairs.push(['outcome', spanData.outcome]);
        if (spanData.session_key) pairs.push(['session_key', spanData.session_key]);
        if (spanData.message_id) pairs.push(['message_id', spanData.message_id]);
        if (spanData.input_tok) pairs.push(['input_tokens', spanData.input_tok]);
        if (spanData.output_tok) pairs.push(['output_tokens', spanData.output_tok]);
        if (spanData.cache_read) pairs.push(['cache_read', spanData.cache_read]);
        if (spanData.cache_write) pairs.push(['cache_write', spanData.cache_write]);
        if (spanData.total_tok) pairs.push(['total_tokens', spanData.total_tok]);
        if (spanData.duration_nano) pairs.push(['duration_ms', (Number(spanData.duration_nano) / 1e6).toFixed(1)]);
        pairs.push(['timestamp', spanData.timestamp]);
        var json = '{\n' + pairs.map(function(p) { return '  "' + p[0] + '": ' + JSON.stringify(p[1]); }).join(',\n') + '\n}';
        detail.innerHTML = '<pre class="waterfall-span-json">' + escapeHTML(json) + '</pre>';
        rowEl.after(detail);
        rowEl.setAttribute('aria-expanded', 'true');
      }
      rowEl.addEventListener('click', function(e) {
        e.stopPropagation();
        toggleDetail();
      });
      rowEl.addEventListener('keydown', function(e) {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          toggleDetail();
        }
      });
    })(s, row);

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

function switchConvTab(btn, tabName) {
  var section = btn.closest('.conversation-section');
  section.querySelectorAll('.conv-tab').forEach(function(t) { t.classList.remove('active'); });
  section.querySelectorAll('.conv-tab-content').forEach(function(c) { c.classList.remove('active'); });
  btn.classList.add('active');
  var target = section.querySelector('#conv-tab-' + tabName);
  if (target) target.classList.add('active');

  // Lazy-load raw logs on first click
  if (tabName === 'rawlogs') {
    var el = section.querySelector('#raw-logs-content');
    if (el && el.classList.contains('loading')) {
      var detailRow = section.closest('.trace-detail-row');
      if (detailRow && detailRow.dataset.traceId) {
        loadRawLogs(detailRow.dataset.traceId, el);
      }
    }
  }
}

async function loadRawLogs(traceId, el) {
  try {
    var tid = escapeSQLString(traceId);
    var res = await query(
      "SELECT timestamp, scope_name, body " +
      "FROM opentelemetry_logs " +
      "WHERE trace_id = '" + tid + "' ORDER BY timestamp LIMIT 200"
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      el.innerHTML = '<div class="loading">' + t('empty.no_raw_logs') + '</div>';
      el.classList.remove('loading');
      return;
    }
    el.classList.remove('loading');
    el.innerHTML = '<div class="raw-logs-list">' + data.map(function(d) {
      var bodyPreview = (d.body || '').substring(0, 500);
      if ((d.body || '').length > 500) bodyPreview += '...';
      return '<div class="raw-log-entry">' +
        '<div class="raw-log-meta">' +
        '<span class="raw-log-ts">' + fmtTime(d.timestamp) + '</span>' +
        (d.scope_name ? '<span class="badge badge-info">' + escapeHTML(d.scope_name) + '</span>' : '') +
        '</div>' +
        '<pre class="raw-log-body">' + escapeHTML(bodyPreview) + '</pre>' +
        '</div>';
    }).join('') + '</div>';
  } catch {
    el.innerHTML = '<div class="loading">' + t('error.load_raw_logs') + '</div>';
    el.classList.remove('loading');
  }
}

// Parse conversation messages from log body rows, deduplicating cumulative re-sends.
//
// Two data formats exist:
//  1) Cumulative arrays (GenAI semantic convention): each prompt event body is
//     [{role,content},...] containing the FULL conversation so far. The last array
//     is the most complete — use it as the base, then append trailing non-array
//     events (e.g. final completion).
//  2) Individual events (openai_v2): each span re-sends prior input messages.
//     Completion outputs (with finish_reason) are unique per span and always kept.
//     Input events (prompts, tool results) are deduplicated by role+content to
//     remove cumulative re-sends.
function parseConversation(logRows) {
  // First pass: find the last array body (most complete conversation state)
  var lastArrayIdx = -1;
  var lastArray = null;
  for (var i = 0; i < logRows.length; i++) {
    if (!logRows[i].body) continue;
    try {
      var p = JSON.parse(logRows[i].body);
      if (Array.isArray(p)) { lastArrayIdx = i; lastArray = p; }
    } catch (_) { /* ignore parse error */ }
  }

  var messages = [];

  if (lastArray) {
    // Cumulative array mode: last array = complete conversation base
    lastArray.forEach(function(msg) {
      if (msg && typeof msg === 'object') {
        var c = typeof msg.content === 'string' ? msg.content :
                msg.content != null ? JSON.stringify(msg.content) : '';
        if (c) messages.push({ role: (msg.role || 'user').toLowerCase(), content: c });
      }
    });
    // Append non-array events after the last array (e.g. final completion)
    for (var j = lastArrayIdx + 1; j < logRows.length; j++) {
      if (!logRows[j].body) continue;
      try {
        var q = JSON.parse(logRows[j].body);
        if (!q || typeof q !== 'object' || Array.isArray(q)) continue;
        if (q.message && typeof q.message === 'object') {
          var ct = typeof q.message.content === 'string' ? q.message.content : '';
          if (ct) messages.push({ role: (q.message.role || 'assistant').toLowerCase(), content: ct });
        }
      } catch (_) { /* ignore parse error */ }
    }
  } else {
    // Individual event mode: completion outputs always kept, inputs deduplicated
    var seenInputs = {};
    logRows.forEach(function(row) {
      if (!row.body) return;
      var parsed;
      try { parsed = JSON.parse(row.body); } catch (_) { return; }
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return;

      // Completion output (has finish_reason) — unique per span, always keep
      if (parsed.finish_reason != null && parsed.message && typeof parsed.message === 'object') {
        var content = typeof parsed.message.content === 'string' ? parsed.message.content : '';
        if (content) messages.push({ role: (parsed.message.role || 'assistant').toLowerCase(), content: content });
        return;
      }

      // Tool call definition only — skip
      if (parsed.tool_calls && parsed.content == null) return;

      // Input events: dedup by role+content to handle cumulative re-sends
      var role, c;
      if (parsed.content != null && parsed.id) {
        role = 'tool';
        c = typeof parsed.content === 'string' ? parsed.content : JSON.stringify(parsed.content);
      } else if (parsed.content != null) {
        role = (parsed.role || 'user').toLowerCase();
        c = typeof parsed.content === 'string' ? parsed.content : JSON.stringify(parsed.content);
      } else {
        return;
      }
      if (!c) return;
      var key = role + '\0' + c;
      if (!seenInputs[key]) {
        seenInputs[key] = true;
        messages.push({ role: role, content: c });
      }
    });
  }

  return messages;
}

// Extract role + text from a single log body for search preview
function parseSearchBody(body) {
  if (!body) return { role: 'unknown', content: '' };
  try {
    var parsed = JSON.parse(body);
    if (Array.isArray(parsed) && parsed.length) {
      var last = parsed[parsed.length - 1];
      if (last && last.content) {
        return {
          role: (last.role || 'user').toLowerCase(),
          content: typeof last.content === 'string' ? last.content : JSON.stringify(last.content),
        };
      }
    } else if (parsed && typeof parsed === 'object') {
      if (parsed.message && parsed.message.content) {
        return {
          role: (parsed.message.role || 'assistant').toLowerCase(),
          content: typeof parsed.message.content === 'string' ? parsed.message.content : JSON.stringify(parsed.message.content),
        };
      }
      if (parsed.content != null) {
        var c = typeof parsed.content === 'string' ? parsed.content : JSON.stringify(parsed.content);
        var r = parsed.id ? 'tool' : (parsed.role || 'user');
        return { role: r.toLowerCase(), content: c };
      }
    }
  } catch (_) { /* not JSON */ }
  return { role: 'unknown', content: body };
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
  var genaiWhere = genai_systemWhere(await genai_getTraceColumns());
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
      "WHERE " + genaiWhere + " " +
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
  var genaiWhere = genai_systemWhere(await genai_getTraceColumns());
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
      "WHERE " + genaiWhere + " " +
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
      return '<tr class="clickable" onclick="switchToTrace(\'' + escapeJSString(d.trace_id) + '\')">' +
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
  var genaiWhere = genai_systemWhere(await genai_getTraceColumns());
  try {
    var res = await query(
      "SELECT \"span_attributes.gen_ai.response.finish_reasons\" AS reason, " +
      "COUNT(1) AS cnt " +
      "FROM opentelemetry_traces " +
      "WHERE " + genaiWhere + " " +
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
  var genaiWhere = genai_systemWhere(await genai_getTraceColumns());
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
      "WHERE " + genaiWhere + " " +
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
      "SELECT timestamp, trace_id, body " +
      "FROM opentelemetry_logs " +
      "WHERE matches_term(body, '" + safeTerm + "') " +
      "  AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "ORDER BY timestamp DESC LIMIT 50"
    );
    var data = rowsToObjects(res);
    if (!data.length) {
      el.innerHTML = '<div class="loading">' + t('empty.no_results') + '</div>';
      return;
    }
    el.innerHTML = data.map(function(d) {
      var p = parseSearchBody(d.body);
      var preview = (p.content || '').substring(0, 200);
      if ((p.content || '').length > 200) preview += '...';
      return '<div class="search-result-item" onclick="switchToTrace(\'' + escapeJSString(d.trace_id) + '\')">' +
      '<div class="search-result-meta">' +
      '<span>' + fmtTime(d.timestamp) + '</span>' +
      '<span class="badge ' + (p.role === 'assistant' ? 'badge-ok' : 'badge-info') + '">' + escapeHTML(p.role) + '</span>' +
      '<span>' + escapeHTML(d.trace_id) + '</span>' +
      '</div>' +
      '<div class="search-result-content">' + escapeHTML(preview) + '</div>' +
      '</div>';
    }).join('');
  } catch (_err) {
    // Table may not exist or matches_term not supported
    el.innerHTML = '<div class="loading">' + t('error.conv_search') + '</div>';
  }
}

async function loadAnomalies() {
  var genaiWhere = genai_systemWhere(await genai_getTraceColumns());
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
      "WHERE " + genaiWhere + " " +
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
      return '<div class="anomaly-item" onclick="switchToTrace(\'' + escapeJSString(d.trace_id) + '\')">' +
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
  var genaiWhere = genai_systemWhere(await genai_getTraceColumns());
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
      "WHERE " + genaiWhere + " " +
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
      return '<tr class="clickable" onclick="switchToTrace(\'' + escapeJSString(d.trace_id) + '\')">' +
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
  var genaiWhere = genai_systemWhere(await genai_getTraceColumns());
  try {
    var res = await query(
      "SELECT trace_id, " +
      "CAST(\"span_attributes.gen_ai.usage.input_tokens\" AS DOUBLE) AS input_tok, " +
      "timestamp " +
      "FROM opentelemetry_traces " +
      "WHERE " + genaiWhere + " " +
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

  // Injection count is set by loadInjectionAlerts()

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
      return '<tr class="clickable" onclick="switchToTrace(\'' + escapeJSString(d.trace_id) + '\')">' +
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

// Prompt injection patterns — regex + label + severity
var injectionPatterns = [
  { re: /ignore\s+(all\s+)?(previous|prior|above)\s+(instructions|prompts|rules|context)/i, label: 'Instruction override', severity: 'high' },
  { re: /disregard\s+(all\s+)?(previous|prior|above)/i, label: 'Instruction override', severity: 'high' },
  { re: /forget\s+(your|all|previous)\s+(instructions|rules|guidelines)/i, label: 'Instruction override', severity: 'high' },
  { re: /(reveal|show|output|print|display|repeat)\s+(your\s+)?(system\s+prompt|instructions|rules|initial\s+prompt)/i, label: 'Prompt extraction', severity: 'medium' },
  { re: /what\s+(is|are)\s+your\s+(system\s+prompt|instructions|rules)/i, label: 'Prompt extraction', severity: 'medium' },
  { re: /you\s+are\s+now\s+(a|an|the|in)\s/i, label: 'Role hijack', severity: 'medium' },
  { re: /pretend\s+(you\s+are|to\s+be)\s/i, label: 'Role hijack', severity: 'low' },
  { re: /act\s+as\s+(a|an|if\s+you\s+were)\s/i, label: 'Role hijack', severity: 'low' },
  { re: /\bjailbreak/i, label: 'Jailbreak keyword', severity: 'high' },
  { re: /\bDAN\b.*\bmode\b/i, label: 'DAN mode', severity: 'high' },
  { re: /do\s+anything\s+now/i, label: 'DAN mode', severity: 'high' },
  { re: /(ignore|bypass|disable)\s+(all\s+)?(safety|content)\s*(filter|policy|guard|check)/i, label: 'Safety bypass', severity: 'high' },
  { re: /\[\s*system\s*\]/i, label: 'System tag injection', severity: 'medium' },
  { re: /<\s*\|?\s*system\s*\|?\s*>/i, label: 'System tag injection', severity: 'medium' },
];

async function loadInjectionAlerts() {
  var el = document.getElementById('injection-alerts');
  var countEl = document.getElementById('sec-injection-count');
  el.innerHTML = '<div class="loading">' + t('empty.loading') + '</div>';

  try {
    // Fetch recent log bodies that could contain user messages
    var res = await query(
      "SELECT timestamp, trace_id, body " +
      "FROM opentelemetry_logs " +
      "WHERE trace_id != '' AND body IS NOT NULL " +
      "AND timestamp > NOW() - INTERVAL '" + intervalSQL() + "' " +
      "ORDER BY timestamp DESC LIMIT 500"
    );
    var data = rowsToObjects(res);
    var alerts = [];

    data.forEach(function(d) {
      if (!d.body) return;
      var parsed;
      try { parsed = JSON.parse(d.body); } catch (_) { return; }

      // Extract user message content only
      var texts = [];
      if (Array.isArray(parsed)) {
        parsed.forEach(function(msg) {
          if (msg && (msg.role === 'user' || !msg.role) && msg.content) texts.push(msg.content);
        });
      } else if (parsed && typeof parsed === 'object') {
        // Prompt message: {"content":"..."} without "message" or "id" wrapper
        if (!parsed.message && !parsed.id && parsed.content) {
          texts.push(typeof parsed.content === 'string' ? parsed.content : '');
        }
      }

      texts.forEach(function(text) {
        if (!text) return;
        injectionPatterns.forEach(function(p) {
          if (p.re.test(text)) {
            // Avoid duplicate alerts for same trace + pattern
            var key = d.trace_id + '\0' + p.label;
            if (!alerts.some(function(a) { return a.key === key; })) {
              alerts.push({
                key: key,
                timestamp: d.timestamp,
                trace_id: d.trace_id,
                label: p.label,
                severity: p.severity,
                content: text,
              });
            }
          }
        });
      });
    });

    // Sort: high → medium → low
    var sev = { high: 0, medium: 1, low: 2 };
    alerts.sort(function(a, b) { return (sev[a.severity] || 9) - (sev[b.severity] || 9); });

    countEl.textContent = String(alerts.length);

    if (!alerts.length) {
      el.innerHTML = '<div class="loading">' + t('empty.no_injection') + '</div>';
      return;
    }

    el.innerHTML = alerts.map(function(a) {
      var severityClass = a.severity === 'high' ? 'badge-error' : a.severity === 'medium' ? 'badge-warn' : 'badge-info';
      var preview = (a.content || '').substring(0, 150);
      if ((a.content || '').length > 150) preview += '...';
      return '<div class="anomaly-item' + (a.severity !== 'high' ? ' warn' : '') + '" onclick="switchToTrace(\'' + escapeJSString(a.trace_id) + '\')">' +
        '<div class="anomaly-reason">' +
        '<span class="badge ' + severityClass + '">' + escapeHTML(a.severity.toUpperCase()) + '</span> ' +
        escapeHTML(a.label) + '</div>' +
        '<div style="font-size:13px;margin-top:4px">' +
        fmtTime(a.timestamp) + ' &middot; ' + escapeHTML(preview) +
        '</div></div>';
    }).join('');
  } catch (_err) {
    countEl.textContent = '\u2014';
    el.innerHTML = '<div class="loading">' + t('empty.injection_na') + '</div>';
  }
}

async function loadToolTimeline() {
  var tbody = document.getElementById('tool-timeline-body');
  var cols = await genai_getTraceColumns();
  try {
    var res = await query(
      "SELECT timestamp, trace_id, span_name, span_status_code, " +
      "ROUND(duration_nano / 1000000.0, 1) AS duration_ms " +
      "FROM opentelemetry_traces " +
      "WHERE " + genai_systemWhereNull(cols) + " " +
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
      return '<tr class="clickable" onclick="switchToTrace(\'' + escapeJSString(d.trace_id) + '\')">' +
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
