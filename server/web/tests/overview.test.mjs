import { readFileSync } from "node:fs";
import vm from "node:vm";
import test from "node:test";
import assert from "node:assert/strict";
const ctx = vm.createContext({});
vm.runInContext(
  readFileSync(new URL("../js/overview.js", import.meta.url), "utf8"),
  ctx,
);
test("aggregate agent counts without concatenating SQL numeric strings", () => {
  const actual = ctx.overviewTotals([
    { sessions: "2", tools: "20", failures: "3", active: "1" },
    { sessions: "5", tools: 10, failures: null, active: "2" },
  ]);
  assert.deepEqual(JSON.parse(JSON.stringify(actual)), {
    sessions: 7,
    tools: 30,
    failures: 3,
    active: 3,
  });
});
test("empty activity does not fabricate observations", () => {
  ctx.t = () => "empty";
  assert.equal(
    ctx.overviewActivity([]),
    '<div class="overview-empty">empty</div>',
  );
});

test('activity keeps empty hourly buckets between observations', () => {
  ctx.currentTimeRange = '24h';
  ctx.tsToMs = Number;
  ctx.escapeHTML = String;
  const html = ctx.overviewActivity([{hour: 0, agent_source: 'codex', events: 2}, {hour: 7200000, agent_source: 'codex', events: 1}]);
  const positions = [...html.matchAll(/<rect x="([^"]+)"/g)].map(match => Number(match[1]));
  assert.equal(positions.length, 2);
  assert.ok(positions[1] > 600, 'the empty middle hour must occupy horizontal space');
});

test('dense activity keeps edge labels inside the chart', () => {
  ctx.currentTimeRange = '24h';
  ctx.tsToMs = Number;
  ctx.escapeHTML = String;
  const html = ctx.overviewActivity(Array.from({ length: 24 }, (_, i) => ({
    hour: i * 3600000, agent_source: 'codex', events: 1,
  })));
  const labels = [...html.matchAll(/<text x="([^"]+)" y="[^"]+" text-anchor="([^"]+)"/g)];
  assert.deepEqual(labels.map(label => label[2]), ['start', 'middle', 'end']);
  assert.equal(Number(labels[0][1]), 0);
  assert.equal(Number(labels[2][1]), 920);
});
