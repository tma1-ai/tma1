import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';
import assert from 'node:assert/strict';
const ctx=vm.createContext({});
vm.runInContext(readFileSync(new URL('../js/overview.js',import.meta.url),'utf8'),ctx);
test('aggregate agent counts without concatenating SQL numeric strings',()=>{
 const actual=ctx.overviewTotals([{sessions:'2',tools:'20',failures:'3',active:'1'},{sessions:'5',tools:10,failures:null,active:'2'}]);
 assert.deepEqual(JSON.parse(JSON.stringify(actual)),{sessions:7,tools:30,failures:3,active:3});
});
test('empty activity does not fabricate observations',()=>{
 ctx.t=()=> 'empty';
 assert.equal(ctx.overviewActivity([]),'<div class="overview-empty">empty</div>');
});
