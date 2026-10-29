// Comparison uses reported usage only; transcript length is not a token count.
var comparisonSelection = new Map();
var comparisonVersion = 0;

function sessionUsage(rows, source) {
  var result = { input: 0, output: 0, cache: 0, reasoning: 0, known: false };
  rows.forEach(function(row) {
    if (row.input_tokens == null && row.output_tokens == null) return;
    result.known = true;
    var input = Number(row.input_tokens) || 0;
    var cache = Number(row.cache_read_tokens) || 0;
    var writes = Number(row.cache_creation_tokens) || 0;
    result.input += input + (source === 'codex' ? 0 : cache + writes);
    result.output += Number(row.output_tokens) || 0;
    result.cache += cache;
    result.reasoning += Number(row.reasoning_tokens) || 0;
  });
  result.cacheRatio = result.input > 0 ? result.cache / result.input : null;
  return result;
}

function comparisonToggle(id, source, checked) {
  if (checked && comparisonSelection.size < 2) comparisonSelection.set(id, source);
  else comparisonSelection.delete(id);
  comparisonVersion++;
  comparisonSync();
}

function comparisonSync() {
  document.querySelectorAll('.compare-select').forEach(function(input) {
    input.checked = comparisonSelection.has(input.dataset.session);
    input.disabled = comparisonSelection.size === 2 && !input.checked;
  });
  var button = document.getElementById('compare-button');
  button.disabled = comparisonSelection.size !== 2;
  button.textContent = t('compare.action') + ' (' + comparisonSelection.size + '/2)';
}

async function comparisonOpen() {
  if (comparisonSelection.size !== 2) return;
  var version = ++comparisonVersion;
  var ids = Array.from(comparisonSelection.keys());
  var sources = Array.from(comparisonSelection.values());
  var list = ids.map(function(id) { return "'" + escapeSQLString(id) + "'"; }).join(',');
  var dialog = document.getElementById('comparison-dialog');
  var body = document.getElementById('comparison-content');
  body.textContent = t('empty.loading');
  dialog.showModal();
  try {
    var data = await Promise.all([
      query("SELECT session_id, MIN(ts) AS start_ts, MAX(ts) AS end_ts, MAX(cwd) AS cwd, " +
        "SUM(CASE WHEN event_type = 'PreToolUse' THEN 1 ELSE 0 END) AS tools, " +
        "SUM(CASE WHEN event_type = 'PostToolUseFailure' OR tool_success = false THEN 1 ELSE 0 END) AS failures, " +
        "SUM(CASE WHEN event_type = 'ContextCompaction' OR event_type = 'PreCompact' THEN 1 ELSE 0 END) AS compactions " +
        "FROM tma1_hook_events WHERE session_id IN (" + list + ") GROUP BY session_id"),
      query("SELECT session_id, SUM(input_tokens) AS input_tokens, SUM(output_tokens) AS output_tokens, " +
        "SUM(cache_read_tokens) AS cache_read_tokens, SUM(cache_creation_tokens) AS cache_creation_tokens, " +
        "SUM(reasoning_tokens) AS reasoning_tokens FROM tma1_messages WHERE session_id IN (" + list + ") GROUP BY session_id")
    ]);
    if (version !== comparisonVersion || !dialog.open) return;
    var hooks = rowsToObjects(data[0]);
    var usage = rowsToObjects(data[1]);
    var columns = ids.map(function(id, i) {
      var h = hooks.find(function(row) { return row.session_id === id; });
      var u = sessionUsage(usage.filter(function(row) { return row.session_id === id; }), sources[i]);
      return {id:id, source:sources[i], hook:h, usage:u};
    });
    var html = '<table class="comparison-table"><thead><tr><th>' + t('compare.metric') + '</th>';
    columns.forEach(function(c) {
      html += '<th><span class="eyebrow">' + escapeHTML(c.source) + '</span><br><code>' + escapeHTML(c.id.slice(0,8)) + '</code><div class="compare-project">' + escapeHTML(c.hook ? c.hook.cwd : t('empty.no_data')) + '</div></th>';
    });
    html += '</tr></thead><tbody>';
    var metrics = [
      ['compare.elapsed', function(c) { return c.hook ? fmtDurSec((tsToMs(c.hook.end_ts)-tsToMs(c.hook.start_ts))/1000) : '—'; }],
      ['sessions.tool_calls', function(c) { return c.hook ? fmtNum(Number(c.hook.tools)) : '—'; }],
      ['compare.failures', function(c) { return c.hook ? fmtNum(Number(c.hook.failures)) : '—'; }],
      ['compare.input', function(c) { return c.usage.known ? fmtTokens(c.usage.input) : '—'; }],
      ['compare.output', function(c) { return c.usage.known ? fmtTokens(c.usage.output) : '—'; }],
      ['compare.cache', function(c) { return c.usage.cacheRatio != null ? (100*c.usage.cacheRatio).toFixed(1)+'%' : '—'; }],
      ['compare.compactions', function(c) { return c.hook ? fmtNum(Number(c.hook.compactions)) : '—'; }]
    ];
    metrics.forEach(function(metric) {
      html += '<tr><th>' + t(metric[0]) + '</th>';
      columns.forEach(function(c) { html += '<td>' + metric[1](c) + '</td>'; });
      html += '</tr>';
    });
    body.innerHTML = html + '</tbody></table><p class="comparison-note">' + t('compare.note') + '</p>';
  } catch (error) {
    if (version === comparisonVersion) body.textContent = t('compare.error') + ': ' + error.message;
  }
}
