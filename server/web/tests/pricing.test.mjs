import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';
import assert from 'node:assert/strict';
const c = vm.createContext({});
for (const file of ['core.js','sessions-stats.js']) vm.runInContext(readFileSync(new URL('../js/'+file,import.meta.url),'utf8'),c);
test('pricing matches model IDs and dated snapshots without family collisions',()=>{
 c.modelPricing=[{p:'gpt-4',i:30,o:60},{p:'gpt-4.1',i:2,o:8},{p:'claude-sonnet-5',i:2,o:10}];
 assert.equal(c.sess_lookupPrice('gpt-4.1').input,2);
 assert.equal(c.sess_lookupPrice('gpt-4.1-2025-04-14').input,2);
 for(const id of ['gpt-4.1-mini','gpt-4x1','gpt-4.1-custom','unknown','claude-sonnet-6','claude-sonnet-5-5']) assert.equal(c.sess_lookupPrice(id).input,null);
 assert.equal(c.sess_lookupPrice('claude-sonnet-5-20261001').output,10);
 c.modelPricing=[];
 assert.equal(c.costCaseSQL('m','i','o'),'CAST(NULL AS DOUBLE)');
});
test('database errors in HTTP 200 responses do not become empty observations',async()=>{
 c.fetch=async()=>({ok:true,json:async()=>({code:1004,error:'invalid SQL'})});
 await assert.rejects(c.query('SELECT 1'),/invalid SQL/);
 assert.equal(c.queryInFlight,0);
});
