// copilot-cli.js — Copilot CLI view: gcp_* functions
// Data comes from tma1_hook_events (agent_source='copilot_cli') and tma1_messages (session_id LIKE 'cp:%')
// Depends on: core.js, chart.js

function gcp_hookWhere() {
  return "FROM tma1_hook_events " +
    "WHERE ts > NOW() - INTERVAL '" + intervalSQL() + "' " +
    "AND agent_source = 'copilot_cli'";
}

function gcp_msgWhere() {
  return "FROM tma1_messages " +
    "WHERE ts > NOW() - INTERVAL '" + intervalSQL() + "' " +
    "AND session_id LIKE 'cp:%'";
}

async function gcp_loadCards() {
  await loadPricing();
  try {
    var results = await Promise.all([
      // Output tokens
      query("SELECT SUM(COALESCE(output_tokens, 0)) AS v " + gcp_msgWhere()),
      // Tool calls
      query("SELECT COUNT(*) AS v " + gcp_hookWhere() + " AND event_type = 'PreToolUse'"),
      // Sessions
      query("SELECT COUNT(DISTINCT session_id) AS v " + gcp_hookWhere()),
      // Cost estimate from messages
      query(
        "SELECT model, " +
        "SUM(COALESCE(output_tokens, 0)) AS out_tok, " +
        "SUM(CASE WHEN message_type IN ('user','tool_result','tool_use') THEN LENGTH(COALESCE(content,''))/4 ELSE 0 END) AS est_in_tok " +
        gcp_msgWhere() + " AND model != '' GROUP BY model"
      ),
      // Avg duration
      query(
        "SELECT AVG(dur_sec) AS v FROM (" +
        "SELECT (MAX(ts) - MIN(ts)) / 1000 AS dur_sec " +
        gcp_hookWhere() + " GROUP BY session_id HAVING COUNT(*) > 1) sub"
      ),
    ]);

    var outTok = Number(rows(results[0])?.[0]?.[0]) || 0;
    var toolCalls = Number(rows(results[1])?.[0]?.[0]) || 0;
    var sessions = Number(rows(results[2])?.[0]?.[0]) || 0;
    var avgDur = Number(rows(results[4])?.[0]?.[0]) || 0;

    // Calculate cost from model pricing
    var totalCost = 0;
    var costRows = rowsToObjects(results[3]);
    for (var i = 0; i < costRows.length; i++) {
      var cr = costRows[i];
      var price = sess_lookupPrice(cr.model);
      var estIn = Number(cr.est_in_tok) || 0;
      var out = Number(cr.out_tok) || 0;
      totalCost += estIn * price.input / 1000000 + out * price.output / 1000000;
    }

    document.getElementById('gcp-val-cost').textContent = fmtCost(totalCost);
    document.getElementById('gcp-val-tokens').textContent = fmtNum(outTok);
    document.getElementById('gcp-val-tools').textContent = fmtNum(toolCalls);
    document.getElementById('gcp-val-sessions').textContent = fmtNum(sessions);
    document.getElementById('gcp-val-duration').textContent = fmtDurSec(avgDur);

    return sessions > 0;
  } catch (err) {
    var banner = document.getElementById('error-banner');
    if (banner) { banner.style.display = 'block'; banner.textContent = 'Copilot CLI: ' + err.message; }
    return false;
  }
}

async function gcp_loadOverview() {
  await Promise.all([
    gcp_loadTokenChart(),
    gcp_loadCostChart(),
    gcp_loadToolDist(),
    gcp_loadActivityHeatmap(),
  ]);
}

async function gcp_loadTokenChart() {
  var el = document.getElementById('gcp-chart-tokens');
  if (!el) return;
  try {
    var res = await query(
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, ts) AS t, " +
      "SUM(COALESCE(output_tokens, 0)) AS output_tok, " +
      "SUM(CASE WHEN message_type IN ('user','tool_result','tool_use') THEN LENGTH(COALESCE(content,''))/4 ELSE 0 END) AS est_input_tok " +
      gcp_msgWhere() + " GROUP BY t ORDER BY t"
    );
    var data = rowsToObjects(res);
    if (!data.length) { el.innerHTML = '<div class="chart-empty">No token data yet</div>'; return; }
    renderChart('gcp-chart-tokens', data, [
      { label: 'Est. Input Tokens', key: 'est_input_tok', color: '#79c0ff' },
      { label: 'Output Tokens', key: 'output_tok', color: '#f0883e' },
    ], function(v) { return fmtNum(v); });
  } catch { el.innerHTML = '<div class="chart-empty">Failed to load token data</div>'; }
}

async function gcp_loadCostChart() {
  var el = document.getElementById('gcp-chart-cost');
  if (!el) return;
  await loadPricing();
  try {
    var res = await query(
      "SELECT date_bin('" + chartBucket() + "'::INTERVAL, ts) AS t, " +
      "model, " +
      "SUM(COALESCE(output_tokens, 0)) AS out_tok, " +
      "SUM(CASE WHEN message_type IN ('user','tool_result','tool_use') THEN LENGTH(COALESCE(content,''))/4 ELSE 0 END) AS est_in_tok " +
      gcp_msgWhere() + " AND model != '' GROUP BY t, model ORDER BY t"
    );
    var rawData = rowsToObjects(res);
    if (!rawData.length) { el.innerHTML = '<div class="chart-empty">No cost data yet</div>'; return; }

    // Aggregate cost per time bucket
    var buckets = {};
    for (var i = 0; i < rawData.length; i++) {
      var d = rawData[i];
      var key = String(d.t);
      if (!buckets[key]) buckets[key] = { t: d.t, cost: 0 };
      var price = sess_lookupPrice(d.model);
      buckets[key].cost += (Number(d.est_in_tok) || 0) * price.input / 1000000 +
                           (Number(d.out_tok) || 0) * price.output / 1000000;
    }
    var data = Object.values(buckets).sort(function(a, b) { return String(a.t) < String(b.t) ? -1 : 1; });
    renderChart('gcp-chart-cost', data, [
      { label: 'Cost (USD)', key: 'cost', color: '#f0883e' },
    ], function(v) { return '$' + Number(v).toFixed(4); });
  } catch { el.innerHTML = '<div class="chart-empty">Failed to load cost data</div>'; }
}

async function gcp_loadToolDist() {
  var el = document.getElementById('gcp-tool-dist');
  if (!el) return;
  try {
    var tc = getThemeColors();
    var res = await query(
      "SELECT tool_name, COUNT(*) AS cnt " +
      gcp_hookWhere() + " AND event_type = 'PreToolUse' AND tool_name != '' " +
      "GROUP BY tool_name ORDER BY cnt DESC LIMIT 15"
    );
    var data = rowsToObjects(res);
    if (!data.length) { el.innerHTML = '<div class="chart-empty">No tool data yet</div>'; return; }
    var maxCnt = Math.max.apply(null, data.map(function(d) { return Number(d.cnt) || 0; }));
    el.innerHTML = data.map(function(d) {
      var cnt = Number(d.cnt) || 0;
      var pct = maxCnt > 0 ? (cnt / maxCnt * 100) : 0;
      return '<div class="bar-row">' +
        '<div class="bar-label" title="' + escapeHTML(d.tool_name) + '">' + escapeHTML(d.tool_name) + '</div>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + pct + '%;background:' + tc.blue + '"></div></div>' +
        '<div class="bar-value">' + fmtNum(cnt) + '</div></div>';
    }).join('');
  } catch { el.innerHTML = '<div class="chart-empty">Failed to load tool data</div>'; }
}

async function gcp_loadActivityHeatmap() {
  var el = document.getElementById('gcp-activity-heatmap');
  if (!el) return;
  var cfg = heatmapConfig();
  try {
    var res = await query(
      "SELECT date_bin('" + cfg.bucket + "'::INTERVAL, ts) AS t, COUNT(*) AS cnt " +
      "FROM tma1_hook_events WHERE agent_source = 'copilot_cli' " +
      "AND ts > NOW() - INTERVAL '" + cfg.interval + "' " +
      "GROUP BY t ORDER BY t"
    );
    renderHeatmap('gcp-activity-heatmap', rowsToObjects(res));
  } catch { el.innerHTML = '<div class="chart-empty">Failed to load activity data</div>'; }
}

async function gcp_loadTools() {
  var tbody = document.getElementById('gcp-tools-body');
  if (!tbody) return;
  try {
    var res = await query(
      "SELECT e.tool_name, " +
      "SUM(CASE WHEN e.event_type = 'PreToolUse' THEN 1 ELSE 0 END) AS calls, " +
      "SUM(CASE WHEN e.event_type = 'PostToolUseFailure' THEN 1 ELSE 0 END) AS failures " +
      gcp_hookWhere() + " AND e.event_type IN ('PreToolUse','PostToolUse','PostToolUseFailure') " +
      "AND e.tool_name != '' GROUP BY e.tool_name ORDER BY calls DESC"
    );
    var data = rowsToObjects(res);
    if (!data.length) { tbody.innerHTML = '<tr><td colspan="4" class="loading">No tool data</td></tr>'; return; }
    tbody.innerHTML = data.map(function(d) {
      var calls = Number(d.calls) || 0;
      var failures = Number(d.failures) || 0;
      var rate = calls > 0 ? ((failures / calls) * 100).toFixed(1) + '%' : '0%';
      var rateClass = failures > 0 ? 'style="color:var(--red)"' : '';
      return '<tr><td>' + escapeHTML(d.tool_name) + '</td>' +
        '<td>' + fmtNum(calls) + '</td>' +
        '<td ' + rateClass + '>' + fmtNum(failures) + '</td>' +
        '<td ' + rateClass + '>' + rate + '</td></tr>';
    }).join('');
  } catch { tbody.innerHTML = '<tr><td colspan="4" class="loading">Failed to load</td></tr>'; }
}

async function gcp_loadCostByModel() {
  var tbody = document.getElementById('gcp-cost-body');
  if (!tbody) return;
  await loadPricing();
  try {
    var res = await query(
      "SELECT model, " +
      "SUM(COALESCE(output_tokens, 0)) AS out_tok, " +
      "COUNT(*) AS msgs, " +
      "SUM(CASE WHEN message_type IN ('user','tool_result','tool_use') THEN LENGTH(COALESCE(content,''))/4 ELSE 0 END) AS est_in_tok " +
      gcp_msgWhere() + " AND model != '' GROUP BY model ORDER BY out_tok DESC"
    );
    var data = rowsToObjects(res);
    if (!data.length) { tbody.innerHTML = '<tr><td colspan="4" class="loading">No cost data</td></tr>'; return; }
    tbody.innerHTML = data.map(function(d) {
      var price = sess_lookupPrice(d.model);
      var estIn = Number(d.est_in_tok) || 0;
      var out = Number(d.out_tok) || 0;
      var cost = estIn * price.input / 1000000 + out * price.output / 1000000;
      return '<tr><td>' + escapeHTML(d.model) + '</td>' +
        '<td>' + fmtNum(out) + '</td>' +
        '<td>' + fmtNum(Number(d.msgs) || 0) + '</td>' +
        '<td class="cost">' + fmtCost(cost) + '</td></tr>';
    }).join('');
  } catch { tbody.innerHTML = '<tr><td colspan="4" class="loading">Failed to load</td></tr>'; }
}

// Tab navigation
(function() {
  var tabsEl = document.getElementById('gcp-tabs');
  if (!tabsEl) return;
  tabsEl.addEventListener('click', function(e) {
    var tab = e.target.closest('.tab[data-gcptab]');
    if (!tab) return;
    tabsEl.querySelectorAll('.tab').forEach(function(t) { t.classList.remove('active'); });
    tab.classList.add('active');
    var tabName = tab.dataset.gcptab;
    document.querySelectorAll('#view-copilot-cli .tab-content').forEach(function(tc) {
      tc.classList.toggle('active', tc.id === 'tab-' + tabName);
    });
    if (tabName === 'gcp-tools') gcp_loadTools();
    if (tabName === 'gcp-cost') gcp_loadCostByModel();
    updateHash();
  });
})();
