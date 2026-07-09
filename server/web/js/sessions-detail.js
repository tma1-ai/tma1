/* Sessions — detail overlay rendering, panel control, and scroll/highlight navigation. */
/* globals: t, escapeHTML, escapeJSString, fmtCost, fmtDurSec, fmtTokens, tsToMs, sess_parseAttrs,
   sessExpandedId, sessTimelineData, sessTargetTs, sessApiCallFP,
   sess_renderContextBar, sess_renderWaterfall, sess_renderAPICalls, sess_renderFileHeatmap,
   sess_renderAgentTree, sess_renderSubagentGantt, sess_renderTraceInsights, sess_initWaterfallClicks,
   renderTimelineItem, sess_renderTimelineWindow, sess_expandWindowTo,
   sessActiveToolFilter, sessFilteredData, sessRenderStart,
   sess_filterByTool, sess_filterTimeline, AgentCanvas */

// ── Render Session Detail (two-column overlay) ────────────────────────

function renderSessionDetail(timeline, stats) {
  var content = document.getElementById('sess-detail-content');
  if (!timeline.length) {
    content.innerHTML = '<div class="loading" style="padding:40px;text-align:center">' + t('empty.no_data') + '</div>';
    return;
  }

  var html = '';

  // ── Header ──
  html += '<div class="sess-overlay-header">';
  html += '<div class="sess-detail-kpi">';
  html += '<div class="sess-kpi"><span class="sess-kpi-label">' + t('sessions.kpi_duration') + '</span><span class="sess-kpi-value">' + fmtDurSec(stats.duration) + '</span></div>';
  html += '<div class="sess-kpi"><span class="sess-kpi-label">' + t('sessions.kpi_tools') + '</span><span class="sess-kpi-value">' + stats.toolCount + '</span></div>';

  var costLabel = stats.costSource === 'otel' ? t('sessions.kpi_cost') : t('sessions.kpi_cost') + ' ~';
  html += '<div class="sess-kpi"><span class="sess-kpi-label">' + costLabel + '</span><span class="sess-kpi-value cost">' + (stats.cost > 0 ? fmtCost(stats.cost) : '\u2014') + '</span></div>';

  // Tokens KPI. The reasoning slot only renders for Codex sessions
  // — Codex / OpenAI o-series report reasoning_token_count as a
  // separate bucket from output_tokens and bill it at the output
  // rate (cost calc in sess_parseCodexOTel and the JSONL fallback
  // both include it). CC + OpenClaw use Anthropic models where
  // thinking is already inside output_tokens, so reasoning isn't
  // surfaced separately; totalReasoningTokens stays 0 and the slot
  // hides naturally via the > 0 guard below.
  var tokLabel = stats.hasOTel ? t('sessions.kpi_tokens') : t('sessions.kpi_tokens') + ' ~';
  var tokText = fmtTokens(stats.totalInputTokens) + ' ' + t('sessions.token_in') + ' / ' + fmtTokens(stats.totalOutputTokens) + ' ' + t('sessions.token_out');
  if (stats.totalReasoningTokens > 0) tokText += ' / ' + fmtTokens(stats.totalReasoningTokens) + ' ' + t('sessions.token_reasoning');
  html += '<div class="sess-kpi"><span class="sess-kpi-label">' + tokLabel + '</span><span class="sess-kpi-value" style="font-size:14px">' + tokText + '</span></div>';

  // Cache KPI (only if OTel data available and cache > 0).
  if (stats.hasOTel && stats.totalCacheTokens > 0) {
    html += '<div class="sess-kpi"><span class="sess-kpi-label">' + t('sessions.kpi_cache') + '</span><span class="sess-kpi-value" style="font-size:14px">' + Math.round(stats.cacheHitRatio * 100) + '%</span></div>';
  }

  // Buttons — right-aligned.
  var lastEvent = timeline[timeline.length - 1];
  var lastIsEnd = lastEvent && lastEvent.source === 'hook' &&
    (lastEvent.data.event_type === 'SessionEnd' || lastEvent.data.event_type === 'Stop');
  var isRecent = lastEvent && (Date.now() - lastEvent.ts) < 10 * 60 * 1000;
  var isActive = isRecent && !lastIsEnd;
  html += '<div class="sess-kpi" style="margin-left:auto;display:flex;gap:6px;align-items:flex-end">';
  if (isActive) {
    html += '<button class="filter-btn" onclick="AgentCanvas.open(\x27live\x27,{sessionId:\x27' + escapeJSString(sessExpandedId) + '\x27})">' + t('sessions.btn_live_canvas') + '</button>';
  }
  html += '<button class="filter-btn" onclick="AgentCanvas.open(\x27replay\x27,{timelineData:sessTimelineData,speed:1,sessionId:\x27' + escapeJSString(sessExpandedId) + '\x27})">\u25B6 ' + t('sessions.btn_replay') + '</button>';
  html += '<button class="sess-close-btn" onclick="sess_closeDetail()" title="' + t('ui.close') + '" aria-label="' + t('ui.close') + '">\u2715</button>';
  html += '</div>';
  html += '</div>'; // .sess-detail-kpi

  // Secondary row.
  html += '<div class="sess-kpi-secondary">';
  html += '<span style="font-family:monospace;font-size:11px" title="' + escapeHTML(sessExpandedId || '') + '">' + escapeHTML(sessExpandedId || '') + '</span>';
  if (stats.primaryModel) html += '<span>' + escapeHTML(stats.primaryModel) + '</span>';
  if (stats.errorCount > 0) html += '<span class="badge badge-error clickable" onclick="sess_toggleErrors()" style="cursor:pointer">' + stats.errorCount + ' ' + t(stats.errorCount > 1 ? 'sessions.errors_badge_plural' : 'sessions.errors_badge') + '</span>';
  html += '</div>';
  // Error details panel (hidden, toggled by clicking error badge).
  if (stats.apiErrors.length > 0) {
    html += '<div id="sess-error-panel" style="display:none;padding:8px 24px;border-bottom:1px solid var(--border);max-height:200px;overflow-y:auto">';
    for (var ei = 0; ei < stats.apiErrors.length; ei++) {
      var err = stats.apiErrors[ei];
      var ea = sess_parseAttrs(err.log_attributes);
      var errMsg = (ea && ea.error) || t('sessions.error_unknown');
      var errModel = (ea && ea.model) || '';
      var errTs = tsToMs(err.timestamp);
      var errTime = errTs ? new Date(errTs).toLocaleTimeString() : '';
      html += '<div class="tl-item clickable" style="padding:4px 8px;font-size:12px;cursor:pointer" onclick="sess_scrollToEvent(document.getElementById(\x27sess-timeline-scroll\x27),' + (errTs || 0) + ')">';
      html += '<span class="tl-time">' + errTime + '</span>';
      html += '<span class="badge badge-error" style="font-size:10px">' + t('ui.error') + '</span> ';
      if (errModel) html += '<span style="color:var(--text-dim)">' + escapeHTML(errModel) + '</span> ';
      html += '<span>' + escapeHTML(errMsg) + '</span>';
      html += '</div>';
    }
    html += '</div>';
  }
  html += '</div>'; // .sess-overlay-header

  // ── Two-column body ──
  html += '<div class="sess-overlay-body">';

  // Left: Insights panel.
  html += '<div class="sess-insights-panel">';
  html += '<button class="sess-panel-toggle" onclick="sess_togglePanel(\x27left\x27)" title="' + t('ui.expand') + '">&#x21C9;</button>';
  html += sess_renderContextBar(stats.context);
  // Anomalies block (filled async after render — see sess_loadDetailAnomalies).
  // Starts hidden so the empty-state path leaves no vertical whitespace;
  // sess_loadDetailAnomalies flips display back to "" when there are
  // anomalies to show. Margin only applies when content is present.
  html += '<div id="sess-detail-anomalies" style="display:none;margin:8px 0"></div>';
  html += sess_renderWaterfall(timeline, stats);
  if (stats.apiCalls.length > 0) html += sess_renderAPICalls(stats);
  html += sess_renderFileHeatmap(stats.files);
  if (stats.agentSpans && stats.agentSpans.length > 0) {
    html += sess_renderSubagentGantt(stats.agentSpans, stats.sessionStart, stats.sessionEnd, stats.agentToolCounts || {});
  } else if (stats.agents.length > 0) {
    html += sess_renderAgentTree(stats.agents, stats.agentToolCounts || {});
  }
  if (stats.ccTraceSpans && stats.ccTraceSpans.length > 0) html += sess_renderTraceInsights(stats.ccTraceSpans);
  html += '</div>';

  // Right: Timeline panel.
  html += '<div class="sess-timeline-panel">';
  html += '<button class="sess-panel-toggle" onclick="sess_togglePanel(\x27right\x27)" title="' + t('ui.expand') + '">&#x21C7;</button>';

  // Toolbar.
  var toolNames = {};
  for (var i = 0; i < timeline.length; i++) {
    var tn = null;
    if (timeline[i].source === 'tool_pair') tn = timeline[i].data.tool_name;
    else if (timeline[i].source === 'hook' && timeline[i].data.event_type === 'PreToolUse') tn = timeline[i].data.tool_name;
    else if (timeline[i].source === 'message' && timeline[i].data.message_type === 'tool_use') tn = timeline[i].data.tool_name;
    if (tn) toolNames[tn] = true;
  }
  html += '<div class="sess-detail-toolbar">';
  html += '<input class="sess-detail-filter" id="sess-detail-filter" type="text" placeholder="' + t('sessions.filter_placeholder') + '" oninput="sess_filterTimeline()" />';
  html += '<div class="sess-detail-chips">';
  html += '<button class="sess-chip active" onclick="sess_filterByTool(this, \'\')">' + t('sessions.chip_all') + '</button>';
  var toolList = Object.keys(toolNames).sort();
  for (var k = 0; k < toolList.length; k++) {
    html += '<button class="sess-chip" onclick="sess_filterByTool(this, \x27' + escapeJSString(toolList[k]) + '\x27)">' + escapeHTML(toolList[k]) + '</button>';
  }
  html += '</div></div>';

  // Timeline. Items are rendered lazily by sess_renderTimelineWindow
  // (windowed tail render) after the structure is in the DOM.
  html += '<div class="sess-timeline-scroll" id="sess-timeline-scroll">';
  html += '<div class="sess-timeline" id="sess-timeline-items">';
  html += '</div></div>';

  html += '</div>'; // .sess-timeline-panel
  html += '</div>'; // .sess-overlay-body

  content.innerHTML = html;
  // The chips above were rebuilt with "All" active — reset the module-level
  // tool filter so a stale filter from a previously opened detail doesn't
  // leak into this session's windowed render.
  sessActiveToolFilter = '';
  sess_renderTimelineWindow(true);
  sess_initWaterfallClicks();
  if (sessExpandedId) sess_loadDetailAnomalies(sessExpandedId);
  var scrollEl = document.getElementById('sess-timeline-scroll');
  if (scrollEl) {
    if (sessTargetTs) {
      sess_scrollToEvent(scrollEl, sessTargetTs);
      if (sessApiCallFP) sess_highlightAPICall(sessApiCallFP);
      sessTargetTs = 0;
      sessApiCallFP = '';
    } else {
      scrollEl.scrollTop = scrollEl.scrollHeight;
    }
  }
}

// ── Panel toggle ──────────────────────────────────────────────────────

function sess_togglePanel(side) {
  var body = document.querySelector('.sess-overlay-body');
  if (!body) return;
  var cls = 'expand-' + side;
  if (body.classList.contains(cls)) {
    body.classList.remove(cls);
  } else {
    body.classList.remove('expand-left', 'expand-right');
    body.classList.add(cls);
  }
}

function sess_toggleErrors() {
  var panel = document.getElementById('sess-error-panel');
  if (panel) panel.style.display = panel.style.display === 'none' ? 'block' : 'none';
}

// ── Scroll / highlight navigation ─────────────────────────────────────

// Scroll to the timeline item closest to the target timestamp and highlight it.
// With windowed rendering the target may live before the rendered window —
// locate it in sessFilteredData by the same criterion (closest data-ts)
// and expand the window first so the DOM search below can find it.
function sess_scrollToEvent(scrollEl, targetMs) {
  var bestIdx = -1, bestDataDiff = Infinity;
  for (var d = 0; d < sessFilteredData.length; d++) {
    var dDiff = Math.abs((sessFilteredData[d].ts || 0) - targetMs);
    if (dDiff < bestDataDiff) { bestDataDiff = dDiff; bestIdx = d; }
  }
  if (bestIdx >= 0 && bestIdx < sessRenderStart) sess_expandWindowTo(bestIdx);
  var items = scrollEl.querySelectorAll('.tl-item-wrap[data-ts]');
  var best = null, bestDiff = Infinity;
  for (var i = 0; i < items.length; i++) {
    var ts = Number(items[i].getAttribute('data-ts'));
    var diff = Math.abs(ts - targetMs);
    if (diff < bestDiff) { bestDiff = diff; best = items[i]; }
  }
  if (!best) { scrollEl.scrollTop = scrollEl.scrollHeight; return; }
  // Clear previous highlight.
  var prev = scrollEl.querySelector('.tl-highlight');
  if (prev) prev.classList.remove('tl-highlight');
  best.classList.add('tl-highlight');
  best.scrollIntoView({ block: 'center' });
}

// Scroll to a timeline tool_pair by tool_use_id (precise CC linkage).
function sess_scrollToToolUseId(toolUseId) {
  var scrollEl = document.getElementById('sess-timeline-scroll');
  if (!scrollEl) return;
  // The data-tool-use-id attribute is only emitted for tool_pair items
  // (renderTimelineItem) — match the same field on the data objects and
  // expand the rendered window if the target sits above it.
  for (var d = 0; d < sessFilteredData.length; d++) {
    var it = sessFilteredData[d];
    if (it.source === 'tool_pair' && it.data.tool_use_id === toolUseId) {
      if (d < sessRenderStart) sess_expandWindowTo(d);
      break;
    }
  }
  var items = scrollEl.querySelectorAll('.tl-item-wrap[data-tool-use-id]');
  var target = null;
  for (var i = 0; i < items.length; i++) {
    if (items[i].getAttribute('data-tool-use-id') === toolUseId) { target = items[i]; break; }
  }
  if (!target) {
    // Fallback: find by data-tool-use-ids containing this id.
    var allWraps = scrollEl.querySelectorAll('.tl-item-wrap');
    for (var j = 0; j < allWraps.length; j++) {
      var ids = allWraps[j].getAttribute('data-tool-use-id') || '';
      if (ids === toolUseId) { target = allWraps[j]; break; }
    }
  }
  if (!target) return;
  var prev = scrollEl.querySelector('.tl-highlight');
  if (prev) prev.classList.remove('tl-highlight');
  target.classList.add('tl-highlight');
  target.scrollIntoView({ block: 'center' });
}

// Expand the API Calls section and highlight the row matching the fingerprint.
function sess_highlightAPICall(fingerprint) {
  var table = document.querySelector('.sess-api-table');
  if (!table) return;
  var details = table.closest('details.sess-section');
  if (details) details.open = true;
  // Try exact fingerprint match first, then fallback to closest timestamp.
  var trs = table.querySelectorAll('tr[data-fp]');
  var best = null;
  for (var i = 0; i < trs.length; i++) {
    if (trs[i].getAttribute('data-fp') === fingerprint) { best = trs[i]; break; }
  }
  if (!best) {
    // Fallback: try as numeric timestamp for Codex (nanosecond string).
    var targetMs = Number(fingerprint);
    if (targetMs > 0) {
      var bestDiff = Infinity;
      for (var j = 0; j < trs.length; j++) {
        var ts = Number(trs[j].getAttribute('data-ts'));
        var diff = Math.abs(ts - targetMs);
        if (diff < bestDiff) { bestDiff = diff; best = trs[j]; }
      }
    }
  }
  if (!best) return;
  best.classList.add('tl-highlight');
  best.scrollIntoView({ block: 'nearest' });
}

// Toggle the info popover shown next to tool-card duration labels. Closes any
// other open popover and auto-dismisses on the next outside click.
function sess_toggleDurPopover(e, info) {
  e.stopPropagation();
  var wasActive = info.classList.contains('active');
  var opened = document.querySelectorAll('.tl-tool-dur-info.active');
  for (var i = 0; i < opened.length; i++) opened[i].classList.remove('active');
  if (wasActive) return;
  info.classList.add('active');
  setTimeout(function() {
    document.addEventListener('click', function() {
      info.classList.remove('active');
    }, { once: true });
  }, 0);
}

// Load + render anomalies detected for this session into the slot inserted
// by renderSessionDetail. Uses the same .sess-section / <details> pattern
// as Waterfall / API calls / Heatmap so it visually slots in as just
// another collapsible insight — and the same compact .anom-row primitive
// from anomalies.js for the row markup.
//
// Default closed; the summary alone gives the agent/user the count + a
// severity breakdown they can scan at a glance, without paying the
// vertical real estate cost of the expanded rows.
async function sess_loadDetailAnomalies(sessionID) {
  var slot = document.getElementById('sess-detail-anomalies');
  if (!slot) return;
  // Helper: keep the empty-state path symmetric -- both the "no anomalies"
  // and "fetch failed" branches must clear content AND hide the slot so
  // its margin doesn't leak whitespace into the layout.
  function clearSlot() { slot.innerHTML = ''; slot.style.display = 'none'; }
  try {
    var resp = await fetch('/api/anomalies?session_id=' + encodeURIComponent(sessionID));
    if (!resp.ok) { clearSlot(); return; }
    var data = await resp.json();
    var items = data.anomalies || [];
    if (items.length === 0) { clearSlot(); return; }

    var sevCounts = { high: 0, medium: 0, low: 0 };
    items.forEach(function (a) { sevCounts[a.severity] = (sevCounts[a.severity] || 0) + 1; });
    var sevParts = [];
    if (sevCounts.high) sevParts.push('<span style="color:var(--red)">' + sevCounts.high + ' ' + t('anom.sev.high') + '</span>');
    if (sevCounts.medium) sevParts.push('<span style="color:var(--orange)">' + sevCounts.medium + ' ' + t('anom.sev.medium') + '</span>');
    if (sevCounts.low) sevParts.push('<span style="color:var(--text-dim)">' + sevCounts.low + ' ' + t('anom.sev.low') + '</span>');
    var sevSummary = sevParts.length ? ' · ' + sevParts.join(' · ') : '';

    var rows = items.map(function (a) { return anom_renderRow(a, false); }).join('');
    slot.style.display = '';
    slot.innerHTML =
      '<details class="sess-section">' +
      '<summary>' + t('anom.session_summary') + ' (' + items.length + ')' + sevSummary + '</summary>' +
      '<div class="anom-detail-list">' + rows + '</div>' +
      '</details>';
  } catch (err) {
    console.error('sess_loadDetailAnomalies', err);
    clearSlot();
  }
}
