import test from 'node:test';import assert from 'node:assert/strict';import {validateReuseStatus,reuseDispositionLabel,reuseReasonLabel} from './verified-reuse-status.js';import {ReadSession,SessionEnded} from './client.js';
const remaining={cpu_millis:1000,ram_mib:2048,disk_mib:4096,runners:1};
const row=(over={})=>({request_key:'k'.repeat(64),pool:'app',host:'a',state:'deleting',disposition:'pending_cleanup',remaining:{...remaining},reusable:{cpu:false,ram:false,runners:false,disk:false},reasons:['awaiting_qualified_terminal_evidence'],...over});
const view=(rows)=> ({observed_at:'2026-11-24T10:00:00Z',rows});
test('reuse status dispositions stay conservative and never claim deletion',()=>{
 const v=view([row()]);
 assert.equal(validateReuseStatus(v),v);
 assert.match(reuseDispositionLabel('pending_cleanup'),/Pending cleanup/);
 assert.match(reuseReasonLabel('awaiting_qualified_terminal_evidence'),/stays allocated/);
 assert.match(reuseReasonLabel('residual_volume_unverified'),/volume/);
 assert.match(reuseReasonLabel('unknown_reason'),/Keep the charge held/);
 const released=view([row({state:'deleting',disposition:'verified_reusable',remaining:{cpu_millis:0,ram_mib:0,disk_mib:0,runners:0},reusable:{cpu:true,ram:true,runners:true,disk:true},reasons:['released_verified_teardown']})]);
 assert.equal(validateReuseStatus(released),released);
 const partial=view([row({disposition:'partially_reusable',remaining:{cpu_millis:1000,ram_mib:2048,disk_mib:0,runners:1},reusable:{cpu:false,ram:false,runners:false,disk:true},reasons:['awaiting_completed_workload','awaiting_terminal_delete_observation','awaiting_post_delete_absence']})]);
 assert.equal(validateReuseStatus(partial),partial);
 const charged=view([row({state:'idle',disposition:'charged',reasons:[]})]);
 assert.equal(validateReuseStatus(charged),charged);
 const historic=view([row({disposition:'released_historic',remaining:{cpu_millis:0,ram_mib:0,disk_mib:0,runners:0},reasons:['historic_release_requires_review']})]);
 assert.equal(validateReuseStatus(historic),historic);
 assert.match(reuseDispositionLabel('released_historic'),/Historic release/);
 assert.match(reuseReasonLabel('historic_release_requires_review'),/never reversed/);
});
test('reuse status validator rejects unsupported shapes and false reuse claims',()=>{
 for(const invalid of [
  view([row({disposition:'verified_reusable'})]),
  view([row({disposition:'charged',reasons:['released_verified_teardown']})]),
  view([row({disposition:'pending_cleanup',reasons:[]})]),
  view([row({reasons:['awaiting_completed_workload','awaiting_completed_workload']})]),
  view([row({reasons:['not_a_reason']})]),
  view([row({disposition:'released_historic',reusable:{cpu:true,ram:false,runners:false,disk:false}})]),
  view([row({disposition:'released_historic',remaining:{cpu_millis:1,ram_mib:0,disk_mib:0,runners:0}})]),
  view([row({disposition:'released_historic',reasons:['released_verified_teardown']})]),
  view([row({reusable:{cpu:'yes',ram:false,runners:false,disk:false}})]),
  view([row({remaining:{cpu_millis:-1,ram_mib:0,disk_mib:0,runners:0}})]),
  view([row({request_key:''})]),
  {rows:[]},
 ]){
  assert.throws(()=>validateReuseStatus(invalid));
 }
});
const resource={cpu_millis:0,ram_mib:0,runners:0};const overview={mode:'experimental-ledger',management_enabled:false,policy_revision:'a'.repeat(64),experimental_provisioning_enabled:false,admission_ready:false,epoch:0,immutable_execution_plans:0,legacy_unplanned_reservations:0,legacy_source_preparations:0,observed_at:'2026-11-24T10:00:00Z',global:{budget:resource,allocated:resource},hosts:[],pools:[],states:{},events:[]};
test('reuse status read is fixed GET and disconnected response cannot release anything',async()=>{
 let finish;const calls=[];
 const session=new ReadSession(async(url,options)=>{calls.push([url,options]);if(url.endsWith('/overview'))return{ok:true,json:async()=>overview};return new Promise(r=>finish=r);});
 await session.connect('a'.repeat(64));
 const pending=session.reuseStatusSnapshot();
 session.disconnect();
 finish({ok:true,json:async()=>view([row()])});
 await assert.rejects(pending,SessionEnded);
 assert.equal(calls[1][0],'/admin/api/v1/reuse-status');
 assert.equal(calls[1][1].method,'GET');
 assert.equal(calls[1][1].signal.aborted,true);
});
