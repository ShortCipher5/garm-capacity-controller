import test from 'node:test';import assert from 'node:assert/strict';
import {validateAllocatorPlan,allocatorPlanStale} from './allocator-plan.js';
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
