/* Sessions — stats computation, file path extraction, pricing helpers. */
/* globals: extractAllFilePaths, sess_lookupPrice, fmtTokens, sess_parseAttrs, tsToMs */

// Safely parse a hook_events.metadata JSON blob. Returns {} on any failure.
function sess_parseMeta(s) {
  if (!s) return {};
  try { var o = JSON.parse(s); return (o && typeof o === 'object') ? o : {}; }
  catch (e) { return {}; }
}

// ── Compute Stats ─────────────────────────────────────────────────────

function sess_computeStats(hookEvents, messages, timeline, apiCalls, apiErrors) {
  var stats = {
    duration: 0, toolCount: 0, primaryModel: '', cost: 0,
    files: {},
    context: { system: 5000, user: 0, tools: 0, reasoning: 0, subagent: 0 },
    agents: [],
    // OTel enrichment.
    apiCalls: apiCalls || [],
    totalInputTokens: 0,
    totalOutputTokens: 0,
    totalCacheTokens: 0,
    cacheHitRatio: 0,
    apiErrors: apiErrors || [],
    errorCount: (apiErrors || []).length,
    hasOTel: false,
    costSource: 'estimate',
  };

  // Duration from timeline bounds.
  if (timeline.length > 0) {
    stats.duration = (timeline[timeline.length - 1].ts - timeline[0].ts) / 1000;
  }

  // Tool pairs → file attention + tool count.
  for (var i = 0; i < timeline.length; i++) {
    var item = timeline[i];
    if (item.source === 'tool_pair') {
      stats.toolCount++;
      var tc = item.data;
      var fps = extractAllFilePaths(tc.tool_name, tc.tool_input);
      for (var fi = 0; fi < fps.length; fi++) {
        var fp = fps[fi];
        if (!stats.files[fp]) stats.files[fp] = { reads: 0, writes: 0 };
        if (tc.tool_name === 'Write' || tc.tool_name === 'Edit' || tc.tool_name === 'apply_patch') stats.files[fp].writes++;
        else stats.files[fp].reads++;
      }
      var resultLen = (tc.tool_result || '').length;
      var mult = (tc.tool_name === 'Read' ? 1.0 : tc.tool_name === 'Grep' || tc.tool_name === 'Glob' ? 0.5 : 0.3);
      if (tc.tool_name === 'Agent' || tc.tool_name === 'Task') {
        stats.context.subagent += Math.round(resultLen / 4 * mult);
      } else {
        stats.context.tools += Math.round(resultLen / 4 * mult);
      }
    }
  }

  // Agent hierarchy + subagent Gantt spans.
  var agentToolCounts = {};
  var openById = {};          // agent_id → open span
  var agentSpans = [];        // paired Start/Stop spans for Gantt
  var taskDescriptionById = {};   // tool_use_id → { description, prompt } from Task tool_input
  var sessionStartTs = Infinity, sessionEndTs = 0;
  for (var h = 0; h < hookEvents.length; h++) {
    var hev = hookEvents[h];
    var hts = tsToMs(hev.ts);
    if (hts < sessionStartTs) sessionStartTs = hts;
    if (hts > sessionEndTs) sessionEndTs = hts;
    var aid = hev.agent_id || '';
    if (hev.event_type === 'PreToolUse' && hev.tool_name &&
        hev.tool_name.toLowerCase() === 'task' && hev.tool_use_id) {
      var ti = sess_parseMeta(hev.tool_input);
      taskDescriptionById[hev.tool_use_id] = {
        description: ti.description || '',
        prompt: ti.prompt || '',
        name: ti.name || '',
      };
    }
    if (hev.event_type === 'SubagentStart') {
      stats.agents.push(hev);
      // If a span is already open for this id, close it out at this ts (anomaly).
      if (openById[aid]) {
        var prev = openById[aid];
        prev.end_ts = hts;
        prev.incomplete = true;
        agentSpans.push(prev);
      }
      var meta = sess_parseMeta(hev.metadata);
      openById[aid] = {
        agent_id: aid,
        agent_type: hev.agent_type || 'subagent',
        start_ts: hts,
        end_ts: 0,
        description: meta.description || '',
        model: '',
        duration_ms: 0,
        total_tokens: 0,
        total_tool_calls: 0,
        incomplete: false,
      };
    } else if (hev.event_type === 'SubagentStop') {
      var sp = openById[aid];
      if (sp) {
        var sm = sess_parseMeta(hev.metadata);
        sp.end_ts = hts;
        sp.model = sm.model || '';
        sp.duration_ms = Number(sm.duration_ms) || (hts - sp.start_ts);
        sp.total_tokens = Number(sm.total_tokens) || 0;
        sp.total_tool_calls = Number(sm.total_tool_calls) || 0;
        agentSpans.push(sp);
        delete openById[aid];
      }
      // Orphan Stop (no matching Start) — ignore.
    }
    if (hev.event_type === 'PreToolUse') {
      agentToolCounts[aid] = (agentToolCounts[aid] || 0) + 1;
    }
  }
  // Flush still-open spans (missing SubagentStop) — cap at sessionEnd, mark incomplete.
  var openIds = Object.keys(openById);
  for (var oi = 0; oi < openIds.length; oi++) {
    var op = openById[openIds[oi]];
    op.end_ts = sessionEndTs || op.start_ts;
    op.duration_ms = op.end_ts - op.start_ts;
    op.incomplete = true;
    agentSpans.push(op);
  }
  agentSpans.sort(function(a, b) { return a.start_ts - b.start_ts; });
  // Attach Task tool_input description/prompt to each span (keyed by agent_id = tool_use_id).
  for (var ax = 0; ax < agentSpans.length; ax++) {
    var td = taskDescriptionById[agentSpans[ax].agent_id];
    if (td) {
      agentSpans[ax].task_description = td.description;
      agentSpans[ax].task_prompt = td.prompt;
      agentSpans[ax].task_name = td.name;
    }
  }
  stats.agentToolCounts = agentToolCounts;
  stats.agentSpans = agentSpans;
  stats.sessionStart = sessionStartTs === Infinity ? 0 : sessionStartTs;
  stats.sessionEnd = sessionEndTs || stats.sessionStart;

  // Messages → context breakdown + model + cost estimate.
  var estInputTokens = 0;
  var estOutputTokens = 0;
  for (var m = 0; m < messages.length; m++) {
    var msg = messages[m];
    var contentLen = (msg.content || '').length;
    var tokens = Math.round(contentLen / 4);
    if (!stats.primaryModel && msg.model) stats.primaryModel = msg.model;

    if (msg.message_type === 'user') {
      stats.context.user += tokens;
      estInputTokens += tokens;
    } else if (msg.message_type === 'tool_result') {
      estInputTokens += Math.round(tokens * 0.3);
    } else if (msg.message_type === 'tool_use') {
      estInputTokens += tokens;
    } else if (msg.message_type === 'assistant' || msg.message_type === 'thinking') {
      stats.context.reasoning += tokens;
      estOutputTokens += tokens;
    }
  }

  // OTel enrichment: prefer precise data over estimates.
  if (stats.apiCalls.length > 0) {
    stats.hasOTel = true;
    var totalIn = 0, totalOut = 0, totalCache = 0, totalCost = 0;
    for (var ac = 0; ac < stats.apiCalls.length; ac++) {
      var call = stats.apiCalls[ac];
      totalIn += call.inputTokens;
      totalOut += call.outputTokens;
      totalCache += call.cacheTokens;
      totalCost += call.cost;
      if (!stats.primaryModel && call.model) stats.primaryModel = call.model;
    }
    stats.totalInputTokens = totalIn;
    stats.totalOutputTokens = totalOut;
    stats.totalCacheTokens = totalCache;
    stats.cost = totalCost;
    stats.costSource = 'otel';
    if (totalIn + totalCache > 0) {
      stats.cacheHitRatio = totalCache / (totalIn + totalCache);
    }
  } else {
    // Fallback to estimates.
    stats.totalInputTokens = estInputTokens;
    stats.totalOutputTokens = estOutputTokens;
    var price = sess_lookupPrice(stats.primaryModel);
    stats.cost = estInputTokens * price.input / 1000000 + estOutputTokens * price.output / 1000000;
  }

  return stats;
}

// ── File path extraction ──────────────────────────────────────────────

function extractFilePath(toolName, inputStr) {
  if (!inputStr) return null;
  try {
    var obj = JSON.parse(inputStr);
    if (toolName === 'Read' || toolName === 'Write' || toolName === 'Edit') return obj.file_path || obj.path || null;
    if (toolName === 'Grep') return obj.path || null;
  } catch (e) { /* not JSON */ }
  if (toolName === 'apply_patch') {
    var m = inputStr.match(/\*\*\* (?:Update|Add|Delete) File: (.+)/);
    return m ? m[1].trim() : null;
  }
  return null;
}

function extractAllFilePaths(toolName, inputStr) {
  if (!inputStr) return [];
  if (toolName === 'apply_patch') {
    var paths = [];
    var re = /\*\*\* (?:Update|Add|Delete) File: (.+)/g;
    var match;
    while ((match = re.exec(inputStr)) !== null) paths.push(match[1].trim());
    return paths;
  }
  var single = extractFilePath(toolName, inputStr);
  return single ? [single] : [];
}

// ── Pricing / formatting helpers ──────────────────────────────────────

function sess_lookupPrice(model) {
  if (!model || !modelPricing || !modelPricing.length) return { input: 3, output: 15 };
  for (var i = 0; i < modelPricing.length; i++) {
    if (model.indexOf(modelPricing[i].p) !== -1) {
      return { input: modelPricing[i].i, output: modelPricing[i].o };
    }
  }
  return { input: 3, output: 15 };
}

function fmtDurSec(sec) {
  if (sec < 60) return Math.round(sec) + 's';
  if (sec < 3600) return Math.round(sec / 60) + 'm';
  return (sec / 3600).toFixed(1) + 'h';
}

function fmtTokens(n) {
  if (n < 1000) return n + '';
  if (n < 1000000) return (n / 1000).toFixed(1) + 'k';
  return (n / 1000000).toFixed(1) + 'M';
}

// ── OTel parsers ──────────────────────────────────────────────────────

// Fallback OTel parser for CC sessions without usage data in messages (old data).
function sess_parseCCOTel(rows, sessionId) {
  var calls = [];
  for (var i = 0; i < rows.length; i++) {
    var a = sess_parseAttrs(rows[i].log_attributes);
    if (!a) continue;
    var rowSessionId = sess_attr(a, 'session.id');
    if (sessionId && rowSessionId && rowSessionId !== sessionId) continue;
    var seq = a['event.sequence'];
    calls.push({
      ts: tsToMs(rows[i].timestamp),
      model: a.model || '',
      inputTokens: Number(a.input_tokens) || 0,
      outputTokens: Number(a.output_tokens) || 0,
      cacheTokens: Number(a.cache_read_tokens) || 0,
      cacheCreationTokens: Number(a.cache_creation_tokens) || 0,
      cost: parseFloat(a.cost_usd) || 0,
      durationMs: parseFloat(a.duration_ms) || 0,
      toolUseIds: [],
      eventSeq: seq != null ? seq : null,
    });
  }
  return calls;
}

function sess_parseCodexOTel(rows, conversationIds) {
  var allowedConversations = null;
  if (conversationIds && conversationIds.length) {
    allowedConversations = {};
    for (var ci = 0; ci < conversationIds.length; ci++) {
      allowedConversations[conversationIds[ci]] = true;
    }
  }

  var calls = [];
  for (var i = 0; i < rows.length; i++) {
    var a = sess_parseAttrs(rows[i].log_attributes);
    if (!a) continue;
    if (allowedConversations) {
      var conversationId = sess_attr(a, 'conversation.id');
      if (!conversationId || !allowedConversations[conversationId]) continue;
    }
    var inputTok = Number(a.input_token_count) || 0;
    var outputTok = Number(a.output_token_count) || 0;
    var model = a.model || '';
    var price = sess_lookupPrice(model);
    calls.push({
      ts: tsToMs(rows[i].timestamp),
      model: model,
      inputTokens: inputTok,
      outputTokens: outputTok,
      cacheTokens: Number(a.cached_token_count) || 0,
      cacheCreationTokens: 0,
      cost: inputTok * price.input / 1000000 + outputTok * price.output / 1000000,
      durationMs: parseFloat(a.duration_ms) || 0,
    });
  }
  return calls;
}

function sess_collectConversationIds(hookEvents) {
  var ids = [];
  var seen = {};
  for (var i = 0; i < hookEvents.length; i++) {
    var id = hookEvents[i].conversation_id;
    if (!id || seen[id]) continue;
    seen[id] = true;
    ids.push(id);
  }
  return ids;
}

function sess_attr(attrs, key) {
  if (!attrs) return null;
  if (Object.prototype.hasOwnProperty.call(attrs, key)) return attrs[key];
  var parts = key.split('.');
  var curr = attrs;
  for (var i = 0; i < parts.length; i++) {
    if (curr == null || typeof curr !== 'object' || !Object.prototype.hasOwnProperty.call(curr, parts[i])) {
      return null;
    }
    curr = curr[parts[i]];
  }
  return curr;
}

function sess_filterBySessionId(rows, sessionId) {
  if (!sessionId) return rows;
  return rows.filter(function(r) {
    var a = sess_parseAttrs(r.log_attributes);
    var rowSessionId = sess_attr(a, 'session.id');
    return !rowSessionId || rowSessionId === sessionId;
  });
}

// Parse log_attributes once per row (avoids redundant JSON.parse per field).
function sess_parseAttrs(la) {
  if (!la) return null;
  try { return typeof la === 'string' ? JSON.parse(la) : la; } catch (e) { return null; }
}
