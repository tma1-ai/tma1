import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';
import assert from 'node:assert/strict';
const ctx = vm.createContext({});
vm.runInContext(readFileSync(new URL('../js/session-compare.js', import.meta.url), 'utf8'), ctx);
test('Codex cache and reasoning are subsets, Claude cache is additive', () => {
 const rows=[{input_tokens:1000,output_tokens:100,cache_read_tokens:800,cache_creation_tokens:50,reasoning_tokens:40}];
 assert.equal(ctx.sessionUsage(rows,'codex').input,1000);
 assert.equal(ctx.sessionUsage(rows,'codex').output,100);
 assert.equal(ctx.sessionUsage(rows,'codex').cacheRatio,.8);
 assert.equal(ctx.sessionUsage(rows,'claude_code').input,1850);
});
test('absent usage differs from reported zero',()=>{
 assert.equal(ctx.sessionUsage([{input_tokens:null,output_tokens:null}],'codex').known,false);
 assert.equal(ctx.sessionUsage([{input_tokens:0,output_tokens:0}],'codex').known,true);
 assert.equal(ctx.sessionUsage([],'codex').cacheRatio,null);
});
