// core.js — global constants + utility functions
// Depends on: i18n.js (t())

var API = '/api/query';
var currentTimeRange = '24h';

function supportedAgentWhere() {
  return "agent_source IN ('claude_code','codex','copilot_cli','opencode')";
}

// Codex reports traces as codex_cli_rs (CLI), codex-app-server (app server)
// or codex_exec (exec mode).
function codexTraceServiceWhere() {
  return "service_name IN ('codex_cli_rs','codex-app-server','codex_exec')";
}

function intervalSQL() {
  var m = { '15m': '15 minutes', '30m': '30 minutes', '1h': '1 hour', '6h': '6 hours', '24h': '1 day', '7d': '7 days', '30d': '30 days', '60d': '60 days' };
  return m[currentTimeRange] || '1 day';
}

function chartBucket() {
  var m = { '15m': '1 minute', '30m': '1 minute', '1h': '5 minutes', '6h': '5 minutes', '24h': '5 minutes', '7d': '15 minutes', '30d': '1 hour', '60d': '2 hours' };
  return m[currentTimeRange] || '5 minutes';
}

// Session query row limit scaled by time range.
// Typical agent usage: ~1k events/day. These limits should cover all but
// the most extreme workloads, while keeping browser performance reasonable.
function sessionQueryLimit() {
  var m = { '1h': 5000, '6h': 10000, '24h': 20000, '7d': 50000, '30d': 100000, '60d': 200000 };
  return m[currentTimeRange] || 20000;
}

// Limit in-flight SQL queries to avoid GreptimeDB memory spikes on wide ranges.
// Excess queries wait in FIFO; nothing is dropped.
var QUERY_CONCURRENCY = 4; // overridden by /api/settings on init
var queryInFlight = 0;
var queryQueue = [];

function queryAcquire() {
  return new Promise(function(resolve) {
    if (queryInFlight < QUERY_CONCURRENCY) {
      queryInFlight++;
      resolve();
    } else {
      queryQueue.push(resolve);
    }
  });
}

function queryRelease() {
  if (queryQueue.length > 0) {
    var next = queryQueue.shift();
    next();
  } else {
    queryInFlight--;
  }
}

async function query(sql) {
  await queryAcquire();
  try {
    var r = await fetch(API, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ sql: sql }),
    });
    if (!r.ok) {
      var msg = 'HTTP ' + r.status;
      try {
        var body = await r.text();
        if (body) msg += ': ' + body.slice(0, 200);
      } catch { /* keep status-only message */ }
      throw new Error(msg);
    }
    var result = await r.json();
    if (result.error || result.code) throw new Error(result.error || ("Database error " + result.code));
    return result;
  } finally {
    queryRelease();
  }
}

function rows(res) {
  return res?.output?.[0]?.records?.rows || [];
}

function cols(res) {
  return res?.output?.[0]?.records?.schema?.column_schemas?.map(function(c) { return c.name; }) || [];
}

function rowsToObjects(res) {
  var c = cols(res), r = rows(res);
  return r.map(function(row) {
    var o = {};
    c.forEach(function(name, i) { o[name] = row[i]; });
    return o;
  });
}

function fmtNum(n) {
  if (n == null) return '\u2014';
  if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
  if (n >= 1000) return (n / 1000).toFixed(1) + 'k';
  return String(Math.round(n));
}

function fmtCost(n) {
  if (n == null) return '\u2014';
  return '$' + Number(n).toFixed(4);
}

function fmtMs(ns) {
  if (ns == null) return '\u2014';
  var s = ns / 1e9;
  return s.toFixed(1) + 's';
}

function fmtDurMs(ms) {
  if (ms == null) return '\u2014';
  if (ms >= 1000) return (ms / 1000).toFixed(1) + 's';
  return Math.round(ms) + 'ms';
}

function fmtDurMsPrecise(ms) {
  if (ms == null) return '\u2014';
  return (ms / 1000).toFixed(3) + 's';
}

function tsToMs(v) {
  if (typeof v === 'number') {
    if (v > 1e18) return v / 1e6;   // nanoseconds
    if (v > 1e15) return v / 1e3;   // microseconds
    if (v > 1e12) return v;          // milliseconds
    return v * 1000;                 // seconds
  }
  return new Date(v).getTime();      // ISO string
}

function fmtTime(ts) {
  if (!ts) return '';
  return new Date(tsToMs(ts)).toLocaleString();
}

// Deep-parse stringified JSON values in an attributes object for display.
// e.g. tool_parameters: "{\"cmd\":\"ls\"}" → tool_parameters: {cmd:"ls"}
function deepParseAttrs(obj) {
  if (!obj || typeof obj !== 'object') return obj;
  var out = {};
  Object.keys(obj).forEach(function(k) {
    var v = obj[k];
    if (typeof v === 'string' && v.length > 1 && (v[0] === '{' || v[0] === '[')) {
      try { out[k] = JSON.parse(v); return; } catch (_) { /* not JSON */ }
    }
    out[k] = v;
  });
  return out;
}

function escapeHTML(s) {
  if (!s) return '';
  if (typeof s !== 'string') s = Array.isArray(s) ? s.join(', ') : String(s);
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

function escapeJSString(s) {
  if (!s) return '';
  return String(s)
    .replace(/\\/g, '\\\\')
    .replace(/'/g, '\\x27')
    .replace(/"/g, '\\x22')
    .replace(/&/g, '\\x26')
    .replace(/\n/g, '\\n')
    .replace(/\r/g, '\\r')
    .replace(/</g, '\\x3c')
    .replace(/>/g, '\\x3e');
}

function escapeSQLString(s) {
  return s ? s.replace(/'/g, "''") : '';
}

// ===================================================================
// Dynamic model pricing (loaded from tma1_model_pricing table)
// ===================================================================
var modelPricing = [];
var pricingLoaded = false;

function pricingNumber(v) {
  return v == null ? null : Number(v);
}

async function loadPricing() {
  if (pricingLoaded) return;
  try {
    var res = await query(
      "SELECT model_pattern, service_tier, input_price, output_price, cache_read_price, " +
      "cache_write_short_price, cache_write_long_price, max_input_tokens " +
      "FROM tma1_model_pricing ORDER BY priority"
    );
    var data = rowsToObjects(res);
    if (data.length) {
      modelPricing = data.map(function(d) {
        return {
          p: d.model_pattern, tier: d.service_tier,
          i: Number(d.input_price), o: Number(d.output_price),
          cacheRead: pricingNumber(d.cache_read_price),
          writeShort: pricingNumber(d.cache_write_short_price),
          writeLong: pricingNumber(d.cache_write_long_price),
          maxInput: pricingNumber(d.max_input_tokens),
        };
      });
      pricingLoaded = true;
    }
  } catch (e) {
    // Missing pricing remains unknown; a later refresh retries the query.
  }
}

function modelPricePattern(model) {
  return '^' + model.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '(-[0-9]{8}|-[0-9]{4}-[0-9]{2}-[0-9]{2})?$';
}

// Maps a reported service tier onto catalog tiers. Missing or unrecognized
// tiers return null so the usage stays unpriced.
function normalizeServiceTier(tier) {
  if (tier == null || tier === '') return null;
  tier = String(tier).toLowerCase();
  if (tier === 'default' || tier === 'auto' || tier === 'standard') return 'standard';
  if (tier === 'priority' || tier === 'fast') return 'priority';
  return null;
}

// Standard-tier list price for input and output only. Used where usage has no
// tier or cache counters (GenAI traces).
function costCaseSQL(modelExpr, inputExpr, outputExpr) {
  var parts = modelPricing.filter(function(m) { return m.tier === 'standard'; }).map(function(m) {
    var cond = "regexp_like(" + modelExpr + ", '" + modelPricePattern(m.p).replace(/'/g, "''") + "')";
    if (m.maxInput != null) cond += " AND CAST(" + inputExpr + " AS BIGINT) <= " + m.maxInput;
    return "WHEN " + cond + " THEN " +
      "CAST(" + inputExpr + " AS DOUBLE)*" + m.i + "/1000000.0+" +
      "CAST(" + outputExpr + " AS DOUBLE)*" + m.o + "/1000000.0";
  });
  if (!parts.length) return "CAST(NULL AS DOUBLE)";
  return "(CASE " + parts.join(" ") + " ELSE CAST(NULL AS DOUBLE) END)";
}

// Per-request cost with cache pricing. e holds SQL expressions:
// model, tier (catalog tier or NULL), fresh (uncached input), output, cacheRead,
// writeShort, writeLong, writeUnsplit (writes of unknown duration), totalInput.
// A token class without a price, unsplit writes, or a prompt over the
// catalog limit makes the result NULL.
function usageCostSQL(e) {
  function term(tokens, price) {
    if (price == null) return "(CASE WHEN " + tokens + " > 0 THEN CAST(NULL AS DOUBLE) ELSE 0 END)";
    return "CAST(" + tokens + " AS DOUBLE)*" + price + "/1000000.0";
  }
  var parts = modelPricing.map(function(m) {
    var cond = "regexp_like(" + e.model + ", '" + modelPricePattern(m.p).replace(/'/g, "''") + "') AND " +
      e.tier + " = '" + m.tier + "'";
    if (m.maxInput != null) cond += " AND " + e.totalInput + " <= " + m.maxInput;
    return "WHEN " + cond + " THEN " + [
      term(e.fresh, m.i), term(e.output, m.o), term(e.cacheRead, m.cacheRead),
      term(e.writeShort, m.writeShort), term(e.writeLong, m.writeLong), term(e.writeUnsplit, null),
    ].join("+");
  });
  if (!parts.length) return "CAST(NULL AS DOUBLE)";
  return "(CASE " + parts.join(" ") + " ELSE CAST(NULL AS DOUBLE) END)";
}

function sumKnownCostSQL(expression) {
  return "CASE WHEN COUNT(*) = COUNT(" + expression + ") THEN SUM(" + expression + ") ELSE NULL END";
}

function setHealthFromData(el, data, thresholds) {
  var th = thresholds || {};
  var p95Red = (th.p95Red != null) ? th.p95Red : 5000;
  var p95Yellow = (th.p95Yellow != null) ? th.p95Yellow : 2000;
  var errRed = (th.errRed != null) ? th.errRed : 5;
  var errYellow = (th.errYellow != null) ? th.errYellow : 1;
  var total = Number(data.total) || 0;
  var errors = Number(data.errors) || 0;
  var p95 = Number(data.p95_ms) || 0;
  var errRate = total > 0 ? (errors / total * 100) : 0;

  var level, label;
  if (total === 0) {
    level = 'na'; label = t('health.na');
  } else if (errRate > errRed || p95 > p95Red) {
    level = 'red'; label = t('health.unhealthy');
  } else if (errRate > errYellow || p95 > p95Yellow) {
    level = 'yellow'; label = t('health.degraded');
  } else {
    level = 'green'; label = t('health.healthy');
  }

  var detail = total > 0
    ? ' (err ' + errRate.toFixed(1) + '%, p95 ' + fmtDurMs(p95) + ')'
    : '';
  el.className = 'health-indicator health-' + level;
  el.innerHTML = '<span class="health-dot"></span><span class="health-text">' +
    escapeHTML(label + detail) + '</span>';
}

// ===================================================================
// Cost chart drill-down popup
// ===================================================================

var _costDrilldownEscHandler = null;
var _costDrilldownClickHandler = null;
var _costDrilldownTimeoutId = null;

function closeCostDrilldown() {
  var existing = document.getElementById('cost-drilldown-popup');
  if (existing) existing.remove();
  if (_costDrilldownTimeoutId) {
    clearTimeout(_costDrilldownTimeoutId);
    _costDrilldownTimeoutId = null;
  }
  if (_costDrilldownEscHandler) {
    document.removeEventListener('keydown', _costDrilldownEscHandler);
    _costDrilldownEscHandler = null;
  }
  if (_costDrilldownClickHandler) {
    document.removeEventListener('click', _costDrilldownClickHandler);
    _costDrilldownClickHandler = null;
  }
}

// Show a drill-down popup below anchorEl with top sessions/traces for a time bucket.
// fetchFn(tsStartISO, tsEndISO) → Promise<[{ time, label, model, tokens, cost, onclick }]>
function showCostDrilldown(anchorEl, tsSec, bucketSec, fetchFn) {
  closeCostDrilldown();

  var tsStart = new Date(tsSec * 1000);
  var tsEnd = new Date((tsSec + bucketSec) * 1000);
  var startISO = tsStart.toISOString();
  var endISO = tsEnd.toISOString();

  var timeFmt = function(d) {
    return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
  };
  var timeLabel = timeFmt(tsStart) + ' \u2013 ' + timeFmt(tsEnd);

  var popup = document.createElement('div');
  popup.id = 'cost-drilldown-popup';
  popup.innerHTML = '<div class="drilldown-header">' +
    '<span class="drilldown-time">' + escapeHTML(timeLabel) + '</span>' +
    '<span class="drilldown-title">' + escapeHTML(t('drilldown.top_sessions')) + '</span>' +
    '<button class="drilldown-close" onclick="closeCostDrilldown()">\u00d7</button>' +
    '</div>' +
    '<div class="drilldown-body"><div class="loading" style="padding:12px;text-align:center">' +
    escapeHTML(t('empty.loading')) + '</div></div>';

  // Insert after the chart container's parent (.chart-container)
  var chartContainer = anchorEl.closest('.chart-container') || anchorEl;
  chartContainer.style.position = 'relative';
  chartContainer.appendChild(popup);

  // Close on Esc
  _costDrilldownEscHandler = function(e) { if (e.key === 'Escape') closeCostDrilldown(); };
  document.addEventListener('keydown', _costDrilldownEscHandler);

  // Close on click outside (delayed to avoid catching the triggering click)
  _costDrilldownTimeoutId = setTimeout(function() {
    _costDrilldownTimeoutId = null;
    _costDrilldownClickHandler = function(e) {
      var p = document.getElementById('cost-drilldown-popup');
      if (p && !p.contains(e.target)) closeCostDrilldown();
    };
    document.addEventListener('click', _costDrilldownClickHandler);
  }, 100);

  // Fetch data
  fetchFn(startISO, endISO).then(function(items) {
    var body = popup.querySelector('.drilldown-body');
    if (!body) return;
    if (!items || !items.length) {
      body.innerHTML = '<div style="padding:12px;color:var(--text-dim);text-align:center">' +
        escapeHTML(t('empty.no_data')) + '</div>';
      return;
    }
    var html = '<table class="data-table drilldown-table"><thead><tr>' +
      '<th>Time</th><th>Session</th><th>Model</th><th>Tokens</th><th>Cost</th>' +
      '</tr></thead><tbody>';
    items.forEach(function(item) {
      html += '<tr class="clickable"' +
        (item.onclick ? ' onclick="' + item.onclick + '"' : '') + '>' +
        '<td>' + escapeHTML(item.time || '') + '</td>' +
        '<td>' + escapeHTML(item.label || '') + '</td>' +
        '<td>' + escapeHTML(item.model || '') + '</td>' +
        '<td>' + fmtNum(item.tokens) + '</td>' +
        '<td>' + fmtCost(item.cost) + '</td>' +
        '</tr>';
    });
    html += '</tbody></table>';
    body.innerHTML = html;
  }).catch(function() {
    var body = popup.querySelector('.drilldown-body');
    if (body) body.innerHTML = '<div style="padding:12px;color:var(--text-dim)">' +
      escapeHTML(t('empty.no_data')) + '</div>';
  });
}
