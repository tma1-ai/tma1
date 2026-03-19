// core.js — global constants + utility functions
// Depends on: i18n.js (t())

var API = '/api/query';
var currentTimeRange = '24h';

function intervalSQL() {
  var m = { '1h': '1 hour', '6h': '6 hours', '24h': '1 day', '7d': '7 days', '30d': '30 days' };
  return m[currentTimeRange] || '1 day';
}

function chartBucket() {
  var m = { '1h': '5 minutes', '6h': '5 minutes', '24h': '5 minutes', '7d': '15 minutes', '30d': '1 hour' };
  return m[currentTimeRange] || '5 minutes';
}

// Session query row limit scaled by time range.
// Typical agent usage: ~1k events/day. These limits should cover all but
// the most extreme workloads, while keeping browser performance reasonable.
function sessionQueryLimit() {
  var m = { '1h': 5000, '6h': 10000, '24h': 20000, '7d': 50000, '30d': 100000 };
  return m[currentTimeRange] || 20000;
}

async function query(sql) {
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
  return r.json();
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
  return (ms / 1000).toFixed(1) + 's';
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
var defaultPrice = { i: 3, o: 15 };
var pricingLoaded = false;

async function loadPricing() {
  if (pricingLoaded) return;
  try {
    var res = await query(
      "SELECT model_pattern, input_price, output_price " +
      "FROM tma1_model_pricing ORDER BY priority"
    );
    var data = rowsToObjects(res);
    if (data.length) {
      modelPricing = data.map(function(d) {
        return { p: d.model_pattern, i: Number(d.input_price), o: Number(d.output_price) };
      });
      pricingLoaded = true;
    }
  } catch (e) {
    // Table may not exist yet; costCaseSQL will fall back to default pricing.
  }
}

function costCaseSQL(modelExpr, inputExpr, outputExpr) {
  var parts = modelPricing.map(function(m) {
    return "WHEN " + modelExpr + " LIKE '%" + m.p.replace(/'/g, "''") + "%' THEN " +
      "CAST(" + inputExpr + " AS DOUBLE)*" + m.i + "/1000000.0+" +
      "CAST(" + outputExpr + " AS DOUBLE)*" + m.o + "/1000000.0";
  });
  if (!parts.length) {
    return "(" +
      "CAST(" + inputExpr + " AS DOUBLE)*" + defaultPrice.i + "/1000000.0+" +
      "CAST(" + outputExpr + " AS DOUBLE)*" + defaultPrice.o + "/1000000.0" +
      ")";
  }
  return "(CASE " + parts.join(" ") +
    " ELSE CAST(" + inputExpr + " AS DOUBLE)*" + defaultPrice.i + "/1000000.0+" +
    "CAST(" + outputExpr + " AS DOUBLE)*" + defaultPrice.o + "/1000000.0 END)";
}

function setHealthFromData(el, data) {
  var total = Number(data.total) || 0;
  var errors = Number(data.errors) || 0;
  var p95 = Number(data.p95_ms) || 0;
  var errRate = total > 0 ? (errors / total * 100) : 0;

  var level, label;
  if (total === 0) {
    level = 'na'; label = 'N/A';
  } else if (errRate > 5 || p95 > 5000) {
    level = 'red'; label = 'Unhealthy';
  } else if (errRate > 1 || p95 > 2000) {
    level = 'yellow'; label = 'Degraded';
  } else {
    level = 'green'; label = 'Healthy';
  }

  var detail = total > 0
    ? ' (err ' + errRate.toFixed(1) + '%, p95 ' + fmtDurMs(p95) + ')'
    : '';
  el.className = 'health-indicator health-' + level;
  el.innerHTML = '<span class="health-dot"></span><span class="health-text">' +
    escapeHTML(label + detail) + '</span>';
}
