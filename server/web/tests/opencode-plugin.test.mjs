import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
const source=readFileSync(new URL('../../../integrations/opencode/tma1.js',import.meta.url),'utf8');
const {TMA1Plugin}=await import('data:text/javascript;base64,'+Buffer.from(source).toString('base64'));
test('OpenCode idle plugin posts the official snapshot and ignores unrelated events',async()=>{
 const original=globalThis.fetch;const posts=[];
 const info={id:'ses_test'};const messages=[{info:{id:'msg_test'},parts:[]}];
 globalThis.fetch=async(url,request)=>{posts.push({url,body:JSON.parse(request.body)});return {ok:true};};
 try {
  const plugin=await TMA1Plugin({client:{session:{
   get:async(options)=>{assert.equal(options.path.id,'ses_test');return {data:info};},
   messages:async(options)=>{assert.equal(options.throwOnError,true);return {data:messages};},
  }}});
  await plugin.event({event:{type:'session.updated'}});assert.equal(posts.length,0);
  await plugin.event({event:{type:'session.idle',properties:{sessionID:'ses_test'}}});
  assert.deepEqual(posts,[{url:'http://127.0.0.1:14318/api/import/opencode',body:{info,messages}}]);
 } finally {globalThis.fetch=original;}
});
