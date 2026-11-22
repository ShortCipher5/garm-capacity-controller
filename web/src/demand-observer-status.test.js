import test from 'node:test';import assert from 'node:assert/strict';
import {validateDemandObserverStatus,currentDemandCounts,demandDisplayState} from './demand-observer-status.js';
import {ReadSession,SessionEnded} from './client.js';
const at=Date.parse('2026-11-18T00:00:00Z');
const view=()=>({state:'observed',observed_at:new Date(at+1000).toISOString(),expires_at:new Date(at+120000).toISOString(),source:'scoped-stock-and-github-observation',allocation_authorized:false,event:{mode:'observation_only',status:'observed',started_at:new Date(at).toISOString(),finished_at:new Date(at+500).toISOString(),atomic:false,missed_webhook_coverage:false,next_poll_seconds:60,counts:{stock_queued:3,observed_demand:6,reader_only:2,retained_history:1,conflicted:1,missing_from_reader:2,pools:2,idle:1,creating:2,busy:3,unknown:1}}});
test('fresh counts preserve provenance and expire locally without a server refresh',()=>{const v=validateDemandObserverStatus(view());assert.equal(currentDemandCounts(v,at+1000).reader_only,2);assert.equal(demandDisplayState(v,at+120000),'stale');assert.equal(currentDemandCounts(v,at+120000),null);assert.equal(currentDemandCounts({...v,state:'observing'},at+1000),null);});
test('unavailable states cannot retain counts or claim allocation authority',()=>{for(const mutate of [v=>v.allocation_authorized=true,v=>v.event.atomic=true,v=>v.event.missed_webhook_coverage=true,v=>v.source='caller',v=>v.event.counts.reader_only=6,v=>v.event.counts.conflicted=7,v=>v.event.counts.busy=-1,v=>v.event.counts.unknown=Number.MAX_SAFE_INTEGER+1,v=>v.state='stale',v=>v.state='stopped',v=>v.state='unavailable',v=>v.expires_at=new Date(at+300001).toISOString()]){const v=view();mutate(v);assert.throws(()=>validateDemandObserverStatus(v));}for(const state of ['unconfigured','not_started','observing','stopped','unavailable'])assert.equal(validateDemandObserverStatus({state,observed_at:new Date(at).toISOString(),source:'scoped-stock-and-github-observation',allocation_authorized:false}).state,state);});
const overview={mode:'experimental-ledger',management_enabled:false,policy_revision:'a'.repeat(64),experimental_provisioning_enabled:false,admission_ready:false,epoch:0,immutable_execution_plans:0,legacy_unplanned_reservations:0,legacy_source_preparations:0,observed_at:new Date(at).toISOString(),global:{budget:{cpu_millis:0,ram_mib:0,runners:0},allocated:{cpu_millis:0,ram_mib:0,runners:0}},hosts:[],pools:[],states:{},events:[]};
test('fixed read route strips no authority and disconnect fences delayed JSON',async()=>{let done;const calls=[];const s=new ReadSession(async(url,opt)=>{calls.push([url,opt]);return url.endsWith('/overview')?{ok:true,json:async()=>overview}:{ok:true,json:()=>new Promise(r=>done=r)};});await s.connect('a'.repeat(64));const pending=s.demandObserverStatusSnapshot();await new Promise(r=>setImmediate(r));s.disconnect();done(view());await assert.rejects(pending,SessionEnded);assert.equal(calls[1][0],'/admin/api/v1/demand-observer-status');assert.equal(calls[1][1].method,'GET');assert.equal(calls[1][1].credentials,'omit');assert.equal(calls[1][1].signal.aborted,true);});
test('authorization loss and explicit abort cannot restore demand state',async()=>{const s=new ReadSession(async url=>url.endsWith('/overview')?{ok:true,json:async()=>overview}:{status:401,ok:false});await s.connect('a'.repeat(64));await assert.rejects(s.demandObserverStatusSnapshot(),/authorization ended/);await assert.rejects(s.demandObserverStatusSnapshot(),SessionEnded);const c=new AbortController();c.abort();const t=new ReadSession(async url=>({ok:true,json:async()=>url.endsWith('/overview')?overview:view()}));await t.connect('a'.repeat(64));await assert.rejects(t.demandObserverStatusSnapshot({signal:c.signal}),SessionEnded);});
test('a completed observation is current only from the read time through its exclusive deadline',()=>{
 const v=validateDemandObserverStatus(view());
 for(const early of [at-1,at+499,at+500,at+999,NaN]) {
  assert.equal(demandDisplayState(v,early),'unavailable');
  assert.equal(currentDemandCounts(v,early),null);
 }
 assert.equal(currentDemandCounts(v,at+1000)?.stock_queued,3);
 assert.equal(currentDemandCounts(v,at+120000),null);
});
test('an observed payload must describe evidence completed at read time and not yet expired',()=>{
 for(const change of [
  v=>v.observed_at=new Date(at+499).toISOString(),
  v=>v.observed_at=v.expires_at,
  v=>v.observed_at=new Date(at+120001).toISOString(),
 ]) {
  const v=view();change(v);assert.throws(()=>validateDemandObserverStatus(v));
 }
});
