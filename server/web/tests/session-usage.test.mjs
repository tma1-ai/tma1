import { readFileSync } from "node:fs";
import vm from "node:vm";
import test from "node:test";
import assert from "node:assert/strict";
const context = vm.createContext({
  modelPricing: [{ p: "known", i: 2, o: 8 }],
  tsToMs: (v) => Date.parse(v),
  sess_parseAttrs: (v) => v,
});
vm.runInContext(readFileSync(new URL("../js/core.js", import.meta.url), "utf8"), context);
context.modelPricing = [{ p: "known", i: 2, o: 8 }];
vm.runInContext(
  readFileSync(new URL("../js/sessions-stats.js", import.meta.url), "utf8"),
  context,
);
test("Codex reasoning is not billed twice and cached prices stay unknown", () => {
  const calls = context.sess_parseCodexOTel([
    {
      timestamp: "2026-10-28",
      log_attributes: {
        model: "known",
        input_token_count: 1000,
        output_token_count: 100,
        reasoning_token_count: 40,
        cached_token_count: 0,
      },
    },
  ]);
  assert.equal(calls[0].cost, 0.0028);
  const stats = context.sess_computeStats([], [], [], calls, []);
  assert.equal(stats.totalOutputTokens, 100);
  assert.equal(stats.totalReasoningTokens, 40);
  calls[0].cost = null;
  calls[0].cacheTokens = 800;
  const cached = context.sess_computeStats([], [], [], calls, []);
  assert.equal(cached.cacheHitRatio, 0.8);
  assert.equal(cached.totalInputTokens, 1000);
  assert.equal(cached.cost, null);
  assert.equal(
    context.sess_usageCost(
      context.sess_lookupPrice("unlisted"),
      100,
      100,
      0,
      0,
    ),
    null,
  );
  assert.equal(
    context.sess_usageCost(context.sess_lookupPrice("known"), 100, 100, 50, 0),
    null,
  );
});
test("Claude cache writes contribute to input, recorded zero cost is valid", () => {
  const calls = [
    {
      inputTokens: 100,
      outputTokens: 20,
      cacheTokens: 800,
      cacheCreationTokens: 100,
      cost: 0,
      reportedCost: true,
    },
  ];
  const stats = context.sess_computeStats([], [], [], calls, []);
  assert.equal(stats.totalInputTokens, 1000);
  assert.equal(stats.cacheHitRatio, 0.8);
  assert.equal(stats.cost, 0);
  assert.equal(stats.costSource, "otel");
});

test("transcript usage records are not labeled as API requests", () => {
  const ctx = vm.createContext({t: String, escapeHTML: String, escapeJSString: String, fmtTokens: String, fmtCost: value => value == null ? '—' : '$' + value});
  vm.runInContext(readFileSync(new URL('../js/sessions-insights.js', import.meta.url), 'utf8'), ctx);
  const html = ctx.sess_renderAPICalls({apiCalls: [{usageRecord: true, inputTokens: 100, outputTokens: 20, cacheTokens: 800, cacheCreationTokens: 100, cost: null}], cost: null, totalInputTokens: 1000, totalOutputTokens: 20, totalCacheTokens: 800});
  assert.ok(html.includes('sessions.usage_records'));
  assert.ok(!html.includes('sessions.api_calls'));
  assert.ok(html.includes('<td>1000</td>'));
  assert.ok(!html.includes('$0'));
});
test('transcript characters never become measured usage or billed cost',()=>{
 const stats=context.sess_computeStats([], [{model:'known',message_type:'assistant',content:'answer '.repeat(1000)}], [], [], []);
 assert.equal(stats.totalInputTokens,null);
 assert.equal(stats.totalOutputTokens,null);
 assert.equal(stats.cost,null);
 assert.equal(context.fmtTokens(null),'—');
});
