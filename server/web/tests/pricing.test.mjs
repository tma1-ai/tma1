import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';
import assert from 'node:assert/strict';
const c = vm.createContext({});
for (const file of ['core.js','sessions-stats.js']) vm.runInContext(readFileSync(new URL('../js/'+file,import.meta.url),'utf8'),c);
test('pricing matches model IDs and dated snapshots without family collisions',()=>{
 const row=(p,i,o)=>({p,tier:'standard',i,o});
 c.modelPricing=[row('gpt-4',30,60),row('gpt-4.1',2,8),row('claude-sonnet-5',2,10)];
 assert.equal(c.sess_lookupPrice('gpt-4.1','standard').i,2);
 assert.equal(c.sess_lookupPrice('gpt-4.1-2025-04-14','standard').i,2);
 for(const id of ['gpt-4.1-mini','gpt-4x1','gpt-4.1-custom','unknown','claude-sonnet-6','claude-sonnet-5-5']) assert.equal(c.sess_lookupPrice(id,'standard'),null);
 assert.equal(c.sess_lookupPrice('claude-sonnet-5-20261001','standard').o,10);
 c.modelPricing=[];
 assert.equal(c.costCaseSQL('m','i','o'),'CAST(NULL AS DOUBLE)');
});
test('service tiers map onto catalog tiers or stay unknown',()=>{
 for(const t of ['default','standard','STANDARD']) assert.equal(c.normalizeServiceTier(t),'standard');
 for(const t of ['priority','fast']) assert.equal(c.normalizeServiceTier(t),'priority');
 for(const t of [null,undefined,'','auto','flex','scale','constructor']) assert.equal(c.normalizeServiceTier(t),null);
});
test('database errors in HTTP 200 responses do not become empty observations',async()=>{
 c.fetch=async()=>({ok:true,json:async()=>({code:1004,error:'invalid SQL'})});
 await assert.rejects(c.query('SELECT 1'),/invalid SQL/);
 assert.equal(c.queryInFlight,0);
});
