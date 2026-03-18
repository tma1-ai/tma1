// metrics-explorer.js — Shared Metrics Explorer with PromQL support
// Depends on: core.js (currentTimeRange, escapeHTML, fmtNum), chart.js (makeUPlotOpts, getThemeColors, chartInstances), i18n.js (t())

var meState = {};

var ME_MAX_SERIES = 20;
var ME_MAX_LABEL_VALUES = 10;
// High-cardinality or PII label patterns — not useful for exploration
var ME_HIGH_CARDINALITY_RE = /(_id|_uuid|_key|_email|_token|_secret|_password)$|^(id|uuid|trace_id|span_id|session_id|user_id|organization_id|instance|email)$/;

// Per-view metric prefix filters — only show relevant metrics in each dashboard
var ME_VIEW_PREFIXES = {
  'cc-metrics':  ['claude_code_'],
  'cdx-metrics': ['codex_'],
  'oc-metrics':  ['openclaw_'],
};
// 'metrics' (GenAI/Traces view) has no entry → shows everything except other views' prefixes
var ME_ALL_VIEW_PREFIXES = ['claude_code_', 'codex_', 'openclaw_'];

function meRangeVector() {
  var m = { '1h': '5m', '6h': '5m', '24h': '15m', '7d': '1h', '30d': '6h' };
  return m[currentTimeRange] || '5m';
}

function meQueryParams() {
  var now = Math.floor(Date.now() / 1000);
  var rangeMap = { '1h': 3600, '6h': 21600, '24h': 86400, '7d': 604800, '30d': 2592000 };
  var stepMap = { '1h': 15, '6h': 60, '24h': 300, '7d': 900, '30d': 3600 };
  var range = rangeMap[currentTimeRange] || 86400;
  return { start: now - range, end: now, step: stepMap[currentTimeRange] || 300 };
}

async function initMetricsExplorer(prefix) {
  var section = document.getElementById(prefix + '-explorer-section');
  if (!section) return;

  // If already initialized with metrics, just re-run the current query (time range changed)
  var existing = meState[prefix];
  if (existing && existing.metrics.length) {
    var inputEl = document.getElementById(prefix + '-promql-input');
    if (inputEl && inputEl.value.trim()) {
      meRunQuery(prefix);
    }
    return;
  }

  meState[prefix] = { metrics: [], currentMetric: '', selector: '', labels: {} };

  try {
    var resp = await fetch('/api/prom/label/__name__/values');
    if (!resp.ok) throw new Error('HTTP ' + resp.status);
    var json = await resp.json();
    if (json.status !== 'success' || !json.data) throw new Error('bad response');

    var metrics = json.data.filter(function(name) {
      if (name.startsWith('greptime_') || name.startsWith('numbers_') ||
          name.startsWith('scripts_') || name.startsWith('__')) return false;
      var viewPrefixes = ME_VIEW_PREFIXES[prefix];
      if (viewPrefixes) {
        // View-specific: only show metrics matching this view's prefixes
        return viewPrefixes.some(function(p) { return name.startsWith(p); });
      }
      // GenAI/Traces view: show everything except other views' agent-specific metrics
      return !ME_ALL_VIEW_PREFIXES.some(function(p) { return name.startsWith(p); });
    });

    if (!metrics.length) {
      section.style.display = 'none';
      return;
    }

    meState[prefix].metrics = metrics;
    section.style.display = 'block';

    var select = document.getElementById(prefix + '-metric-select');
    if (!select) return;
    select.innerHTML = '<option value="">' + t('filter.select_metric') + '</option>' +
      metrics.map(function(m) {
        return '<option value="' + escapeHTML(m) + '">' + escapeHTML(m) + '</option>';
      }).join('');
  } catch {
    section.style.display = 'none';
  }
}

async function meOnMetricSelect(prefix) {
  var select = document.getElementById(prefix + '-metric-select');
  var metric = select ? select.value : '';
  if (!metric) return;

  var st = meState[prefix];
  if (!st) return;
  st.currentMetric = metric;
  st.selector = metric;
  st.labels = {};

  // Load label names for this metric
  var tagsEl = document.getElementById(prefix + '-label-tags');
  var quickEl = document.getElementById(prefix + '-quick-actions');
  var inputEl = document.getElementById(prefix + '-promql-input');
  if (tagsEl) tagsEl.innerHTML = '';
  if (quickEl) quickEl.innerHTML = '';

  try {
    var labelsResp = await fetch('/api/prom/labels?match[]=' + encodeURIComponent(metric));
    var labelsJson = await labelsResp.json();
    var labelNames = (labelsJson.data || []).filter(function(l) {
      return l !== '__name__' && l !== '__field__';
    });

    if (labelNames.length) {
      // Fetch values for each label in parallel
      var valueResults = await Promise.all(labelNames.map(function(l) {
        return fetch('/api/prom/label/' + encodeURIComponent(l) + '/values?match[]=' + encodeURIComponent(metric))
          .then(function(r) { return r.json(); });
      }));

      labelNames.forEach(function(name, i) {
        var vals = (valueResults[i].data || []).slice(0, 20);
        st.labels[name] = vals;
      });

      // Render label tags — filter out high-cardinality, PII, and single-value labels
      if (tagsEl) {
        var html = '';
        labelNames.forEach(function(name) {
          if (ME_HIGH_CARDINALITY_RE.test(name)) return;
          var vals = st.labels[name] || [];
          if (vals.length <= 1 || vals.length > ME_MAX_LABEL_VALUES) return;
          vals.forEach(function(val) {
            html += '<span class="me-label-tag" onclick="meClickLabel(\'' + escapeJSString(prefix) + '\',\'' +
              escapeJSString(name) + '\',\'' + escapeJSString(val) + '\')">' +
              escapeHTML(name) + '=' + escapeHTML(val) + '</span>';
          });
        });
        tagsEl.innerHTML = html;
      }
    }

    // Render quick actions — these call meQuickAction() which reads st.selector at click time
    // Pick first low-cardinality label for sum by()
    var firstLabel = Object.keys(st.labels).find(function(name) {
      return !ME_HIGH_CARDINALITY_RE.test(name) && (st.labels[name] || []).length <= ME_MAX_LABEL_VALUES;
    }) || '';
    var actions = [
      { label: 'rate', key: 'rate' },
      { label: 'increase', key: 'increase' },
    ];
    if (firstLabel) {
      actions.push({ label: 'sum by(' + firstLabel + ')', key: 'sum' });
    }
    actions.push({ label: 'avg_over_time', key: 'avg_over_time' });
    actions.push({ label: 'topk(5)', key: 'topk' });

    if (quickEl) {
      quickEl.innerHTML = actions.map(function(a) {
        return '<button class="filter-btn" onclick="meQuickAction(\'' + escapeJSString(prefix) + '\', \'' +
          a.key + '\')">' + escapeHTML(a.label) + '</button>';
      }).join('');
    }

    // Set default query and execute
    if (inputEl) inputEl.value = st.selector;
    meRunQuery(prefix);
  } catch {
    // Label discovery failed, still allow manual PromQL
    if (inputEl) inputEl.value = st.selector;
    meRunQuery(prefix);
  }
}

function meClickLabel(prefix, name, val) {
  var st = meState[prefix];
  if (!st) return;
  var inputEl = document.getElementById(prefix + '-promql-input');
  if (!inputEl) return;
  var filter = name + '="' + val + '"';

  // Update the base selector (metric + label matchers)
  var selector = st.selector || st.currentMetric;
  if (selector.indexOf('{') !== -1) {
    selector = selector.replace(/}/, ', ' + filter + '}');
  } else {
    selector = selector + '{' + filter + '}';
  }
  st.selector = selector;
  inputEl.value = selector;
  meRunQuery(prefix);
}

function meQuickAction(prefix, action) {
  var st = meState[prefix];
  if (!st) return;
  var selector = st.selector || st.currentMetric;
  var rv = meRangeVector();
  var firstLabel = Object.keys(st.labels)[0] || '';
  var promql;
  switch (action) {
    case 'rate': promql = 'rate(' + selector + '[' + rv + '])'; break;
    case 'increase': promql = 'increase(' + selector + '[' + rv + '])'; break;
    case 'sum': promql = 'sum by (' + firstLabel + ') (' + selector + ')'; break;
    case 'avg_over_time': promql = 'avg_over_time(' + selector + '[' + rv + '])'; break;
    case 'topk': promql = 'topk(5, ' + selector + ')'; break;
    default: promql = selector;
  }
  meSetQuery(prefix, promql);
}

function meSetQuery(prefix, promql) {
  var inputEl = document.getElementById(prefix + '-promql-input');
  if (inputEl) inputEl.value = promql;
  meRunQuery(prefix);
}

async function meRunQuery(prefix) {
  var inputEl = document.getElementById(prefix + '-promql-input');
  var chartArea = document.getElementById(prefix + '-chart-area');
  var legendEl = document.getElementById(prefix + '-legend');
  if (!inputEl || !chartArea) return;

  var promql = inputEl.value.trim();
  if (!promql) return;

  chartArea.innerHTML = '<div class="chart-empty">' + t('empty.loading') + '</div>';
  if (legendEl) legendEl.innerHTML = '';

  try {
    var params = meQueryParams();
    var body = 'query=' + encodeURIComponent(promql) +
      '&start=' + params.start + '&end=' + params.end + '&step=' + params.step;
    var resp = await fetch('/api/prom/query_range', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: body,
    });
    var json = await resp.json();

    if (json.status === 'error') {
      chartArea.innerHTML = '<div class="chart-empty">' + t('empty.promql_error') + ': ' + escapeHTML(json.error || 'unknown error') + '</div>';
      return;
    }

    if (!json.data || !json.data.result) {
      chartArea.innerHTML = '<div class="chart-empty">' + t('empty.no_metrics') + '</div>';
      return;
    }

    meRenderChart(prefix, json);
  } catch (err) {
    chartArea.innerHTML = '<div class="chart-empty">' + t('empty.promql_error') + ': ' + escapeHTML(err.message) + '</div>';
  }
}

function meRenderChart(prefix, promResult) {
  var chartArea = document.getElementById(prefix + '-chart-area');
  var legendEl = document.getElementById(prefix + '-legend');
  var series = promResult.data.result;

  if (!series.length) {
    chartArea.innerHTML = '<div class="chart-empty">' + t('empty.no_metrics') + '</div>';
    if (legendEl) legendEl.innerHTML = '';
    return;
  }

  var truncated = false;
  if (series.length > ME_MAX_SERIES) {
    truncated = true;
    series = series.slice(0, ME_MAX_SERIES);
  }

  var tc = getThemeColors();
  var colors = [tc.blue, tc.green, tc.orange, tc.purple, tc.red, tc.yellow];

  // query_range guarantees aligned timestamps across all series
  var timestamps = series[0].values.map(function(v) { return Number(v[0]); });
  var uData = [timestamps];
  var uSeries = [{}];
  var legendItems = [];

  // Find discriminating labels — labels whose values differ across series,
  // excluding high-cardinality labels that produce unreadable legends
  var allKeys = {};
  series.forEach(function(s) {
    Object.keys(s.metric).forEach(function(k) { if (k !== '__name__') allKeys[k] = true; });
  });
  var discrimKeys = Object.keys(allKeys).filter(function(k) {
    if (ME_HIGH_CARDINALITY_RE.test(k)) return false;
    var vals = {};
    series.forEach(function(s) { vals[s.metric[k] || ''] = true; });
    return Object.keys(vals).length > 1;
  });
  // Fallback: if nothing left, try all non-high-cardinality keys
  if (!discrimKeys.length) {
    discrimKeys = Object.keys(allKeys).filter(function(k) {
      return !ME_HIGH_CARDINALITY_RE.test(k);
    });
  }
  // Last resort: use all keys but truncate values
  if (!discrimKeys.length) discrimKeys = Object.keys(allKeys);

  series.forEach(function(s, i) {
    uData.push(s.values.map(function(v) { return Number(v[1]) || null; }));
    var labelStr = discrimKeys
      .map(function(k) { return k + '=' + (s.metric[k] || ''); })
      .join(', ');
    var name = labelStr || s.metric.__name__ || 'value';
    if (name.length > 80) name = name.substring(0, 77) + '...';
    var color = colors[i % colors.length];
    uSeries.push({ label: name, stroke: color, width: 2 });
    legendItems.push({ name: name, color: color });
  });

  chartArea.innerHTML = '';
  var chartDiv = document.createElement('div');
  chartArea.appendChild(chartDiv);

  if (truncated) {
    var notice = document.createElement('div');
    notice.style.cssText = 'font-size:12px;color:var(--text-muted);margin-bottom:8px';
    notice.textContent = t('empty.series_limit').replace('{n}', String(promResult.data.result.length));
    chartArea.insertBefore(notice, chartDiv);
  }

  var width = chartArea.clientWidth - 16;
  var opts = makeUPlotOpts('', uSeries, width, function(v) { return v == null ? '' : fmtNum(v); });
  var chartKey = prefix + '-explorer-chart';
  if (chartInstances[chartKey]) chartInstances[chartKey].destroy();
  chartInstances[chartKey] = new uPlot(opts, uData, chartDiv);

  // Render legend
  if (legendEl) {
    legendEl.innerHTML = legendItems.map(function(item) {
      return '<span class="me-legend-item">' +
        '<span class="me-legend-dot" style="background:' + item.color + '"></span>' +
        escapeHTML(item.name) + '</span>';
    }).join('');
  }
}
