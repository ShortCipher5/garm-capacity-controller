import test from 'node:test';
import assert from 'node:assert/strict';
import {validateNativeAssemblyReport} from './native-assembly-client.js';
const hash='a'.repeat(64), selection='b'.repeat(64);
const target={target_id:'server',binding:{preparation_id:hash}};
const report={target_id:'server',preparation_id:hash,selection_digest:selection,manifest_digest:'c'.repeat(64),plan_digest:'d'.repeat(64),assembly_digest:'e'.repeat(64),state:'bundle_created_not_reloaded',activation_authorized:false};
test('actual assembly report remains local configuration bound to exact saved selection',()=>{
  assert.deepEqual(validateNativeAssemblyReport({...report,output_path:'/private/not-exposed'},target,selection),report);
  for(const patch of [{target_id:'other'},{preparation_id:selection},{selection_digest:hash},{manifest_digest:''},{plan_digest:'x'.repeat(64)},{assembly_digest:null},{state:'registry_reloaded_not_prepared'},{state:'prepared_not_activated'},{activation_authorized:true}]) {
    assert.throws(()=>validateNativeAssemblyReport({...report,...patch},target,selection),/does not match/);
  }
});

import {webcrypto,createHash} from 'node:crypto';
import {nativeAssemblyRequest} from './native-assembly-client.js';
if(!globalThis.crypto)globalThis.crypto=webcrypto;
const wire=Buffer.from('unit-test-public-key-wire'),hostFingerprint='SHA256:'+createHash('sha256').update(wire).digest('base64').replace(/=+$/,'');
const paired={target_id:'server',binding:{preparation_id:hash,native_target:{address:'192.0.2.1',port:22,admin_user:'administrator',host_key:'ssh-ed25519 '+wire.toString('base64'),admin_key_fingerprint:'SHA256:'+'A'.repeat(43)}}};
const saved={state:'saved_configuration_not_applied',selection_digest:selection,selection:{binding:paired.binding,physical_capacity_qualified:false,saved_at:new Date(Date.now()-1000).toISOString(),expires_at:new Date(Date.now()+600000).toISOString()}};
const oldReview='f'.repeat(64), current={target_id:'server',preparation_id:hash,address:'192.0.2.1',port:22,admin_user:'administrator',host_key_fingerprint:hostFingerprint,admin_key_fingerprint:paired.binding.native_target.admin_key_fingerprint,manifest_digest:'f'.repeat(64),script_sha256:hash,review_digest:oldReview,bundle_bytes:100,activation_authorized:false,state:{review_digest:oldReview,status:'not_started'}};
const newView={...current,manifest_digest:report.manifest_digest,review_digest:report.plan_digest,state:{status:'not_started',review_digest:report.plan_digest}};
const token='9'.repeat(64);
test('assembly GET binds actual configured registry to exact pinned saved target',async()=>{
 const value=await nativeAssemblyRequest(token,null,paired,saved,null,null,{fetcher:async(url,o)=>{assert.equal(url,'/admin/api/v1/native-assembly');assert.equal(o.method,'GET');return{status:200,json:async()=>({targets:[current]})};}});assert.deepEqual(value,current);
 for(const patch of [{address:'192.0.2.2'},{preparation_id:selection},{host_key_fingerprint:'SHA256:'+'B'.repeat(43)},{admin_key_fingerprint:'SHA256:'+'C'.repeat(43)}])await assert.rejects(nativeAssemblyRequest(token,null,paired,saved,null,null,{fetcher:async()=>({status:200,json:async()=>({targets:[{...current,...patch}]})})}),/does not match/);
});
test('local bundle build sends fixed approved IDs, never private configuration or paths',async()=>{
 let calls=0;const result=await nativeAssemblyRequest(token,'build',paired,saved,current,null,{fetcher:async(_,o)=>{calls++;assert.equal(o.credentials,'omit');assert.deepEqual(JSON.parse(o.body),{action:'build',target_id:'server',selection_digest:selection,confirm_build:true});return{status:200,json:async()=>({target_id:'server',assembly:{...report,output_path:'not exposed'}})};}});assert.deepEqual(result,report);assert.equal(calls,1);
 await assert.rejects(nativeAssemblyRequest(token,'build',paired,{...saved,selection:{...saved.selection,expires_at:new Date(Date.now()-1).toISOString()}},current,null,{fetcher:async()=>{calls++;}}),/fresh saved/);assert.equal(calls,1);
});
test('registry reload binds old review and exact assembled result without requesting SSH',async()=>{
 const result=await nativeAssemblyRequest(token,'reload',paired,saved,current,report,{fetcher:async(_,o)=>{assert.deepEqual(JSON.parse(o.body),{action:'reload',target_id:'server',selection_digest:selection,assembly_digest:report.assembly_digest,current_review_digest:oldReview,confirm_reload:true});return{status:200,json:async()=>({target_id:'server',assembly:report,preparation:newView,state:'registry_reloaded_not_prepared'})};}});assert.equal(result.preparation.state.status,'not_started');
 for(const response of [{target_id:'server',assembly:{...report,plan_digest:hash},preparation:{...newView,review_digest:hash,state:{status:'not_started',review_digest:hash}},state:'registry_reloaded_not_prepared'},{target_id:'server',assembly:report,preparation:current,state:'registry_reloaded_not_prepared'},{target_id:'server',assembly:{...report,assembly_digest:hash},preparation:newView,state:'registry_reloaded_not_prepared'},{target_id:'other',assembly:report,preparation:newView,state:'registry_reloaded_not_prepared'}])await assert.rejects(nativeAssemblyRequest(token,'reload',paired,saved,current,report,{fetcher:async()=>({status:200,json:async()=>response})}));
});
test('uncertain build or registry reload never retries and inspection is explicitly read-only',async()=>{
 for(const action of ['build','reload']){let count=0;await assert.rejects(nativeAssemblyRequest(token,action,paired,saved,current,report,{fetcher:async()=>{count++;throw Error('lost response');}}),/uncertain/);assert.equal(count,1);}
 await nativeAssemblyRequest(token,'inspect',paired,saved,null,null,{fetcher:async(_,o)=>{assert.deepEqual(JSON.parse(o.body),{action:'inspect',target_id:'server',selection_digest:selection});return{status:200,json:async()=>({target_id:'server',assembly:report})};}});
});
