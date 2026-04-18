/* Sessions — timeline item rendering, filtering, and search helpers. */
/* globals: escapeHTML, escapeJSString, t, tsToMs, sessTimelineData, fmtDurMs */

// ── Timeline filter ───────────────────────────────────────────────────

var sessActiveToolFilter = '';

function sess_filterByTool(btn, toolName) {
  sessActiveToolFilter = toolName;
  document.querySelectorAll('.sess-chip').forEach(function(c) { c.classList.remove('active'); });
  btn.classList.add('active');
  sess_applyFilters();
}

function sess_filterTimeline() { sess_applyFilters(); }

function sess_applyFilters() {
  var keyword = (document.getElementById('sess-detail-filter').value || '').toLowerCase().trim();
  var filtered = sessTimelineData.filter(function(item) {
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
  var html = '';
  for (var i = 0; i < filtered.length; i++) html += renderTimelineItem(filtered[i]);
  container.innerHTML = html || '<div class="loading">' + t('empty.no_data') + '</div>';
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
  var argsSummary = summarizeToolArgs(tc.tool_name, tc.tool_input);

  var html = '<div class="tl-tool-card ' + statusClass + '">';
  html += '<div class="tl-tool-card-header">';
  html += '<span class="tl-time">' + time + '</span>';
  html += '<span class="tl-tool-name">' + escapeHTML(tc.tool_name || 'unknown') + '</span>';
  html += '<span class="tl-tool-dur" title="' + escapeHTML(durTitle) + '">' + durLabel + '</span>';
  html += '<span class="tl-tool-status">' + statusIcon + '</span>';
  html += '</div>';
  if (argsSummary) html += '<div class="tl-tool-card-args">' + escapeHTML(argsSummary) + '</div>';
  if (result) {
    html += '<details class="tl-tool-card-result"><summary>' + t('sessions.result') + '</summary>';
    html += formatToolResult(tc.tool_name, result);
    html += '</details>';
  }
  html += '</div>';
  return html;
}

function formatToolResult(toolName, result) {
  if (!result) return '';
  try {
    var obj = JSON.parse(result);
    if (typeof obj !== 'object' || obj === null) throw new Error('not an object');
    return '<div class="tl-result-structured">' + formatResultObj(toolName, obj) + '</div>';
  } catch (e) {
    var text = result.length > 2000 ? result.slice(0, 2000) + '\u2026' : result;
    return '<pre>' + escapeHTML(text) + '</pre>';
  }
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

function summarizeToolArgs(toolName, argsStr) {
  if (!argsStr) return '';
  try {
    var obj = JSON.parse(argsStr);
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
  try { return typeof ev.metadata === 'string' ? JSON.parse(ev.metadata) : ev.metadata; } catch (e) { return {}; }
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
    var args = summarizeToolArgs(ev.tool_name, ev.tool_input);
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

function renderMessage(msg, ts) {
  var time = ts ? new Date(ts).toLocaleTimeString() : '';
  var type = msg.message_type;
  var content = msg.content || '';
  if (type === 'user') return '<div class="tl-item tl-msg-user"><span class="tl-time">' + time + '</span> <span class="tl-role tl-role-user">' + t('sessions.role_user') + '</span> <div class="tl-content">' + escapeHTML(content) + '</div></div>';
  if (type === 'assistant') return '<div class="tl-item tl-msg-assistant"><span class="tl-time">' + time + '</span> <span class="tl-role tl-role-assistant">' + t('sessions.role_assistant') + '</span> <div class="tl-content">' + escapeHTML(content) + '</div></div>';
  if (type === 'thinking') return '<div class="tl-item tl-msg-thinking" onclick="this.classList.toggle(\x27expanded\x27)"><span class="tl-time">' + time + '</span> <span class="tl-role" style="color:var(--purple)">' + t('sessions.role_thinking') + '</span> <div class="tl-content tl-thinking-content">' + escapeHTML(content) + '</div></div>';
  if (type === 'tool_use') {
    var toolLabel = msg.tool_name || 'tool';
    var as = summarizeToolArgs(toolLabel, content);
    return '<div class="tl-tool-card tl-tool-card-ok"><div class="tl-tool-card-header"><span class="tl-time">' + time + '</span><span class="tl-tool-name">' + escapeHTML(toolLabel) + '</span></div>' + (as ? '<div class="tl-tool-card-args">' + escapeHTML(as) + '</div>' : '') + '</div>';
  }
  if (type === 'tool_result') return '<div class="tl-item tl-tool-result"><span class="tl-time">' + time + '</span> <span class="tl-badge tl-badge-ok">\u2713</span> <span class="tl-result">' + escapeHTML(content.length > 200 ? content.slice(0, 200) + '\u2026' : content) + '</span></div>';
  return '<div class="tl-item"><span class="tl-time">' + time + '</span> ' + escapeHTML(content) + '</div>';
}
