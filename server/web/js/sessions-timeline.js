/* Sessions — timeline item rendering, filtering, and search helpers. */
/* globals: escapeHTML, escapeJSString, t, tsToMs, sessTimelineData, fmtDurMs,
            sess_toggleDurPopover */

// ── Timeline filter ───────────────────────────────────────────────────

var sessActiveToolFilter = '';

// Windowed (lazy) rendering: only the tail SESS_RENDER_WINDOW items of the
// filtered timeline are put in the DOM; a "load earlier" header exposes
// older chunks on demand. Large sessions (20k+ items) previously rendered
// everything in one innerHTML assignment, blocking the main thread.
var SESS_RENDER_WINDOW = 500;   // chunk size
var sessFilteredData = [];      // current filtered array
var sessRenderStart = 0;        // index of first rendered item within sessFilteredData

function sess_filterByTool(btn, toolName) {
  sessActiveToolFilter = toolName;
  document.querySelectorAll('.sess-chip').forEach(function(c) { c.classList.remove('active'); });
  btn.classList.add('active');
  sess_applyFilters();
}

// Debounced keyword filter (mirrors sess_debouncedFilter in sessions.js) —
// avoids re-filtering + rebuilding the full timeline DOM on every keystroke.
var sessDetailFilterTimer = null;
function sess_filterTimeline() {
  if (sessDetailFilterTimer) clearTimeout(sessDetailFilterTimer);
  sessDetailFilterTimer = setTimeout(sess_applyFilters, 180);
}

function sess_applyFilters() {
  sess_renderTimelineWindow(true);
}

// Single render path for both the initial detail render and filter changes.
// Recomputes sessFilteredData and renders only the tail window into
// #sess-timeline-items. reset=true re-anchors the window to the tail
// (new detail open or filter change).
function sess_renderTimelineWindow(reset) {
  var filterEl = document.getElementById('sess-detail-filter');
  var keyword = ((filterEl && filterEl.value) || '').toLowerCase().trim();
  sessFilteredData = sessTimelineData.filter(function(item) {
    if (sessActiveToolFilter) {
      var tn = null;
      if (item.source === 'tool_pair') tn = item.data.tool_name;
      else if (item.source === 'hook' && item.data.tool_name) tn = item.data.tool_name;
      else if (item.source === 'message' && item.data.tool_name) tn = item.data.tool_name;
      if (tn !== sessActiveToolFilter) return false;
    }
    if (keyword) {
      var text = '';
      if (item.source === 'tool_pair') text = (item.data.tool_name || '') + ' ' + (item.data.tool_input || '') + ' ' + (item.data.tool_result || '');
      else if (item.source === 'hook') text = (item.data.tool_name || '') + ' ' + (item.data.tool_input || '') + ' ' + (item.data.tool_result || '') + ' ' + (item.data.message || '');
      else text = (item.data.content || '') + ' ' + (item.data.tool_name || '');
      if (text.toLowerCase().indexOf(keyword) === -1) return false;
    }
    return true;
  });
  var container = document.getElementById('sess-timeline-items');
  if (!container) return;
  if (reset) sessRenderStart = Math.max(0, sessFilteredData.length - SESS_RENDER_WINDOW);
  container.innerHTML = sessFilteredData.length
    ? sess_windowHTML()
    : '<div class="loading">' + t('empty.no_data') + '</div>';
}

// HTML for the current window: "load earlier" header (when items are
// hidden above) + the rendered items from sessRenderStart to the end.
function sess_windowHTML() {
  var html = sess_loadEarlierHTML();
  for (var i = sessRenderStart; i < sessFilteredData.length; i++) html += renderTimelineItem(sessFilteredData[i]);
  return html;
}

function sess_loadEarlierHTML() {
  if (sessRenderStart <= 0) return '';
  return '<div class="tl-load-earlier" id="sess-load-earlier" onclick="sess_loadEarlier()">\u2B06 ' +
    t('sessions.load_earlier').replace('{n}', String(sessRenderStart)) + '</div>';
}

// "Load earlier" click: expose the previous SESS_RENDER_WINDOW items by
// prepending them, preserving the scroll position (content above the
// viewport grows, so scrollTop is shifted by the height delta).
function sess_loadEarlier() {
  var container = document.getElementById('sess-timeline-items');
  if (!container || sessRenderStart <= 0) return;
  var newStart = Math.max(0, sessRenderStart - SESS_RENDER_WINDOW);
  var html = '';
  for (var i = newStart; i < sessRenderStart; i++) html += renderTimelineItem(sessFilteredData[i]);
  sessRenderStart = newStart;
  var header = document.getElementById('sess-load-earlier');
  if (header) header.parentNode.removeChild(header);
  var scrollEl = document.getElementById('sess-timeline-scroll');
  var oldHeight = scrollEl ? scrollEl.scrollHeight : 0;
  container.insertAdjacentHTML('afterbegin', sess_loadEarlierHTML() + html);
  if (scrollEl) scrollEl.scrollTop += (scrollEl.scrollHeight - oldHeight);
}

// Expand the rendered window so the item at idx (within sessFilteredData)
// is in the DOM — used by scroll-to navigation targeting a non-rendered
// region. Re-renders the window with some context above the target.
function sess_expandWindowTo(idx) {
  if (idx >= sessRenderStart) return;
  var container = document.getElementById('sess-timeline-items');
  if (!container) return;
  sessRenderStart = Math.max(0, idx - 50);
  container.innerHTML = sess_windowHTML();
}

// ── Timeline item rendering ───────────────────────────────────────────

function renderTimelineItem(item) {
  var extraAttrs = '';
  if (item.source === 'tool_pair' && item.data.tool_use_id) {
    extraAttrs = ' data-tool-use-id="' + escapeHTML(item.data.tool_use_id) + '"';
  }
  var wrapper = '<div class="tl-item-wrap" data-ts="' + (item.ts || 0) + '"' + extraAttrs + '>';
  var inner = '';
  if (item.source === 'tool_pair') inner = renderToolPair(item.data, item.ts);
  else if (item.source === 'hook') inner = renderHookEvent(item.data, item.ts);
  else inner = renderMessage(item.data, item.ts);
  return wrapper + inner + '</div>';
}

function renderToolPair(tc, ts) {
  var time = ts ? new Date(ts).toLocaleTimeString() : '';
  var durMs = (tc.end_ts && tc.start_ts) ? tc.end_ts - tc.start_ts : 0;
  var durLabel = durMs < 1000 ? durMs + 'ms' : (durMs / 1000).toFixed(1) + 's';
  var durTitle = t('sessions.tool_dur_hook_tooltip');
  var statusClass = tc.failed ? 'tl-tool-card-err' : 'tl-tool-card-ok';
  var statusIcon = tc.failed ? '\u2717' : '\u2713';
  var result = tc.tool_result || '';
  var argsSummary = summarizeToolArgs(tc.tool_name, tc.tool_input, sess_parseJSONField(tc, 'tool_input'));

  var html = '<div class="tl-tool-card ' + statusClass + '">';
  html += '<div class="tl-tool-card-header">';
  html += '<span class="tl-time">' + time + '</span>';
  html += '<span class="tl-tool-name">' + escapeHTML(tc.tool_name || 'unknown') + '</span>';
  html += '<span class="tl-tool-dur">' + durLabel + '<span class="tl-tool-dur-info" role="button" tabindex="0" aria-label="' + escapeHTML(durTitle) + '" onclick="sess_toggleDurPopover(event, this)">\u24D8<span class="tl-tool-dur-popover" onclick="event.stopPropagation()">' + escapeHTML(durTitle) + '</span></span></span>';
  html += '<span class="tl-tool-status">' + statusIcon + '</span>';
  html += '</div>';
  if (argsSummary) html += '<div class="tl-tool-card-args">' + escapeHTML(argsSummary) + '</div>';
  if (result) {
    html += '<details class="tl-tool-card-result"><summary>' + t('sessions.result') + '</summary>';
    html += formatToolResult(tc.tool_name, result, sess_parseJSONField(tc, 'tool_result'));
    html += '</details>';
  }
  html += '</div>';
  return html;
}

// Memoized JSON.parse for a string field on a timeline data object. The
// parsed value is cached on the object itself so repeated render passes
// (every filter change re-renders each item) skip re-parsing. Returns null
// when the field is empty or not valid JSON.
function sess_parseJSONField(data, field) {
  var key = '_parsed_' + field;
  if (!(key in data)) {
    var parsed = null;
    try { parsed = JSON.parse(data[field]); } catch (e) { parsed = null; }
    data[key] = parsed;
  }
  return data[key];
}

function formatToolResult(toolName, result, parsedResult) {
  if (!result) return '';
  var obj = parsedResult;
  if (typeof obj === 'object' && obj !== null) {
    return '<div class="tl-result-structured">' + formatResultObj(toolName, obj) + '</div>';
  }
  var text = result.length > 2000 ? result.slice(0, 2000) + '\u2026' : result;
  return '<pre>' + escapeHTML(text) + '</pre>';
}

function formatResultObj(toolName, obj) {
  var html = '';
  if (obj.stdout != null || obj.stderr != null) {
    if (obj.stdout) html += '<div class="tl-result-field"><span class="tl-result-key">stdout</span><pre>' + escapeHTML(truncResultText(obj.stdout)) + '</pre></div>';
    if (obj.stderr) html += '<div class="tl-result-field"><span class="tl-result-key">stderr</span><pre class="tl-result-err">' + escapeHTML(truncResultText(obj.stderr)) + '</pre></div>';
    if (!html) html = '<div class="tl-result-field"><span class="tl-result-key">stdout</span><pre>' + t('sessions.no_data_result') + '</pre></div>';
    return html;
  }
  if (obj.file && obj.file.content != null) {
    html += '<div class="tl-result-field"><span class="tl-result-key">content</span><pre>' + escapeHTML(truncResultText(obj.file.content)) + '</pre></div>';
    return html;
  }
  if (obj.filePath) {
    html += '<div class="tl-result-field"><span class="tl-result-key">file</span> ' + escapeHTML(obj.filePath) + '</div>';
    if (obj.newString) html += '<div class="tl-result-field"><span class="tl-result-key">new</span><pre>' + escapeHTML(truncResultText(obj.newString)) + '</pre></div>';
    return html;
  }
  if (obj.output != null) {
    html += '<div class="tl-result-field"><span class="tl-result-key">output</span><pre>' + escapeHTML(truncResultText(typeof obj.output === 'string' ? obj.output : JSON.stringify(obj.output))) + '</pre></div>';
    return html;
  }
  var pretty = JSON.stringify(obj, null, 2);
  return '<pre>' + escapeHTML(truncResultText(pretty)) + '</pre>';
}

function truncResultText(s) {
  return s.length > 2000 ? s.slice(0, 2000) + '\u2026' : s;
}

function summarizeToolArgs(toolName, argsStr, parsedArgs) {
  if (!argsStr) return '';
  try {
    var obj = parsedArgs;
    if (toolName === 'Read' || toolName === 'Write') return obj.file_path || obj.path || argsStr;
    if (toolName === 'Edit') return obj.file_path || obj.path || argsStr;
    if (toolName === 'Bash') return obj.command || argsStr;
    if (toolName === 'Glob') return obj.pattern || argsStr;
    if (toolName === 'Grep') return (obj.pattern || '') + (obj.path ? ' in ' + obj.path : '');
    if (toolName === 'Agent' || toolName === 'Task') return obj.description || obj.prompt || argsStr;
    if (toolName === 'WebSearch') return obj.query || argsStr;
    if (toolName === 'WebFetch') return obj.url || argsStr;
  } catch (e) { /* not JSON */ }
  if (argsStr.length > 120) return argsStr.slice(0, 120) + '\u2026';
  return argsStr;
}

function hookMeta(ev) {
  if (!ev.metadata) return {};
  if (ev._parsedMeta === undefined) {
    var meta;
    try { meta = typeof ev.metadata === 'string' ? JSON.parse(ev.metadata) : ev.metadata; } catch (e) { meta = {}; }
    ev._parsedMeta = meta;
  }
  return ev._parsedMeta;
}

function renderHookEvent(ev, ts) {
  var type = ev.event_type;
  var time = ts ? new Date(ts).toLocaleTimeString() : '';
  var meta = hookMeta(ev);

  // Session lifecycle.
  if (type === 'SessionStart') {
    var src = meta.source || '';
    var model = meta.model || '';
    var detail = [src, model].filter(Boolean).join(' \u00B7 ');
    return '<div class="tl-item tl-lifecycle"><span class="tl-time">' + time + '</span> <span class="tl-badge tl-badge-start">\u25B6 ' + t('sessions.ev_start') + '</span>' + (detail ? ' <span style="color:var(--text-dim);font-size:11px">' + escapeHTML(detail) + '</span>' : '') + '</div>';
  }
  if (type === 'SessionEnd' || type === 'Stop') {
    var reason = meta.reason || '';
    return '<div class="tl-item tl-lifecycle"><span class="tl-time">' + time + '</span> <span class="tl-badge tl-badge-end">\u25A0 ' + t('sessions.ev_end') + '</span>' + (reason ? ' <span style="color:var(--text-dim);font-size:11px">' + escapeHTML(reason) + '</span>' : '') + '</div>';
  }

  // Tool calls.
  if (type === 'PreToolUse') {
    var args = summarizeToolArgs(ev.tool_name, ev.tool_input, sess_parseJSONField(ev, 'tool_input'));
    return '<div class="tl-tool-card tl-tool-card-pending"><div class="tl-tool-card-header"><span class="tl-time">' + time + '</span><span class="tl-tool-name">' + escapeHTML(ev.tool_name || 'unknown') + '</span><span class="tl-tool-dur">\u2026</span></div>' + (args ? '<div class="tl-tool-card-args">' + escapeHTML(args) + '</div>' : '') + '</div>';
  }

  // Subagents.
  if (type === 'SubagentStart') return '<div class="tl-item tl-subagent"><span class="tl-time">' + time + '</span> <span class="tl-badge tl-badge-sub">\u25B6</span> ' + t('sessions.ev_subagent_start') + ' <strong>' + escapeHTML(ev.agent_type || '') + '</strong></div>';
  if (type === 'SubagentStop') return '<div class="tl-item tl-subagent"><span class="tl-time">' + time + '</span> <span class="tl-badge tl-badge-sub">\u25A0</span> ' + t('sessions.ev_subagent_stop') + '</div>';

  // Notifications.
  if (type === 'Notification') return '<div class="tl-item tl-notification"><span class="tl-time">' + time + '</span> <span class="tl-badge tl-badge-warn">\u26A0</span> ' + escapeHTML(ev.message || ev.notification_type || t('sessions.ev_notification')) + '</div>';

  // Context compaction.
  if (type === 'PreCompact') return '<div class="tl-compact-divider">\u2702 ' + t('sessions.ev_compacting') + '</div>';
  if (type === 'PostCompact') return '<div class="tl-compact-divider">\u2702 ' + t('sessions.ev_compacted') + '</div>';

  // User prompt (from hook).
  if (type === 'UserPromptSubmit') {
    var prompt = meta.prompt || ev.message || '';
    if (prompt.length > 200) prompt = prompt.slice(0, 200) + '\u2026';
    return '<div class="tl-item tl-msg-user"><span class="tl-time">' + time + '</span> <span class="tl-role tl-role-user">' + t('sessions.role_user') + '</span> <div class="tl-content">' + escapeHTML(prompt) + '</div></div>';
  }

  // Permissions.
  if (type === 'PermissionRequest') return '<div class="tl-item"><span class="tl-time">' + time + '</span> <span class="tl-badge" style="background:rgba(240,136,62,0.15);color:var(--orange)">\uD83D\uDD12 ' + t('sessions.ev_perm') + '</span> ' + escapeHTML(ev.tool_name || meta.tool_name || '') + '</div>';
  if (type === 'PermissionDenied') return '<div class="tl-item"><span class="tl-time">' + time + '</span> <span class="tl-badge badge-error">\u2717 ' + t('sessions.ev_denied') + '</span> ' + escapeHTML(ev.tool_name || meta.tool_name || '') + '</div>';

  // File changes.
  if (type === 'FileChanged') {
    var fp = meta.file_path || '';
    var fev = meta.event || 'change';
    var ficon = fev === 'add' ? '+' : fev === 'unlink' ? '\u2212' : '~';
    var fcolor = fev === 'add' ? 'var(--green)' : fev === 'unlink' ? 'var(--red)' : 'var(--blue)';
    return '<div class="tl-item" style="font-size:11px;padding:2px 12px"><span class="tl-time">' + time + '</span> <span style="color:' + fcolor + ';font-weight:600;margin:0 4px">' + ficon + '</span> ' + escapeHTML(fp) + '</div>';
  }

  // Tasks.
  if (type === 'TaskCreated') return '<div class="tl-item"><span class="tl-time">' + time + '</span> <span class="tl-badge badge-info">\u2610 ' + t('sessions.ev_task') + '</span> ' + escapeHTML(ev.message || '') + '</div>';
  if (type === 'TaskCompleted') return '<div class="tl-item"><span class="tl-time">' + time + '</span> <span class="tl-badge badge-ok">\u2611 ' + t('sessions.ev_done') + '</span> ' + escapeHTML(ev.message || '') + '</div>';

  // CWD change.
  if (type === 'CwdChanged') return '<div class="tl-item" style="font-size:11px;padding:2px 12px"><span class="tl-time">' + time + '</span> <span style="color:var(--text-dim)">cd</span> ' + escapeHTML(meta.new_cwd || ev.cwd || '') + '</div>';

  // Instructions loaded.
  if (type === 'InstructionsLoaded') return '<div class="tl-item" style="font-size:11px;padding:2px 12px"><span class="tl-time">' + time + '</span> <span style="color:var(--text-dim)">\uD83D\uDCCB</span> ' + escapeHTML(meta.file_path || '') + '</div>';

  // Fallback for any unknown event type.
  return '<div class="tl-item"><span class="tl-time">' + time + '</span> <span class="tl-badge" style="background:rgba(255,255,255,0.06);color:var(--text-dim)">' + escapeHTML(type) + '</span></div>';
}

// Full message bodies for the show-more toggle on long user/assistant
// messages. renderMessage truncates bodies over 2000 chars and registers
// the message here; sess_showFullMessage fills the full text back in.
var sessLongMsgs = [];

// Reveal the full body of a truncated message. Uses textContent (not
// innerHTML) so the raw content needs no HTML escaping.
function sess_showFullMessage(link, id) {
  var msg = sessLongMsgs[id];
  if (!msg || !link.parentNode) return;
  link.parentNode.textContent = msg.content || '';
}

// Render a message body, capping very long user/assistant content with a
// show-more toggle so huge messages don't dominate build + paint time.
function sess_msgBody(msg, content) {
  if (content.length <= 2000) return escapeHTML(content);
  if (msg._longId === undefined) { msg._longId = sessLongMsgs.length; sessLongMsgs.push(msg); }
  return escapeHTML(content.slice(0, 2000)) + '\u2026 <a class="tl-show-more" onclick="sess_showFullMessage(this, ' + msg._longId + ')">' + t('ui.expand') + '</a>';
}

function renderMessage(msg, ts) {
  var time = ts ? new Date(ts).toLocaleTimeString() : '';
  var type = msg.message_type;
  var content = msg.content || '';
  if (type === 'user') return '<div class="tl-item tl-msg-user"><span class="tl-time">' + time + '</span> <span class="tl-role tl-role-user">' + t('sessions.role_user') + '</span> <div class="tl-content">' + sess_msgBody(msg, content) + '</div></div>';
  if (type === 'assistant') return '<div class="tl-item tl-msg-assistant"><span class="tl-time">' + time + '</span> <span class="tl-role tl-role-assistant">' + t('sessions.role_assistant') + '</span> <div class="tl-content">' + sess_msgBody(msg, content) + '</div></div>';
  if (type === 'thinking') return '<div class="tl-item tl-msg-thinking" onclick="this.classList.toggle(\x27expanded\x27)"><span class="tl-time">' + time + '</span> <span class="tl-role" style="color:var(--purple)">' + t('sessions.role_thinking') + '</span> <div class="tl-content tl-thinking-content">' + escapeHTML(content) + '</div></div>';
  if (type === 'tool_use') {
    var toolLabel = msg.tool_name || 'tool';
    var as = summarizeToolArgs(toolLabel, content, sess_parseJSONField(msg, 'content'));
    return '<div class="tl-tool-card tl-tool-card-ok"><div class="tl-tool-card-header"><span class="tl-time">' + time + '</span><span class="tl-tool-name">' + escapeHTML(toolLabel) + '</span></div>' + (as ? '<div class="tl-tool-card-args">' + escapeHTML(as) + '</div>' : '') + '</div>';
  }
  if (type === 'tool_result') return '<div class="tl-item tl-tool-result"><span class="tl-time">' + time + '</span> <span class="tl-badge tl-badge-ok">\u2713</span> <span class="tl-result">' + escapeHTML(content.length > 200 ? content.slice(0, 200) + '\u2026' : content) + '</span></div>';
  if (type === 'tool_evidence' || type === 'tool_evidence_result') {
    var evidenceLabel = msg.tool_name || 'tool';
    var evidence = content.length > 300 ? content.slice(0, 300) + '\u2026' : content;
    return '<div class="tl-item"><span class="tl-time">' + time + '</span> <span class="tl-badge" style="background:rgba(148,163,184,0.14);color:var(--text-dim)">' + t('sessions.badge_evidence') + '</span> <strong>' + escapeHTML(evidenceLabel) + '</strong> <span class="tl-result">' + escapeHTML(evidence) + '</span></div>';
  }
  if (type === 'system' || type === 'developer') return '<div class="tl-item"><span class="tl-time">' + time + '</span> <span class="tl-role" style="color:var(--text-dim)">' + escapeHTML(type.toUpperCase()) + '</span> <div class="tl-content">' + escapeHTML(content) + '</div></div>';
  return '<div class="tl-item"><span class="tl-time">' + time + '</span> ' + escapeHTML(content) + '</div>';
}
