import { readFileSync } from "node:fs";
import vm from "node:vm";
import test from "node:test";
import assert from "node:assert/strict";
const pricing = [
  { p: "known", tier: "standard", i: 2, o: 8, cacheRead: 0.2, writeShort: 2.5, writeLong: 4, maxInput: 1000 },
  { p: "known", tier: "priority", i: 4, o: 16, cacheRead: 0.4, writeShort: null, writeLong: null, maxInput: 1000 },
];
const context = vm.createContext({ tsToMs: (v) => Date.parse(v), sess_parseAttrs: (v) => v });
vm.runInContext(readFileSync(new URL("../js/core.js", import.meta.url), "utf8"), context);
context.modelPricing = pricing;
vm.runInContext(
  readFileSync(new URL("../js/sessions-stats.js", import.meta.url), "utf8"),
  context,
);
const codexCall = (attrs) => context.sess_parseCodexOTel([
  { timestamp: "2026-10-28", log_attributes: { model: "known", input_token_count: 1000, output_token_count: 100, reasoning_token_count: 40, cached_token_count: 800, ...attrs } },
])[0];
test("Codex usage is priced per tier with cached input", () => {
  // 200 fresh * 2 + 800 cached * 0.2 + 100 output * 8, per million.
  assert.equal(codexCall({}).cost, 0.00136);
  assert.equal(codexCall({ service_tier: "priority" }).cost, 0.00272);
  assert.equal(codexCall({ service_tier: "flex" }).cost, null);
  // Codex does not report whether a write was 30-minute or 12-hour.
  assert.equal(codexCall({ cache_write_token_count: 10 }).cost, null);
  const stats = context.sess_computeStats([], [], [], [codexCall({})], []);
  assert.equal(stats.totalOutputTokens, 100);
  assert.equal(stats.totalReasoningTokens, 40);
  assert.equal(stats.totalInputTokens, 1000);
  assert.equal(stats.cacheHitRatio, 0.8);
});
test("stored usage stays unpriced without a tier, write split or short prompt", () => {
  const price = context.sess_lookupPrice("known", "standard");
  // Claude counts: input excludes cache. 300 writes, 100 of them 1-hour.
  const claude = context.sess_usage(100, 10, 200, 300, 100, false);
  assert.equal(context.sess_usageCost(price, claude), (100 * 2 + 10 * 8 + 200 * 0.2 + 200 * 2.5 + 100 * 4) / 1e6);
  assert.equal(context.sess_usageCost(price, context.sess_usage(100, 10, 200, 300, null, false)), null);
  assert.equal(context.sess_usageCost(context.sess_lookupPrice("known", null), claude), null);
  assert.equal(context.sess_usageCost(price, context.sess_usage(600, 10, 200, 300, 100, false)), null);
  assert.equal(context.sess_usageCost(context.sess_lookupPrice("known", "priority"), claude), null);
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
