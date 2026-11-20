import test from 'node:test';import assert from 'node:assert/strict';
import {validateAllocatorPlan,allocatorPlanStale,allocatorComparisonState,allocatorComparisonMessage,stockMaximumPreviewMessage,stockMaximumPreviewView,AllocatorReadinessError,allocatorFailureMessage} from './allocator-plan.js';
const n=Date.parse('2026-11-18T10:00:00Z'),pool='11111111-1111-1111-1111-111111111111';
const view=()=>({source:'scoped-stock-and-coherent-ledger',source_identity:'a'.repeat(64),demand_digest:'b'.repeat(64),ledger_sequence:3,policy_revision:2,started_at:new Date(n-1000).toISOString(),finished_at:new Date(n-500).toISOString(),captured_at:new Date(n).toISOString(),expires_at:new Date(n+59000).toISOString(),atomic:false,remaining_jobs:1,pools:[{pool,repository:pool,supply:null,charged:{cpu_millis:1,ram_mib:1,disk_mib:1,runners:1},cap:2,warm_idle:0,additional:1,additional_warm:0,covered_by_idle:0,covered_by_grant:0,deferred_by_inflight:1}]});
test('plan preserves unknown supply and explicit non-atomic provenance',()=>{const v=validateAllocatorPlan(view(),n);assert.equal(v.pools[0].supply,null);assert.equal(allocatorPlanStale(v,n),false);assert.equal(allocatorPlanStale(v,n+60000),true);});
test('stale and invalid linked plan responses rejected',()=>{for(const change of [v=>v.atomic=true,v=>v.source_identity='',v=>v.expires_at=new Date(n).toISOString(),v=>v.pools.push(v.pools[0]),v=>v.pools[0].supply={pool_id:'wrong',idle:0,busy:0,creating:0,other:0},v=>v.pools[0].additional=-1,v=>v.pools[0].charged.ram_mib=-1]){const v=view();change(v);assert.throws(()=>validateAllocatorPlan(v,n));}});

import {ReadSession,SessionEnded} from "./client.js";
const overview={mode:'experimental-ledger',management_enabled:false,policy_revision:'a'.repeat(64),experimental_provisioning_enabled:false,admission_ready:false,epoch:0,immutable_execution_plans:0,legacy_unplanned_reservations:0,legacy_source_preparations:0,observed_at:'2026-11-18T00:00:00Z',global:{budget:{cpu_millis:0,ram_mib:0,runners:0},allocated:{cpu_millis:0,ram_mib:0,runners:0}},hosts:[],pools:[],states:{},events:[]};
test('plan read cannot restore a disconnected session and only uses fixed GET',async()=>{
 let finish;const calls=[];const session=new ReadSession(async(url,opt)=>{calls.push([url,opt]);if(url.endsWith('/overview'))return{ok:true,json:async()=>overview};return new Promise(resolve=>finish=resolve);});
 await session.connect('a'.repeat(64));const pending=session.allocatorPlanSnapshot();session.disconnect();finish({ok:true,json:async()=>view()});await assert.rejects(pending,SessionEnded);assert.equal(calls[1][0],'/admin/api/v1/allocator-plan');assert.equal(calls[1][1].method,'GET');assert.equal(calls[1][1].signal.aborted,true);
});
test('monitoring veto remains distinct from capacity and rejects malformed fields',()=>{
 const v=view();v.pressure=[{host:'host',state:'missing_or_stale_not_headroom',sampled_at:'0001-01-01T00:00:00Z'}];v.pools[0].suppressed_by_pressure=1;
 assert.equal(validateAllocatorPlan(v,n).pools[0].charged.runners,1);
 v.pressure[0].state='free_capacity';assert.throws(()=>validateAllocatorPlan(v,n));
 v.pressure[0].state='partial_sample_not_headroom';v.pools[0].suppressed_by_pressure=-1;assert.throws(()=>validateAllocatorPlan(v,n));
});

test('fixed readiness reasons never display raw response errors or identifiers',()=>{
 for(const reason of ['source_not_configured','preview_unavailable','observation_unavailable','ledger_unavailable','pool_mapping_missing','host_binding_unavailable','enrollment_or_evidence_unavailable','stock_inventory_accounting_unavailable','planning_unavailable','snapshot_changed_or_expired','awaiting_observation','preview_stopped']) {
  const message=allocatorFailureMessage(new AllocatorReadinessError({state:'unavailable',reason,error:'PRIVATE'}));
  assert.match(message,/Current observation and plan are unknown/);assert.match(message,/advisory readiness/);assert.doesNotMatch(message,/PRIVATE/);
 }
 for(const body of [null,{},[],{state:'ready',reason:'ledger_unavailable'},{state:'unavailable',reason:'PRIVATE'},{state:'unavailable',reason:['ledger_unavailable']},{state:'unavailable',reason:'constructor'}]) {
  assert.equal(allocatorFailureMessage(new AllocatorReadinessError(body)),allocatorFailureMessage(new Error('PRIVATE')));
 }
});
test('stock accounting refusal does not imply deletion or available capacity',()=>{
 const message=allocatorFailureMessage(new AllocatorReadinessError({state:'unavailable',reason:'stock_inventory_accounting_unavailable',pool_id:'PRIVATE'}));
 assert.match(message,/could not be matched to saved accounting/);
 assert.match(message,/does not establish runner ownership, whether a runner was deleted, or whether capacity is free/);
 assert.doesNotMatch(message,/PRIVATE/);
});
test('503 fixed readiness survives only within the current read session',async()=>{
 let finish;const session=new ReadSession(async url=>url.endsWith('/overview')?{ok:true,json:async()=>overview}:{ok:false,status:503,json:()=>new Promise(resolve=>finish=resolve)});
 await session.connect('a'.repeat(64));const pending=session.allocatorPlanSnapshot();await new Promise(resolve=>setImmediate(resolve));session.disconnect();finish({state:'unavailable',reason:'ledger_unavailable'});await assert.rejects(pending,SessionEnded);
});
test('503 readiness parses only fixed reasons and malformed payload stays generic',async()=>{
 for(const body of [{state:'unavailable',reason:'pool_mapping_missing'},null]) {
  const session=new ReadSession(async url=>url.endsWith('/overview')?{ok:true,json:async()=>overview}:{ok:false,status:503,json:async()=>{if(body===null)throw new Error('PRIVATE');return body;}});
  await session.connect('a'.repeat(64));await assert.rejects(session.allocatorPlanSnapshot(),error=>error instanceof AllocatorReadinessError && error.reason===(body?.reason??null));session.disconnect();
 }
});

test("unchanged policy does not hide newer charged-ledger state",()=>{
 const v=validateAllocatorPlan(view(),n), captured={policyRevision:v.policy_revision,ledgerSequence:v.ledger_sequence,now:n};
 assert.equal(allocatorComparisonState(v,captured),"linked_at_capture");
 assert.match(allocatorComparisonMessage("linked_at_capture"),/not evidence of current/);
 assert.equal(allocatorComparisonState(v,{...captured,ledgerSequence:captured.ledgerSequence+1}),"preview_superseded");
 assert.match(allocatorComparisonMessage("preview_superseded"),/historical/);
 assert.equal(allocatorComparisonState(v,{...captured,ledgerSequence:captured.ledgerSequence-1}),"saved_comparison_older");
 assert.match(allocatorComparisonMessage("saved_comparison_older"),/Reload the saved policy/);
});
test("expiry and failed or incompatible saved policy do not become comparable",()=>{
 const v=view(), captured={policyRevision:v.policy_revision,ledgerSequence:v.ledger_sequence,now:n};
 assert.equal(allocatorComparisonState(v,{...captured,now:Date.parse(v.expires_at),policyStale:true}),"preview_expired");
 assert.equal(allocatorComparisonState(v,{...captured,policyStale:true}),"saved_comparison_stale");
 assert.equal(allocatorComparisonState(v,{...captured,policyRevision:captured.policyRevision+1}),"policy_changed");
 for(const ledgerSequence of [undefined,null,NaN,-1,String(captured.ledgerSequence)])assert.equal(allocatorComparisonState(v,{...captured,ledgerSequence}),"saved_comparison_unavailable");
 assert.equal(allocatorComparisonState(v,{...captured,policyRevision:undefined}),"saved_comparison_unavailable");
 assert.equal(allocatorComparisonState(null,captured),"unavailable");
 assert.equal(allocatorComparisonMessage("__proto__"),allocatorComparisonMessage("unavailable"));
});

const withMaximumPreview=(state="proposed")=>{
 const plan=view();
 plan.stock_maximum_preview={state,expires_at:new Date(n+10000).toISOString(),
  proposals:state==="proposed"?[{pool_id:pool,repository_id:pool,observed:2,suggested:3}]:[]};
 return plan;
};
test("optional stock maximum display hides absent field and accepts the closed stock schema",()=>{
 assert.equal(stockMaximumPreviewView(view(),"linked_at_capture",n),null);
 const plan=withMaximumPreview();
 assert.deepEqual(stockMaximumPreviewView(plan,"linked_at_capture",n),{
  state:"proposed",expiresAt:n+10000,proposals:[{pool_id:pool,repository_id:pool,observed:2,suggested:3}]
 });
 const lower=withMaximumPreview();lower.stock_maximum_preview.proposals[0].suggested=1;
 assert.equal(stockMaximumPreviewView(lower,"linked_at_capture",n).state,"proposed");
 for(const state of ["unavailable","no_confirmed_proposals"]){
  assert.equal(stockMaximumPreviewView(withMaximumPreview(state),"linked_at_capture",n).state,state);
 }
});
test("stock maximum display never keeps proposal rows after either expiry or ledger drift",()=>{
 const plan=withMaximumPreview();
 assert.deepEqual(stockMaximumPreviewView(plan,"linked_at_capture",n+10000),{state:"expired",proposals:[]});
 assert.deepEqual(stockMaximumPreviewView(plan,"linked_at_capture",n-1001),{state:"expired",proposals:[]});
 for(const comparison of ["preview_expired","saved_comparison_stale","policy_changed","preview_superseded","saved_comparison_older","saved_comparison_unavailable"]){
  assert.deepEqual(stockMaximumPreviewView(plan,comparison,n),{state:"comparison_unavailable",proposals:[]});
 }
 assert.match(stockMaximumPreviewMessage("expired"),/no historical suggestions/);
});
test("malformed stock preview fails closed independently of ordinary advice",()=>{
 for(const mutate of [
  p=>{p.stock_maximum_preview=null;},
  p=>{p.stock_maximum_preview.state="ready";},
  p=>{p.stock_maximum_preview.token="PRIVATE";},
  p=>{p.stock_maximum_preview.proposals=[];},
  p=>{p.stock_maximum_preview.state="no_confirmed_proposals";},
  p=>{p.stock_maximum_preview.proposals[0].secret="PRIVATE";},
  p=>{p.stock_maximum_preview.proposals[0].repository_id="22222222-2222-4222-8222-222222222222";},
  p=>{p.stock_maximum_preview.proposals[0].pool_id="22222222-2222-4222-8222-222222222222";},
  p=>{p.stock_maximum_preview.proposals[0].suggested=4;},
  p=>{p.stock_maximum_preview.proposals[0].suggested="3";},
  p=>{p.stock_maximum_preview.proposals[0].observed=0;},
  p=>{p.stock_maximum_preview.expires_at=p.captured_at;},
  p=>{p.stock_maximum_preview.expires_at=new Date(n+60000).toISOString();},
  p=>{p.stock_maximum_preview.expires_at="prompt";},
  p=>{p.pools.push({...p.pools[0],pool:"22222222-2222-4222-8222-222222222222"});p.stock_maximum_preview.proposals.push(p.stock_maximum_preview.proposals[0]);},
 ]){
  const plan=withMaximumPreview();mutate(plan);
  assert.equal(validateAllocatorPlan(plan,n),plan);
  assert.deepEqual(stockMaximumPreviewView(plan,"linked_at_capture",n),{state:"unavailable",proposals:[]});
 }
});

test("optional observed minimum idle preserves zero and rejects malformed or contradictory values",()=>{
 for(const value of [0,1,2]){
  const plan=withMaximumPreview();plan.stock_maximum_preview.proposals[0].observed_min_idle=value;
  assert.equal(stockMaximumPreviewView(plan,"linked_at_capture",n).proposals[0].observed_min_idle,value);
  assert.equal(stockMaximumPreviewView(plan,"linked_at_capture",n+10000).proposals.length,0);
  assert.equal(stockMaximumPreviewView(plan,"preview_superseded",n).proposals.length,0);
 }
 for(const value of [null,undefined,"0",false,-1,0.5,3,1000001,[],{}]){
  const plan=withMaximumPreview();plan.stock_maximum_preview.proposals[0].observed_min_idle=value;
  assert.deepEqual(stockMaximumPreviewView(plan,"linked_at_capture",n),{state:"unavailable",proposals:[]});
 }
 const lower=withMaximumPreview();Object.assign(lower.stock_maximum_preview.proposals[0],{observed_min_idle:2,suggested:1});
 assert.equal(stockMaximumPreviewView(lower,"linked_at_capture",n).state,"unavailable");
});

test("optional host evidence is exact, advisory and hidden when stale",()=>{
 const plan=withMaximumPreview();plan.stock_maximum_preview.host_evidence="configured_only";
 assert.equal(stockMaximumPreviewView(plan,"linked_at_capture",n).hostEvidence,"configured_only");
 for(const state of ["unavailable","proposed"]){
  for(const value of ["verified",true,["configured_only"],null]){
   const invalid=withMaximumPreview(state);invalid.stock_maximum_preview.host_evidence=value;
   assert.equal(stockMaximumPreviewView(invalid,"linked_at_capture",n).state,"unavailable");
  }
 }
 assert.equal(stockMaximumPreviewView(plan,"linked_at_capture",n+10000).hostEvidence,undefined);
 assert.equal(stockMaximumPreviewView(plan,"preview_superseded",n).hostEvidence,undefined);
});
