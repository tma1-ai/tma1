// core.js — global constants + utility functions
// Depends on: i18n.js (t())

var API = '/api/query';
var currentTimeRange = '24h';

function intervalSQL() {
  var m = { '1h': '1 hour', '6h': '6 hours', '24h': '1 day', '7d': '7 days' };
  return m[currentTimeRange] || '1 day';
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
    return "WHEN " + modelExpr + " LIKE '%" + m.p + "%' THEN " +
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
