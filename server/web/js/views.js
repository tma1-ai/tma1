// views.js — view management, theme, tab navigation, init
// Depends on: core.js, chart.js, i18n.js, traces.js, claude-code.js, codex.js
// Must be loaded LAST. Calls initViews() at the bottom.

var currentView = null; // 'claude-code', 'codex', 'openclaw', or 'traces'
var autoRefreshTimer = null;
var refreshInFlight = false;
var dataSources = {
  hasLogs: false,
  hasTraces: false,
  hasOpenClaw: false,
  hasGenAITraces: false,
  hasClaudeLogs: false,
  hasCodex: false,
  ccMetrics: [],
  codexMetrics: [],
};

async function detectDataSources() {
  try {
    var res = await query(
      "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public'"
    );
    var tables = rowsToObjects(res).map(function(r) { return r.table_name; });
    var result = {
      hasLogs: tables.includes('opentelemetry_logs'),
      hasTraces: tables.includes('opentelemetry_traces'),
      hasOpenClaw: false,
      hasGenAITraces: false,
      hasClaudeLogs: false,
      hasCodex: false,
      ccMetrics: tables.filter(function(t) { return t.startsWith('claude_code_'); }),
      codexMetrics: tables.filter(function(t) { return t.startsWith('codex_'); }),
      ocMetrics: tables.filter(function(t) { return t.startsWith('openclaw_'); }),
    };
    result.hasCodex = result.codexMetrics.length > 0;
    result.hasOpenClaw = result.ocMetrics.length > 0;
    if (result.hasLogs) {
      try {
        var logKindRes = await query(
          "SELECT " +
          "SUM(CASE WHEN body LIKE 'claude_code.%' OR scope_name = 'com.anthropic.claude_code.events' THEN 1 ELSE 0 END) AS claude_rows, " +
          "SUM(CASE WHEN scope_name LIKE 'codex_%' THEN 1 ELSE 0 END) AS codex_rows " +
          "FROM opentelemetry_logs"
        );
        var logKindRow = rows(logKindRes)?.[0] || [];
        result.hasClaudeLogs = (Number(logKindRow[0]) || 0) > 0;
        result.hasCodex = (Number(logKindRow[1]) || 0) > 0;
      } catch { /* ignore */ }
    }
    if (result.hasTraces) {
      try {
        var colRes = await query(
          "SELECT column_name FROM information_schema.columns " +
          "WHERE table_name = 'opentelemetry_traces' AND table_schema = 'public'"
        );
        var columns = rowsToObjects(colRes).map(function(r) { return r.column_name; });
        result.hasOpenClaw = columns.some(function(c) { return c.indexOf('span_attributes.openclaw.') === 0; });
        result.hasGenAITraces = columns.some(function(c) { return c.indexOf('span_attributes.gen_ai.') === 0; });
      } catch { /* column detection failed, fall back to showing traces view */ }
      if (!result.hasCodex) {
        try {
          var codexTraceRes = await query(
            "SELECT COUNT(*) AS cnt FROM opentelemetry_traces WHERE service_name = 'codex_cli_rs'"
          );
          result.hasCodex = (Number(rows(codexTraceRes)?.[0]?.[0]) || 0) > 0;
        } catch { /* ignore */ }
      }
      // Detect OpenClaw via span_name when columns are not yet registered.
      if (!result.hasOpenClaw) {
        try {
          var ocSpanRes = await query(
            "SELECT 1 FROM opentelemetry_traces WHERE span_name LIKE 'openclaw.%' LIMIT 1"
          );
          result.hasOpenClaw = (rows(ocSpanRes) || []).length > 0;
        } catch { /* ignore */ }
      }
      // Fallback: if no recognized schema found, default to GenAI.
      if (!result.hasOpenClaw && !result.hasGenAITraces) {
        result.hasGenAITraces = true;
      }
    }
    return result;
  } catch {
    return {
      hasLogs: false,
      hasTraces: false,
      hasOpenClaw: false,
      hasGenAITraces: false,
      hasClaudeLogs: false,
      hasCodex: false,
      ccMetrics: [],
      codexMetrics: [],
    };
  }
}

function updateHash() {
  var hash = currentView || '';
  if (currentView === 'claude-code') {
    var tab = document.querySelector('#cc-tabs .tab.active');
    var tabName = tab ? tab.dataset.cctab : null;
    if (tabName && tabName !== 'cc-overview') hash += '/' + tabName;
  } else if (currentView === 'codex') {
    var cdxTab = document.querySelector('#cdx-tabs .tab.active');
    var cdxTabName = cdxTab ? cdxTab.dataset.cdxtab : null;
    if (cdxTabName && cdxTabName !== 'cdx-overview') hash += '/' + cdxTabName;
  } else if (currentView === 'openclaw') {
    var tab3 = document.querySelector('#oc-tabs .tab.active');
    var tabName3 = tab3 ? tab3.dataset.octab : null;
    if (tabName3 && tabName3 !== 'oc-overview') hash += '/' + tabName3;
  } else if (currentView === 'traces') {
    var tab2 = document.querySelector('#view-traces .tab.active');
    var tabName2 = tab2 ? tab2.dataset.tab : null;
    if (tabName2 && tabName2 !== 'overview') hash += '/' + tabName2;
  }
  hash += '/' + currentTimeRange;
  history.replaceState(null, '', '#' + hash);
}

function parseHash() {
  var h = location.hash.replace('#', '');
  if (!h) return { view: null, tab: null, range: null };
  var parts = h.split('/');
  var validRanges = ['1h', '6h', '24h', '7d', '30d'];
  var range = null;
  if (parts.length > 0 && validRanges.includes(parts[parts.length - 1])) {
    range = parts.pop();
  }
  return { view: parts[0] || null, tab: parts[1] || null, range: range };
}

async function switchView(viewId, skipHash) {
  document.getElementById('view-claude-code').style.display = 'none';
  document.getElementById('view-codex').style.display = 'none';
  document.getElementById('view-openclaw').style.display = 'none';
  document.getElementById('view-traces').style.display = 'none';
  document.getElementById('setup-notice').style.display = 'none';

  currentView = viewId;
  var viewEl = document.getElementById('view-' + viewId);
  if (viewEl) {
    var hasData = true;
    if (viewId === 'claude-code') {
      hasData = await cc_loadCards();
      if (hasData) cc_loadOverview();
    } else if (viewId === 'codex') {
      hasData = await cdx_loadCards();
      if (hasData) cdx_loadOverview();
    } else if (viewId === 'openclaw') {
      hasData = await oc_loadCards();
      if (hasData) oc_loadOverview();
    } else if (viewId === 'traces') {
      await loadPricing();
      hasData = await loadMetrics();
      if (hasData) loadOverviewCharts();
    }
    if (hasData) {
      viewEl.style.display = 'block';
    } else {
      document.getElementById('setup-notice').style.display = 'block';
    }
  }
  checkDataFreshness();
  if (!skipHash) updateHash();
}

async function initViews() {
  dataSources = await detectDataSources();
  var hasCCView = dataSources.hasClaudeLogs || dataSources.ccMetrics.length > 0;
  var viewSelect = document.getElementById('view-select');

  viewSelect.innerHTML = '';
  var views = [];
  if (hasCCView) views.push({ id: 'claude-code', label: 'Claude Code' });
  if (dataSources.hasCodex) views.push({ id: 'codex', label: 'Codex' });
  if (dataSources.hasOpenClaw) views.push({ id: 'openclaw', label: 'OpenClaw' });
  if (dataSources.hasGenAITraces) views.push({ id: 'traces', label: 'OTel GenAI' });

  if (views.length === 0) {
    document.getElementById('setup-notice').style.display = 'block';
    viewSelect.style.display = 'none';
    return;
  }

  views.forEach(function(v) {
    var opt = document.createElement('option');
    opt.value = v.id;
    opt.textContent = v.label;
    viewSelect.appendChild(opt);
  });

  if (views.length > 1) {
    viewSelect.style.display = '';
  } else {
    viewSelect.style.display = 'none';
  }

  // Restore view + tab + time range from URL hash
  var hash = parseHash();
  if (hash.range) {
    currentTimeRange = hash.range;
    document.getElementById('time-range').value = hash.range;
  }
  var viewIds = views.map(function(v) { return v.id; });
  var targetView = hash.view && viewIds.includes(hash.view) ? hash.view : views[0].id;
  viewSelect.value = targetView;
  switchView(targetView, true);

  // Restore tab within view
  if (hash.tab) {
    if (targetView === 'claude-code') {
      var tabBtn = document.querySelector('#cc-tabs .tab[data-cctab="' + hash.tab + '"]');
      if (tabBtn) tabBtn.click();
    } else if (targetView === 'codex') {
      var tabBtnCodex = document.querySelector('#cdx-tabs .tab[data-cdxtab="' + hash.tab + '"]');
      if (tabBtnCodex) tabBtnCodex.click();
    } else if (targetView === 'openclaw') {
      var tabBtn3 = document.querySelector('#oc-tabs .tab[data-octab="' + hash.tab + '"]');
      if (tabBtn3) tabBtn3.click();
    } else if (targetView === 'traces') {
      var tabBtn2 = document.querySelector('#view-traces .tab[data-tab="' + hash.tab + '"]');
      if (tabBtn2) tabBtn2.click();
    }
  }
  updateHash();
  onAutoRefreshChange();
}

// ===================================================================
// Tab navigation (Traces view)
// ===================================================================
document.querySelectorAll('#view-traces .tab').forEach(function(btn) {
  btn.addEventListener('click', function() {
    document.querySelectorAll('#view-traces .tab').forEach(function(t) { t.classList.remove('active'); });
    document.querySelectorAll('#view-traces .tab-content').forEach(function(t) { t.classList.remove('active'); });
    btn.classList.add('active');
    document.getElementById('tab-' + btn.dataset.tab).classList.add('active');
    onTabChange(btn.dataset.tab);
    updateHash();
  });
});

async function onTabChange(tab) {
  if (tab === 'traces') loadTraces();
  else if (tab === 'cost') { await loadPricing(); loadCostTab(); }
  else if (tab === 'security') loadSecurityTab();
  else if (tab === 'search') loadAnomalies();
  else if (tab === 'overview') { await loadPricing(); loadOverviewCharts(); }
}

// Tab navigation (OpenClaw view)
document.querySelectorAll('#oc-tabs .tab').forEach(function(btn) {
  btn.addEventListener('click', function() {
    document.querySelectorAll('#oc-tabs .tab').forEach(function(t) { t.classList.remove('active'); });
    document.querySelectorAll('#view-openclaw .tab-content').forEach(function(t) { t.classList.remove('active'); });
    btn.classList.add('active');
    document.getElementById('tab-' + btn.dataset.octab).classList.add('active');
    oc_onTabChange(btn.dataset.octab);
    updateHash();
  });
});

// Tab navigation (Claude Code view)
document.querySelectorAll('#cc-tabs .tab').forEach(function(btn) {
  btn.addEventListener('click', function() {
    document.querySelectorAll('#cc-tabs .tab').forEach(function(t) { t.classList.remove('active'); });
    document.querySelectorAll('#view-claude-code .tab-content').forEach(function(t) { t.classList.remove('active'); });
    btn.classList.add('active');
    document.getElementById('tab-' + btn.dataset.cctab).classList.add('active');
    cc_onTabChange(btn.dataset.cctab);
    updateHash();
  });
});

function cc_onTabChange(tab) {
  if (tab === 'cc-overview') cc_loadOverview();
  else if (tab === 'cc-sessions') cc_loadSessions();
  else if (tab === 'cc-tools') cc_loadToolsTab();
  else if (tab === 'cc-cost') cc_loadCostTab();
  else if (tab === 'cc-search') cc_loadAnomalies();
}

// Tab navigation (Codex view)
document.querySelectorAll('#cdx-tabs .tab').forEach(function(btn) {
  btn.addEventListener('click', function() {
    document.querySelectorAll('#cdx-tabs .tab').forEach(function(t) { t.classList.remove('active'); });
    document.querySelectorAll('#view-codex .tab-content').forEach(function(t) { t.classList.remove('active'); });
    btn.classList.add('active');
    document.getElementById('tab-' + btn.dataset.cdxtab).classList.add('active');
    cdx_onTabChange(btn.dataset.cdxtab);
    updateHash();
  });
});

function onTimeRangeChange() {
  currentTimeRange = document.getElementById('time-range').value;
  updateHash();
  refreshCurrentView();
}

function onAutoRefreshChange() {
  if (autoRefreshTimer) {
    clearInterval(autoRefreshTimer);
    autoRefreshTimer = null;
  }
  var seconds = parseInt(document.getElementById('auto-refresh').value, 10);
  if (!seconds) return;
  autoRefreshTimer = setInterval(function() {
    if (refreshInFlight) return;
    refreshInFlight = true;
    refreshCurrentView().finally(function() { refreshInFlight = false; });
  }, seconds * 1000);
}

document.addEventListener('visibilitychange', function() {
  if (document.hidden) {
    if (autoRefreshTimer) {
      clearInterval(autoRefreshTimer);
      autoRefreshTimer = null;
    }
  } else {
    onAutoRefreshChange();
  }
});

async function refreshCurrentView() {
  if (!currentView) return;
  var hasData = true;
  if (currentView === 'traces') {
    hasData = await loadMetrics();
    if (hasData) {
      var activeTab = document.querySelector('#view-traces .tab.active');
      if (activeTab) onTabChange(activeTab.dataset.tab);
    }
  } else if (currentView === 'openclaw') {
    hasData = await oc_loadCards();
    if (hasData) {
      var activeTab3 = document.querySelector('#oc-tabs .tab.active');
      if (activeTab3) oc_onTabChange(activeTab3.dataset.octab);
    }
  } else if (currentView === 'claude-code') {
    hasData = await cc_loadCards();
    if (hasData) {
      var activeTab2 = document.querySelector('#cc-tabs .tab.active');
      if (activeTab2) cc_onTabChange(activeTab2.dataset.cctab);
    }
  } else if (currentView === 'codex') {
    hasData = await cdx_loadCards();
    if (hasData) {
      var activeCodexTab = document.querySelector('#cdx-tabs .tab.active');
      if (activeCodexTab) cdx_onTabChange(activeCodexTab.dataset.cdxtab);
    }
  }
  var viewEl = document.getElementById('view-' + currentView);
  var setupEl = document.getElementById('setup-notice');
  if (hasData) {
    if (viewEl) viewEl.style.display = 'block';
    if (setupEl) setupEl.style.display = 'none';
  } else {
    if (viewEl) viewEl.style.display = 'none';
    if (setupEl) setupEl.style.display = 'block';
  }
}

// ===================================================================
// Status check
// ===================================================================
async function checkStatus() {
  try {
    var r = await fetch('/status');
    var data = await r.json();
    var dot = document.getElementById('status-dot');
    if (data.greptimedb === 'running') {
      dot.classList.remove('offline');
    } else {
      dot.classList.add('offline');
    }
  } catch {
    document.getElementById('status-dot').classList.add('offline');
  }
  checkDataFreshness();
}

// ===================================================================
// Data freshness indicator
// ===================================================================
async function checkDataFreshness() {
  var el = document.getElementById('data-freshness');
  if (!el || !currentView) { if (el) el.textContent = ''; return; }

  var sql;
  if (currentView === 'claude-code') {
    // Check both logs and metrics — use whichever is newer
    try {
      var ccResults = await Promise.all([
        query("SELECT MAX(timestamp) AS last_ts FROM opentelemetry_logs").catch(function() { return null; }),
        query("SELECT MAX(greptime_timestamp) AS last_ts FROM claude_code_token_usage_tokens_total").catch(function() { return null; }),
      ]);
      var ts1 = ccResults[0] && rows(ccResults[0])?.[0]?.[0];
      var ts2 = ccResults[1] && rows(ccResults[1])?.[0]?.[0];
      var lastTs = null;
      if (ts1 && ts2) lastTs = parseTimestamp(ts1) > parseTimestamp(ts2) ? ts1 : ts2;
      else lastTs = ts1 || ts2;
      renderFreshness(el, lastTs);
    } catch { el.textContent = ''; el.className = 'data-freshness'; }
    return;
  } else if (currentView === 'codex') {
    try {
      var codexResults = await Promise.all([
        query("SELECT MAX(timestamp) AS last_ts FROM opentelemetry_logs WHERE scope_name LIKE 'codex_%'").catch(function() { return null; }),
        query("SELECT MAX(greptime_timestamp) AS last_ts FROM codex_turn_token_usage_sum").catch(function() { return null; }),
      ]);
      var cTs1 = codexResults[0] && rows(codexResults[0])?.[0]?.[0];
      var cTs2 = codexResults[1] && rows(codexResults[1])?.[0]?.[0];
      var cLastTs = [cTs1, cTs2].filter(Boolean).reduce(function(latest, ts) {
        if (!latest) return ts;
        return parseTimestamp(ts) > parseTimestamp(latest) ? ts : latest;
      }, null);
      renderFreshness(el, cLastTs);
    } catch { el.textContent = ''; el.className = 'data-freshness'; }
    return;
  } else if (currentView === 'openclaw') {
    sql = "SELECT MAX(timestamp) AS last_ts FROM opentelemetry_traces WHERE span_name LIKE 'openclaw.%'";
  } else if (currentView === 'traces') {
    sql = "SELECT MAX(timestamp) AS last_ts FROM opentelemetry_traces WHERE \"span_attributes.gen_ai.system\" IS NOT NULL";
  } else {
    el.textContent = '';
    return;
  }

  try {
    var res = await query(sql);
    var r = rows(res);
    lastTs = r && r[0] && r[0][0];
    renderFreshness(el, lastTs);
  } catch {
    el.textContent = '';
    el.className = 'data-freshness';
  }
}

function parseTimestamp(val) {
  if (!val) return NaN;
  if (typeof val === 'number') {
    if (val > 1e15) return val / 1e6;
    if (val > 1e12) return val / 1e3;
    return val;
  }
  var s = String(val).replace(' ', 'T');
  s = s.replace(/(\.\d{3})\d+/, '$1');
  s = s.replace(/([+-]\d{2})(\d{2})$/, '$1:$2');
  if (!/[Z+-]/.test(s.slice(-6))) s += 'Z';
  return new Date(s).getTime();
}

function renderFreshness(el, lastTs) {
  if (!lastTs) {
    el.textContent = t('freshness.no_data');
    el.className = 'data-freshness very-stale';
    return;
  }
  var diffMs = Date.now() - parseTimestamp(lastTs);
  if (isNaN(diffMs) || diffMs < 0) {
    el.textContent = t('freshness.no_data');
    el.className = 'data-freshness very-stale';
    return;
  }
  var diffSec = Math.floor(diffMs / 1000);
  var label;
  if (diffSec < 60) label = diffSec + 's ago';
  else if (diffSec < 3600) label = Math.floor(diffSec / 60) + 'm ago';
  else label = Math.floor(diffSec / 3600) + 'h ago';

  el.textContent = t('freshness.last_data') + label;
  el.className = 'data-freshness';
  if (diffSec > 3600) el.classList.add('very-stale');
  else if (diffSec > 300) el.classList.add('stale');
}

// ===================================================================
// Theme management
// ===================================================================
function getSystemTheme() {
  return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
}

function setTheme(mode) {
  // mode: 'light', 'dark', or 'system'
  localStorage.setItem('tma1-theme', mode);
  var resolved = mode === 'system' ? getSystemTheme() : mode;
  document.documentElement.setAttribute('data-theme', resolved);
  // Update toggle buttons
  document.querySelectorAll('.theme-toggle button').forEach(function(btn) {
    btn.classList.toggle('active', btn.dataset.theme === mode);
  });
  // Rebuild current view charts (colors change with theme)
  if (currentView === 'claude-code') {
    cc_loadOverview();
  } else if (currentView === 'codex') {
    cdx_loadOverview();
  } else if (currentView === 'openclaw') {
    oc_loadOverview();
  } else if (currentView === 'traces') {
    loadOverviewCharts();
  }
}

function initTheme() {
  var saved = localStorage.getItem('tma1-theme') || 'dark';
  var resolved = saved === 'system' ? getSystemTheme() : (saved === 'light' || saved === 'dark' ? saved : 'dark');
  document.documentElement.setAttribute('data-theme', resolved);
  document.querySelectorAll('.theme-toggle button').forEach(function(btn) {
    btn.classList.toggle('active', btn.dataset.theme === saved);
  });
  // Listen for OS theme changes when in system mode
  window.matchMedia('(prefers-color-scheme: light)').addEventListener('change', function() {
    if (localStorage.getItem('tma1-theme') === 'system') {
      document.documentElement.setAttribute('data-theme', getSystemTheme());
    }
  });
}

// ===================================================================
// Init
// ===================================================================
initTheme();
initLocale();
checkStatus();
initViews();
setInterval(checkStatus, 10000);
