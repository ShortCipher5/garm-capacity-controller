import test from 'node:test';
import assert from 'node:assert/strict';
import {validatePreparationView,nativePreparationRequest} from './native-preparation-client.js';
const token='a'.repeat(64), review='b'.repeat(64), id='c'.repeat(64);
const target={admin_user:'administrator',host_key_fingerprint:'SHA256:'+'A'.repeat(43),admin_key_fingerprint:'SHA256:'+'B'.repeat(43),manifest_digest:token,script_sha256:token,target_id:'server',address:'192.0.2.10',port:22,preparation_id:id,review_digest:review,bundle_bytes:100,state:{review_digest:review,status:'not_started'},activation_authorized:false};
const reply=()=>({status:202,json:async()=>({target_id:'server',state:{review_digest:review,status:'unknown'}})});
test('preparation views reject activation and mismatched durable lineage',()=>{
 assert.equal(validatePreparationView({targets:[target]}).targets[0],target);
 for(const delta of [{activation_authorized:true},{bundle_bytes:0},{state:{review_digest:token,status:'unknown'}},{state:{review_digest:review,status:'ready'}}])assert.throws(()=>validatePreparationView({targets:[{...target,...delta}]}));
 assert.throws(()=>validatePreparationView({targets:[target,target]}));
 const prepared={...target,state:{review_digest:review,status:'prepared_not_activated',receipt:{version:'native-owner-preparation/v1',id,state:'prepared_not_activated',manifest_digest:token,unit_digest:token,activation_authorized:false}}};
 assert.equal(validatePreparationView({targets:[prepared]}).targets[0],prepared);
 assert.throws(()=>validatePreparationView({targets:[{...prepared,state:{...prepared.state,receipt:{...prepared.state.receipt,id:token}}}]}));
});
test('preparation posts only fixed reviewed identifiers once',async()=>{
 let calls=0;await nativePreparationRequest(token,{...target,endpoint:'not sent',command:'not sent'},false,{fetcher:async(url,o)=>{calls++;assert.equal(url,'/admin/api/v1/native-preparation');assert.equal(o.credentials,'omit');assert.equal(o.redirect,'error');assert.deepEqual(JSON.parse(o.body),{target_id:'server',review_digest:review,confirm_prepare:true,inspect_only:false});return reply();}});assert.equal(calls,1);
 await assert.rejects(nativePreparationRequest(token,target,false,{fetcher:async()=>{calls++;throw Error('lost');}}),/never upload again/);assert.equal(calls,2);
});
test('unknown or prepared outcomes only permit explicit inspection',async()=>{
 const unknown={...target,state:{review_digest:review,status:'unknown'}};let calls=0;
 await assert.rejects(nativePreparationRequest(token,unknown,false,{fetcher:async()=>{calls++;return reply();}}),/inspection only/);assert.equal(calls,0);
 await nativePreparationRequest(token,unknown,true,{fetcher:async(_,o)=>{calls++;assert.equal(JSON.parse(o.body).inspect_only,true);return reply();}});assert.equal(calls,1);
 await assert.rejects(nativePreparationRequest(token,target,true),/Reload and review/);
});
test('preparation rejects unexpected status, response correlation and receipts on unknown',async()=>{
 for(const response of [{status:200,json:reply().json},{status:202,json:async()=>({target_id:'other',state:{review_digest:review,status:'unknown'}})},{status:202,json:async()=>({target_id:'server',state:{review_digest:token,status:'unknown'}})},{status:202,json:async()=>({target_id:'server',state:{review_digest:review,status:'unknown',receipt:{}}})}])await assert.rejects(nativePreparationRequest(token,target,false,{fetcher:async()=>response}));
});

test('latest attempt is separate from historical receipt and strict about outcome shape',()=>{
 const receipt={version:'native-owner-preparation/v1',id,state:'prepared_not_activated',manifest_digest:token,unit_digest:token,activation_authorized:false};
 const attempt={sequence:2,mode:'inspect',review_digest:review,status:'observation_unavailable',started_at:'2026-11-18T15:00:00Z',completed_at:'2026-11-18T15:01:00Z'};
 const current={...target,state:{review_digest:review,status:'prepared_not_activated',receipt,attempt}};
 assert.equal(validatePreparationView({targets:[current]}).targets[0],current);
 for(const bad of [{...attempt,sequence:1025},{...attempt,review_digest:token},{...attempt,completed_at:'2026-11-18T14:00:00Z'},{...attempt,status:'observation_unavailable',receipt},{...attempt,status:'unknown'},{...attempt,status:'prepared_not_activated'}])assert.throws(()=>validatePreparationView({targets:[{...current,state:{...current.state,attempt:bad}}]}));
 assert.throws(()=>validatePreparationView({targets:[{...target,state:{...target.state,attempt:{...attempt,status:'unknown',completed_at:undefined}}}]}));
});
test('accepted attempts must advance exact review and requested mode; full history cannot start',async()=>{
 const attempt={sequence:1,mode:'prepare',review_digest:review,status:'unknown',started_at:'2026-11-18T15:00:00Z'};
 const unknown={...target,state:{review_digest:review,status:'unknown',attempt}};
 const accepted={...attempt,sequence:2,mode:'inspect'};
 const fetcher=a=>async()=>({status:202,json:async()=>({target_id:'server',state:{review_digest:review,status:'unknown',...(a?{attempt:a}:{})}})});
 assert.equal((await nativePreparationRequest(token,unknown,true,{fetcher:fetcher(accepted)})).state.attempt.sequence,2);
 for(const a of [undefined,{...accepted,sequence:1},{...accepted,mode:'prepare'},{...accepted,sequence:3}])await assert.rejects(nativePreparationRequest(token,unknown,true,{fetcher:fetcher(a)}),/accepted attempt/);
 await assert.rejects(nativePreparationRequest(token,{...unknown,state:{...unknown.state,attempt:{...attempt,sequence:1024}}},true,{fetcher:fetcher(accepted)}),/Reload and review/);
});
