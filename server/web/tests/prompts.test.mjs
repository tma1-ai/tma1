import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';
import assert from 'node:assert/strict';
const elements = {};
const ctx=vm.createContext({t:String,tsToMs:Number,escapeSQLString:s=>s.replaceAll("'","''"),
 escapeHTML:s=>String(s).replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;'),
 document:{getElementById:id=>elements[id] ||= {}}, rowsToObjects:x=>x});
vm.runInContext(readFileSync(new URL('../js/prompts.js',import.meta.url),'utf8'),ctx);

test('context includes assistant replies and stops before the next request',async()=>{
 const sql=[];
 ctx.query=async q=>{sql.push(q);return q.includes('ORDER BY ts DESC') ? [
  {role:'assistant',message_type:'assistant',content:'Option 2 preserves the API.',ts:9},
  {role:'user',message_type:'user',content:'Compare the alternatives.',ts:8},
 ] : [
  {role:'assistant',message_type:'assistant',content:'Implemented option 2.',ts:11},
  {role:'user',message_type:'user',content:'Now delete the project.',ts:12},
  {role:'assistant',message_type:'assistant',content:'Unrelated work',ts:13},
 ];};
 const context=await ctx.pr_evaluationContext({sessionId:"s'1",ts:10});
 assert.deepEqual(Array.from(context.excerpts,m=>m.content),['Compare the alternatives.','Option 2 preserves the API.','Implemented option 2.']);
 assert.deepEqual(Array.from(context.excerpts,m=>m.position),['before','before','after']);
 assert.ok(sql.every(q=>q.includes("session_id = 's''1'")));
 assert.equal(context.truncated,false);
});
test('bounded excerpts disclose truncation and query errors do not become missing context',async()=>{
 ctx.query=async q=>q.includes('DESC')?Array.from({length:21},()=>({role:'assistant',message_type:'assistant',content:'x'.repeat(1501)})):[];
 const context=await ctx.pr_evaluationContext({sessionId:'s',ts:10});
 assert.equal(context.excerpts.length,20);
 assert.equal(context.excerpts[0].content.length,1500);
 assert.equal(context.truncated,true);
 ctx.query=async()=>{throw new Error('database unavailable');};
 await assert.rejects(ctx.pr_evaluationContext({sessionId:'s',ts:10}),/database unavailable/);
});
test('short followups and keyword stuffing receive no local quality badge',()=>{
 ctx.pr_renderPromptCards([
  {sessionId:'s',ts:1,content:'继续'},
  {sessionId:'s',ts:2,content:'fix error should must verify'},
 ],0);
 assert.ok(elements['pr-prompt-list'].innerHTML.includes('继续'));
 assert.doesNotMatch(elements['pr-prompt-list'].innerHTML,/pr-score|excellent|poor/);
});
test('structured advice renders evidence and action as text',()=>{
 const el={};
 ctx.pr_renderLLMResult(el,{suggestions:[{target:'execution',evidence:['<script>'],impact:'Verification was skipped.',action:'Run the requested test.'}]});
 assert.match(el.innerHTML,/&lt;script&gt;/);
 assert.match(el.innerHTML,/Run the requested test/);
 assert.doesNotMatch(el.innerHTML,/<script>/);
});
test('time-spaced sample includes both ends without score bias',()=>{
 const data=Array.from({length:100},(_,i)=>({ts:i}));
 const sample=ctx.pr_samplePrompts(data);
 assert.equal(sample.length,14);assert.equal(sample[0].ts,0);assert.equal(sample[13].ts,99);
});
