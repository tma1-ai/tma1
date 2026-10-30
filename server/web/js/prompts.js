// prompts.js — Prompt Evaluation & Improvement view
// Depends on core.js and i18n.js.

var prPage = 0;
var prPageSize = 50;
var prHasNext = false;
var prPromptData = []; // prompts on the current page
var prLLMAvailable = null; // null = unchecked, true/false
var prExpandedIdx = -1;

// pr_sourceSessionIDs fetches session IDs matching the selected agent_source filter.
// Returns null when "All" is selected (no filter needed), or a SQL IN-list string.
// Uses a two-step approach instead of a subquery to avoid GreptimeDB memory issues.
var prSourceCache = { range: null, source: null, ids: null };

async function pr_sourceSessionIDs() {
  var el = document.getElementById('pr-source-filter');
  if (!el || !el.value) return null; // "All" — no filter

  var src = el.value;
  var iv = intervalSQL();
  var cacheKey = iv + ':' + src;
  if (prSourceCache.range === cacheKey) return prSourceCache.ids;

  var res = await query(
    "SELECT session_id FROM tma1_hook_events WHERE agent_source = '" +
    escapeSQLString(src) + "' AND ts > NOW() - INTERVAL '" + iv +
    "' GROUP BY session_id ORDER BY MAX(ts) DESC LIMIT 500"
  );
  var r = rowsToObjects(res);
  if (!r || r.length === 0) {
    prSourceCache = { range: cacheKey, source: src, ids: '' };
    return '';
  }
  var ids = r.map(function(row) { return "'" + escapeSQLString(row.session_id) + "'"; }).join(',');
  prSourceCache = { range: cacheKey, source: src, ids: ids };
  return ids;
}

// prCachedSourceIDs is populated in pr_loadCards() before source-filtered queries run.
var prCachedSourceIDs = null; // null = "All", '' = no match, 'id,...' = filter list

function pr_sourceSQL() {
  if (prCachedSourceIDs === null) return ''; // "All"
  if (prCachedSourceIDs === '') return ' AND 1=0'; // no matching sessions
  return ' AND session_id IN (' + prCachedSourceIDs + ')';
}

function pr_reload() {
  prDataCache = null;
  prPromptData = [];
  prPage = 0;
  prExpandedIdx = -1;
  prSourceCache = { range: null, source: null, ids: null }; // invalidate source cache on reload
  pr_loadCards().then(function(ok) {
    if (!ok) {
      // Clear KPI cards and content areas when no data matches the filter.
      ['pr-val-prompts', 'pr-val-turns', 'pr-val-cost'].forEach(function(id) {
        var el = document.getElementById(id);
        if (el) el.textContent = '\u2014';
      });
      var empty = '<div class="chart-empty">' + t('empty.no_data') + '</div>';
      ['pr-prompt-list', 'pr-pattern-content'].forEach(function(id) {
        var el = document.getElementById(id);
        if (el) el.innerHTML = empty;
      });
      var pg = document.getElementById('pr-pagination');
      if (pg) pg.innerHTML = '';
      var ai = document.getElementById('pr-ai-container');
      if (ai) ai.style.display = 'none';
      return;
    }
    pr_loadData().then(function() {
      pr_loadOverview();
      pr_loadPrompts();
      pr_loadPatterns();
    });
  });
}

// ============================================================
// Request patterns
// ============================================================

var PR_VERBS = ['fix', 'add', 'implement', 'refactor', 'debug', 'explain', 'update', 'remove', 'test', 'review', 'create', 'change', 'optimize', 'migrate', 'upgrade', 'configure', 'write', 'build', 'check', 'find', 'search', 'read', 'show'];

// ============================================================
// Data Loading
// ============================================================

var prTotalPrompts = 0; // full count from DB (may exceed analyzed)
var prDataCache = null; // { range: string, data: requests[] }
var prDataGeneration = 0; // incremented on each pr_loadData(), used as AI insights cache key

async function pr_loadCards() {
  prDataCache = null; // invalidate on every cards reload (refresh / time range change)
  prPromptData = [];  // force pr_loadData() re-fetch (don't let AI Insights use stale data)
  var iv = intervalSQL();
  try {
    prCachedSourceIDs = await pr_sourceSessionIDs();
    var res = await query(
      "SELECT COUNT(*) AS total_prompts, COUNT(DISTINCT session_id) AS sessions " +
      "FROM tma1_messages WHERE message_type = 'user' AND ts > NOW() - INTERVAL '" + iv + "'" + pr_sourceSQL()
    );
    var r = rows(res);
    if (!r || !r[0]) return false;
    prTotalPrompts = Number(r[0][0]) || 0;
    var totalSessions = Number(r[0][1]) || 0;
    if (prTotalPrompts === 0) return false;

    document.getElementById('pr-val-prompts').textContent = fmtNum(prTotalPrompts);
    document.getElementById('pr-val-cost').textContent = fmtNum(totalSessions);
    document.getElementById('pr-val-turns').textContent = totalSessions > 0 ? (prTotalPrompts / totalSessions).toFixed(1) : '\u2014';

    return true;
  } catch (e) {
    return false;
  }
}

async function pr_loadData() {
  // Return cached data if time range hasn't changed.
  if (prDataCache && prDataCache.range === currentTimeRange) {
    return prDataCache.data;
  }

  var iv = intervalSQL();

  // Q1: User prompts
  var promptRes = await query(
    "SELECT session_id, ts, content, model FROM tma1_messages " +
    "WHERE message_type = 'user' AND ts > NOW() - INTERVAL '" + iv + "'" + pr_sourceSQL() +
    " ORDER BY ts DESC LIMIT 500"
  );
  var prompts = rowsToObjects(promptRes);
  if (!prompts || prompts.length === 0) return [];

  var requests = prompts.map(function(p) {
    var earlier = prompts.filter(function(other) {
      return other.session_id === p.session_id && tsToMs(other.ts) < tsToMs(p.ts);
    }).slice(0, 4).reverse().map(function(other) { return other.content || ''; });
    return {sessionId:p.session_id, ts:p.ts, content:p.content || '', model:p.model || '',
      previousPrompts:earlier};
  });

  prDataCache = { range: currentTimeRange, data: requests };
  prDataGeneration++;
  return requests;
}

// ============================================================
// Overview Tab
// ============================================================

async function pr_loadOverview() {
  var requests = await pr_loadData();
  if (!requests || requests.length === 0) return;

  // Update KPI: show analyzed count (may differ from total if truncated)
  if (requests.length < prTotalPrompts) {
    document.getElementById('pr-val-prompts').innerHTML = fmtNum(requests.length) +
      ' <span style="font-size:12px;color:var(--text-dim);font-weight:400">/ ' + fmtNum(prTotalPrompts) + '</span>';
  }

  // AI Insights container (only when LLM configured)
  var aiContainer = document.getElementById('pr-ai-container');
  if (aiContainer) aiContainer.style.display = prLLMAvailable ? '' : 'none';
}

// ============================================================
// Prompts Tab
// ============================================================

async function pr_loadPrompts() {
  var requests = (await pr_loadData()).slice();
  if (!requests || requests.length === 0) {
    document.getElementById('pr-prompt-list').innerHTML = '<div class="chart-empty">' + t('empty.waiting') + '</div>';
    return;
  }

  requests.sort(function(a,b) { return tsToMs(b.ts)-tsToMs(a.ts); });

  prPromptData = requests;

  // Paginate
  var start = prPage * prPageSize;
  var page = requests.slice(start, start + prPageSize);
  prHasNext = requests.length > start + prPageSize;

  pr_renderPromptCards(page, start);
  pr_renderPagination(requests.length);
}

function pr_renderPromptCards(pageData, startIdx) {
  var container = document.getElementById('pr-prompt-list');
  if (pageData.length === 0) {
    container.innerHTML = '<div class="chart-empty">' + t('pr.no_prompts') + '</div>';
    return;
  }

  var html = '';
  pageData.forEach(function(p, i) {
    var idx = startIdx + i;
    var expanded = prExpandedIdx === idx;
    var relTime = pr_relativeTime(p.ts);
    html += '<div class="pr-card' + (expanded ? ' expanded' : '') + '" data-idx="' + idx + '" onclick="pr_toggleCard(' + idx + ')">';
    html += '<div class="pr-card-header">';
    html += '<div class="pr-card-meta">';
    html += '<span>' + escapeHTML(relTime) + '</span>';
    html += '<span class="pr-sep">\u00b7</span>';
    html += '<span title="' + escapeHTML(p.sessionId) + '">' + escapeHTML(p.sessionId.slice(0, 12)) + '</span>';
    html += '</div></div>';
    html += '<div class="pr-card-content">' + escapeHTML(p.content) + '</div>';

    if (expanded) {
      html += pr_renderDetail(p);
    }
    html += '</div>';
  });

  container.innerHTML = html;
}

function pr_renderDetail(p) {
  var html = '<div class="pr-detail"><p class="panel-caption">' + t('pr.review_note') + '</p>';

  // LLM evaluate button
  if (prLLMAvailable) {
    html += '<button class="pr-eval-btn" onclick="event.stopPropagation(); pr_evaluate(' + prPromptData.indexOf(p) + ')" id="pr-eval-btn-' + prPromptData.indexOf(p) + '">' + t('pr.deep_evaluate') + '</button>';
    html += '<div id="pr-llm-result-' + prPromptData.indexOf(p) + '"></div>';
  }

  html += '</div>';
  return html;
}

function pr_toggleCard(idx) {
  prExpandedIdx = prExpandedIdx === idx ? -1 : idx;
  var start = prPage * prPageSize;
  var page = prPromptData.slice(start, start + prPageSize);
  pr_renderPromptCards(page, start);
}

function pr_renderPagination(total) {
  var el = document.getElementById('pr-pagination');
  var totalPages = Math.ceil(total / prPageSize);
  if (totalPages <= 1) { el.innerHTML = ''; return; }

  el.innerHTML =
    '<button class="filter-btn" onclick="pr_prevPage()" ' + (prPage === 0 ? 'disabled' : '') + '>\u2190 ' + t('pr.prev') + '</button>' +
    ' <span style="color:var(--text-muted);font-size:13px">' + (prPage + 1) + ' / ' + totalPages + '</span> ' +
    '<button class="filter-btn" onclick="pr_nextPage()" ' + (!prHasNext ? 'disabled' : '') + '>' + t('pr.next') + ' \u2192</button>';
}

function pr_prevPage() { if (prPage > 0) { prPage--; pr_loadPrompts(); } }
function pr_nextPage() { if (prHasNext) { prPage++; pr_loadPrompts(); } }

// ============================================================
// Patterns Tab
// ============================================================

async function pr_loadPatterns() {
  var requests = await pr_loadData();
  var groups = {};
  requests.forEach(function(p) { var verb = pr_extractVerb(p.content); groups[verb] = (groups[verb] || 0) + 1; });
  var html = '<p class="panel-caption">' + t('pr.signal_note') + '</p><table><thead><tr><th>' + t('pr.pattern') + '</th><th>' + t('pr.count') + '</th></tr></thead><tbody>';
  Object.entries(groups).sort(function(a,b) { return b[1]-a[1]; }).forEach(function(row) {
    html += '<tr><td>' + escapeHTML(row[0]) + '</td><td>' + row[1] + '</td></tr>';
  });
  document.getElementById('pr-pattern-content').innerHTML = html + '</tbody></table>';
}

function pr_extractVerb(content) {
  var lower = (content || '').toLowerCase().trim();
  // Skip leading @, /, # etc.
  lower = lower.replace(/^[@/#!\-*]+\s*/, '');
  var words = lower.split(/\s+/);
  for (var i = 0; i < Math.min(words.length, 5); i++) {
    var w = words[i].replace(/[^a-z]/g, '');
    if (PR_VERBS.indexOf(w) !== -1) return w;
  }
  return 'other';
}

// ============================================================
// LLM Evaluation
// ============================================================

async function pr_checkLLM() {
  try {
    var resp = await fetch('/api/evaluate');
    var data = await resp.json();
    prLLMAvailable = data.available === true;
  } catch (e) {
    prLLMAvailable = false;
  }
}

async function pr_evaluationContext(p) {
  var where = " FROM tma1_messages WHERE session_id = '" + escapeSQLString(p.sessionId) + "' AND content IS NOT NULL AND content <> ''";
  var select = 'SELECT "role", message_type, SUBSTR(content, 1, 1501) AS content, ts';
  var results = await Promise.all([
    query(select + where + ' AND ts < ' + tsToMs(p.ts) + ' ORDER BY ts DESC LIMIT 21'),
    query(select + where + ' AND ts > ' + tsToMs(p.ts) + ' ORDER BY ts ASC LIMIT 13'),
  ]);
  var before = rowsToObjects(results[0]);
  var after = rowsToObjects(results[1]);
  var boundary = after.findIndex(function(m) { return m.message_type === 'user'; });
  var following = boundary < 0 ? after.slice(0,12) : after.slice(0,boundary);
  var messages = before.slice(0,20).reverse().concat(following);
  return {
    excerpts: messages.map(function(m, i) { return {role:m.role, type:m.message_type,
      position:i < Math.min(before.length,20) ? 'before' : 'after', content:m.content.slice(0,1500)}; }),
    limitations: 'Partial transcript: at most 20 preceding and 12 following messages, 1500 characters each. Following messages stop at the next user request. Equal-timestamp messages are excluded because their order is unknown. Repository state and verified final outcome are not supplied.',
    truncated: before.length > 20 || (boundary < 0 && after.length > 12) || messages.some(function(m) { return m.content.length > 1500; }),
  };
}

async function pr_evaluate(idx) {
  var p = prPromptData[idx];
  if (!p) return;

  var btn = document.getElementById('pr-eval-btn-' + idx);
  var resultEl = document.getElementById('pr-llm-result-' + idx);
  if (btn) { btn.disabled = true; btn.textContent = t('pr.evaluating'); }

  try {
    var context = await pr_evaluationContext(p);
    var resp = await fetch('/api/evaluate', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        prompt: p.content,
        context: context,
        lang: currentLocale || 'en',
      }),
    });
    var data = await resp.json();
    if (data.error) {
      resultEl.innerHTML = '<div class="pr-llm-result" style="border-color:var(--red)"><div class="pr-llm-header" style="color:var(--red)">' + t('pr.llm_error_title') + '</div><div style="font-size:13px;color:var(--text-muted)">' + escapeHTML(data.error) + '</div></div>';
      return;
    }
    pr_renderLLMResult(resultEl, data);
  } catch (e) {
    if (resultEl) resultEl.innerHTML = '<div class="pr-llm-result" style="border-color:var(--red)"><div style="font-size:13px;color:var(--red)">' + escapeHTML(e.message) + '</div></div>';
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = t('pr.deep_evaluate'); }
  }
}

function pr_renderLLMResult(el, data) {
  var html = '<div class="pr-llm-result">';
  html += '<div class="pr-llm-header">' + t('pr.llm_evaluation') + ' \u00b7 ' + escapeHTML(data.model || '') + '</div>';

  // V2: point-based with reasoning
  if (data.points) {
    if (data.reasoning) {
      html += '<div class="pr-ai-summary" style="margin-bottom:10px;font-style:italic">' + escapeHTML(data.reasoning) + '</div>';
    }
    Object.entries(data.points).forEach(function(entry) {
      var dim = entry[0], detail = entry[1];
      var color = 'var(--text-muted)';
      html += '<div class="pr-dim-card" style="margin-bottom:6px">' +
        '<div class="pr-dim-card-head">' +
          '<span class="pr-dim-card-label">' + escapeHTML(dim) + '</span>' +
          '<span class="pr-dim-card-val" style="color:' + color + '">' + (detail.score == null ? '—' : detail.score + '/5') + '</span>' +
        '</div>';
      (detail.evidence || []).forEach(function(quote) { html += '<blockquote>' + escapeHTML(quote) + '</blockquote>'; });
      if (detail.awarded && detail.awarded.length > 0) {
        html += '<div style="font-size:11px;color:var(--text-muted);margin-top:4px">';
        detail.awarded.forEach(function(a) {
          html += '<span style="display:inline-block;margin-right:8px">\u2713 ' + escapeHTML(a) + '</span>';
        });
        html += '</div>';
      }
      html += '</div>';
    });
  }
  // Suggestions
  if (data.suggestions && data.suggestions.length > 0) {
    html += '<div class="pr-suggestions" style="border-style:solid;border-color:var(--blue)">';
    html += '<div class="pr-suggestions-title" style="color:var(--blue)">' + t('pr.suggestions') + '</div>';
    data.suggestions.forEach(function(s) {
      html += '<div class="pr-suggestion-item"><strong>' + escapeHTML(t('pr.advice.' + s.target)) + '</strong><blockquote>' + s.evidence.map(escapeHTML).join('<br>') + '</blockquote><p>' + escapeHTML(s.impact) + '</p><p>' + escapeHTML(s.action) + '</p></div>';
    });
    html += '</div>';
  }

  html += '</div>';
  el.innerHTML = html;
}

// ============================================================
// AI Insights (Sampled Summary)
// ============================================================

var prAIInsightsCache = {};

function pr_samplePrompts(requests) {
  var ordered = requests.slice().sort(function(a,b) { return tsToMs(a.ts)-tsToMs(b.ts); });
  if (ordered.length <= 14) return ordered;
  return Array.from({length:14}, function(_,i) { return ordered[Math.floor(i*(ordered.length-1)/13)]; });
}

async function pr_loadAIInsights() {
  var key = prDataGeneration + ':' + (currentLocale || 'en');
  if (prAIInsightsCache[key]) {
    pr_renderAIInsights(prAIInsightsCache[key]);
    return;
  }

  var btn = document.getElementById('pr-ai-btn');
  var el = document.getElementById('pr-ai-insights');
  if (btn) { btn.disabled = true; btn.textContent = t('pr.ai_generating'); }
  el.innerHTML = '<div class="loading">' + t('pr.ai_generating') + '</div>';

  var dataset = await pr_loadData();
  var sample = pr_samplePrompts(dataset);
  if (sample.length === 0) {
    el.innerHTML = '<div class="chart-empty">' + t('pr.no_prompts') + '</div>';
    if (btn) { btn.disabled = false; btn.textContent = t('pr.generate_insights'); }
    return;
  }

  // Snapshot stats before async LLM call (globals may change while awaiting).
  var snapshotTotal = dataset.length;
  var snapshotRange = currentTimeRange;
  var avgScore = null;

  var payload = {
    prompts: sample.map(function(s) {
      return {
        content: s.content,
        previous_prompts: s.previousPrompts || [],
      };
    }),
    total_prompts: snapshotTotal,
    avg_score: avgScore,
    lang: currentLocale || 'en',
  };

  try {
    var resp = await fetch('/api/evaluate/summary', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    var data = await resp.json();
    if (data.error) {
      el.innerHTML = '<div class="pr-llm-result" style="border-color:var(--red)"><div style="font-size:13px;color:var(--red)">' + escapeHTML(data.error) + '</div></div>';
      return;
    }
    data._sampleSize = sample.length;
    data._totalSize = snapshotTotal;
    data._avgScore = avgScore;
    data._timeRange = snapshotRange;
    data._sample = sample; // keep for rendering example prompts
    prAIInsightsCache[key] = data;
    pr_renderAIInsights(data);
    pr_saveInsight(data); // persist to DB (best-effort)
  } catch (e) {
    el.innerHTML = '<div style="color:var(--red);font-size:13px">' + t('pr.ai_error') + '</div>';
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = t('pr.generate_insights'); }
  }
}

function pr_renderAIInsights(data) {
  var el = document.getElementById('pr-ai-insights');
  var sample = data._sample || [];
  var html = '';

  // Summary
  if (data.summary) {
    html += '<div class="pr-ai-summary">' + escapeHTML(data.summary) + '</div>';
  }

  // Patterns with expanded explanation + example prompts
  if (data.patterns && data.patterns.length > 0) {
    data.patterns.forEach(function(p) {
      var freqClass = p.frequency === 'high' ? 'high' : (p.frequency === 'medium' ? 'medium' : 'low');
      html += '<div class="pr-ai-pattern">';
      html += '<div class="pr-ai-pattern-issue">' + escapeHTML(p.issue) +
        ' <span class="pr-ai-pattern-freq ' + freqClass + '">' + escapeHTML(p.frequency) + '</span></div>';
      html += '<div style="font-size:13px;color:var(--text);margin:6px 0">' + escapeHTML(p.suggestion) + '</div>';

      // Expanded explanation
      if (p.explanation) {
        html += '<div style="font-size:12px;color:var(--text-muted);line-height:1.5;margin-bottom:6px">' + escapeHTML(p.explanation) + '</div>';
      }

      // Example prompts from sample
      if (p.examples && p.examples.length > 0 && sample.length > 0) {
        html += '<div class="pr-ai-examples">';
        p.examples.forEach(function(idx) {
          var s = sample[idx - 1]; // 1-based index
          if (s) {
            var preview = s.content.length > 120 ? s.content.slice(0, 120) + '...' : s.content;
            html += '<div class="pr-ai-example">' +
              escapeHTML(preview) + '</div>';
          }
        });
        html += '</div>';
      }

      html += '</div>';
    });
  }

  // Top tip
  if (data.top_tip) {
    html += '<div class="pr-ai-top-tip">';
    html += '<div style="font-size:10px;color:var(--green);text-transform:uppercase;letter-spacing:0.5px;margin-bottom:4px">' + t('pr.ai_top_tip') + '</div>';
    html += escapeHTML(data.top_tip);
    html += '</div>';
  }

  // Footnote
  html += '<div class="pr-ai-footnote">' +
    t('pr.ai_sampled').replace('{n}', data._sampleSize || '?').replace('{total}', data._totalSize || '?') +
    (data.model ? ' \u00b7 ' + escapeHTML(data.model) : '') +
    '</div>';

  el.innerHTML = html;
}

// ============================================================
// Insight Persistence & History
// ============================================================

var prHistoryOpen = false;
var prActiveInsightId = null;

async function pr_saveInsight(data) {
  try {
    var sampleForSave = (data._sample || []).map(function(s) {
      return { content: s.content || '' };
    });
    await fetch('/api/insights', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        summary: data.summary || '',
        patterns: data.patterns || [],
        top_tip: data.top_tip || '',
        model: data.model || '',
        sample_size: data._sampleSize || 0,
        total_prompts: data._totalSize || 0,
        avg_score: data._avgScore,
        rubric_version: "task-evidence-v4",
        sample_prompts: sampleForSave,
        time_range: data._timeRange || currentTimeRange,
      }),
    });
  } catch (e) { /* silent — save is best-effort */ }
}

async function pr_toggleHistory() {
  var panel = document.getElementById('pr-ai-history-panel');
  prHistoryOpen = !prHistoryOpen;
  if (!prHistoryOpen) { panel.style.display = 'none'; return; }
  panel.style.display = '';
  panel.innerHTML = '<div class="loading">' + t('pr.ai_history_loading') + '</div>';
  try {
    var resp = await fetch('/api/insights?limit=10');
    var gdbResp = await resp.json();
    var recs = (gdbResp.output && gdbResp.output[0] && gdbResp.output[0].records) || {};
    var colNames = (recs.schema && recs.schema.column_schemas || []).map(function(c) { return c.name; });
    var rowData = (recs.rows || []).map(function(row) {
      var obj = {};
      colNames.forEach(function(c, i) { obj[c] = row[i]; });
      return obj;
    });
    pr_renderHistoryPanel(rowData);
  } catch (e) {
    panel.innerHTML = '<div style="color:var(--red);font-size:12px">' + t('pr.ai_error') + '</div>';
  }
}

function pr_renderHistoryPanel(items) {
  var panel = document.getElementById('pr-ai-history-panel');
  if (!items || items.length === 0) {
    panel.innerHTML = '<div class="chart-empty" style="height:auto;padding:16px">' + t('pr.ai_no_history') + '</div>';
    return;
  }

  var html = '<div class="pr-ai-history-panel">';
  items.forEach(function(item) {
    var id = item.insight_id || '';
    var sampleSize = Number(item.sample_size) || 0;
    var total = Number(item.total_prompts) || 0;
    var range = item.time_range || '';
    var summary = (item.summary || '').slice(0, 80);
    var ts = item.ts ? new Date(tsToMs(item.ts)).toLocaleString() : '';
    var active = id === prActiveInsightId ? ' active' : '';

    html += '<div class="pr-ai-history-card' + active + '" data-insight-id="' + escapeHTML(id) + '" onclick="pr_loadHistoryDetail(\'' + escapeJSString(id) + '\')">';
    html += '<div class="pr-ai-history-date">' + escapeHTML(ts) + '</div>';
    html += '<div class="pr-ai-history-meta">';
    if (item.rubric_version !== 'task-evidence-v4') html += '<span>Legacy rubric</span>';
    html += '<span>\u00b7</span>';
    html += '<span>' + sampleSize + ' / ' + total + ' ' + t('pr.ai_prompts_label') + '</span>';
    if (range) html += '<span>\u00b7</span><span>' + escapeHTML(range) + '</span>';
    html += '</div>';
    if (summary) html += '<div class="pr-ai-history-summary">' + escapeHTML(summary) + (item.summary && item.summary.length > 80 ? '...' : '') + '</div>';
    html += '</div>';
  });
  html += '</div>';
  panel.innerHTML = html;
}

async function pr_loadHistoryDetail(insightId) {
  var el = document.getElementById('pr-ai-insights');
  el.innerHTML = '<div class="loading">' + t('pr.ai_history_loading') + '</div>';
  try {
    var resp = await fetch('/api/insights/' + encodeURIComponent(insightId));
    var gdbResp = await resp.json();
    var recs = (gdbResp.output && gdbResp.output[0] && gdbResp.output[0].records) || {};
    var colNames = (recs.schema && recs.schema.column_schemas || []).map(function(c) { return c.name; });
    var row = (recs.rows || [])[0];
    if (!row) { el.innerHTML = '<div class="chart-empty">' + t('pr.ai_error') + '</div>'; return; }

    var obj = {};
    colNames.forEach(function(c, i) { obj[c] = row[i]; });

    // Parse JSON string fields
    var data = {
      summary: obj.summary || '',
      patterns: JSON.parse(obj.patterns || '[]'),
      top_tip: obj.top_tip || '',
      model: obj.model || '',
      _sampleSize: Number(obj.sample_size) || 0,
      _totalSize: Number(obj.total_prompts) || 0,
      _sample: JSON.parse(obj.sample_prompts || '[]').map(function(s) {
        return { content: s.content || '' };
      }),
    };
    prActiveInsightId = insightId;
    pr_renderAIInsights(data);
    // Update history card highlights
    document.querySelectorAll('.pr-ai-history-card').forEach(function(card) {
      card.classList.toggle('active', card.dataset.insightId === insightId);
    });
  } catch (e) {
    el.innerHTML = '<div style="color:var(--red);font-size:12px">' + escapeHTML(e.message) + '</div>';
  }
}

// ============================================================
// Tab Navigation
// ============================================================

function pr_onTabChange(tab) {
  if (tab === 'pr-overview') pr_loadOverview();
  else if (tab === 'pr-prompts') pr_loadPrompts();
  else if (tab === 'pr-patterns') pr_loadPatterns();
}

// ============================================================
// Helpers
// ============================================================

function pr_relativeTime(ts) {
  var ms = tsToMs(ts);
  var diff = Date.now() - ms;
  if (diff < 60000) return t('pr.time.now');
  if (diff < 3600000) return Math.floor(diff / 60000) + 'm ' + t('pr.time.ago');
  if (diff < 86400000) return Math.floor(diff / 3600000) + 'h ' + t('pr.time.ago');
  return Math.floor(diff / 86400000) + 'd ' + t('pr.time.ago');
}
