import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';
import assert from 'node:assert/strict';
const ctx=vm.createContext({t:String,tsToMs:Number});
vm.runInContext(readFileSync(new URL('../js/prompts.js',import.meta.url),'utf8'),ctx);
test('session cost, exploration and failures do not change text signals',()=>{
 const prompt='修复登录失败，保持已有接口，验证成功后返回首页。';
 const a=ctx.pr_scorePrompt(prompt);
 const b=ctx.pr_scorePrompt(prompt,{turns:100,totalInput:9999999},{fail:100},Array(20).fill({tool:'Read',ok:false}));
 assert.equal(a.composite,b.composite);
 assert.ok(a.composite>0);
 assert.ok(!ctx.pr_getSuggestions(prompt,b).some(s=>/no_code|no_paths|exploration|high_cost/.test(s)));
});
test('follow-ups abstain rather than becoming low quality prompts',()=>{
 for(const text of ['继续','好的','continue','go ahead','LGTM'])assert.equal(ctx.pr_scorePrompt(text).composite,null);
 assert.equal(ctx.pr_mean([null,40,80]),60);
 assert.equal(ctx.pr_mean([null]),null);
});
test('ordinary English words are not rewarded as code identifiers',()=>{
 assert.equal(ctx.pr_scorePrompt('elephant orange beautiful winter').composite,0);
});
test('time-spaced sample includes both ends without score bias',()=>{
 const data=Array.from({length:100},(_,i)=>({ts:i,dims:{composite:i}}));
 const sample=ctx.pr_samplePrompts(data);
 assert.equal(sample.length,14);assert.equal(sample[0].ts,0);assert.equal(sample[13].ts,99);
});
