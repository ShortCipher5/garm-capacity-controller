import test from 'node:test';
import assert from 'node:assert/strict';
import { stockApplyView, stockApplyMessages, stockApplyDiagnosticsUnavailable } from './stock-apply-view.js';

const scope='ab'.repeat(32);
const operation={operation_id:'01'.repeat(16),scope,pool_id:'pool',repository_id:'repo',source:'checked-source',fingerprint:'checked-settings',observed:3,suggested:4,charged:2,direction:'increase',state:'proposed',drift:false,plan_expires_at:'2026-11-24T10:00:20Z',created_at:'2026-11-24T10:00:00Z',updated_at:'2026-11-24T10:00:00Z'};
const action=(action,enabled,reason)=>({action,enabled,...(reason?{reason}:{})});
const actions=[action('apply',false,'no_supported_adapter_implemented'),action('withdraw_proposal',true),action('pause',true),action('resume',false,'not_paused'),action('resolve_outcome',false,'receipt_resolution_unavailable'),action('rollback',false,'rollback_requires_original_outcome_and_current_state')];
const resume=(lift,liftReason,ready,reason)=>({lift_pause:lift,...(liftReason?{lift_reason:liftReason}:{}),ready,...(reason?{reason}:{})});
const stockResume=resume(false,'not_paused',false,'stock_garm_v0_2_1_observe_only');
const stockView={scope,state:'proposed',reason:'',paused:false,drift:false,operation,actions,
 charged:{runners:2,reason:'a proposal changes nothing upstream; the configured maximum and all existing runner charges remain fully counted'},
 capability:{supported:false,refusals:[{code:'stock_garm_v0_2_1_observe_only',detail:'stock_garm_v0_2_1'}]},resume:stockResume};

test('valid controller payload round-trips with its state and charges',()=>{
 const view=stockApplyView(stockView);
 assert.equal(view.state,'proposed');
 assert.equal(view.operation.suggested,4);
 assert.equal(view.charged.runners,2);
 assert.equal(view.capability.refusals[0].code,'stock_garm_v0_2_1_observe_only');
});

test('malformed and untrusted payloads collapse to a conservative unavailable view',()=>{
 for(const raw of [null,[],{},'{bad}',{...stockView,state:'ready'},{...stockView,scope:'zz'.repeat(32)},
  {...stockView,extra:true},Object.fromEntries(Object.entries(stockView).filter(([k])=>k!=='charged')),
  {...stockView,charged:{runners:100001,reason:'x'.repeat(40)}},
  {...stockView,capability:{supported:true,refusals:[{code:'stock_garm_v0_2_1_observe_only'}]}},
  {...stockView,capability:{supported:false,refusals:null}},
  {...stockView,actions:actions.slice(1)},
  {...stockView,actions:actions.map(a=>a.action==='apply'?{...a,enabled:true}:a)},
  {...stockView,actions:actions.map(a=>a.action==='rollback'?{...a,enabled:true}:a)},
  {...stockView,drift:true,operation:undefined},
  {...stockView,state:'outcome_unknown',operation:undefined},
  {...stockView,operation:{...operation,operation_id:'nope'}},
  {...stockView,operation:{...operation,state:'applied'}},
  {...stockView,operation:{...operation,scope:'cd'.repeat(32)}},
  {...stockView,operation:{...operation,charged:1}},
  {...stockView,operation:{...operation,state:'applied_when_proven'}},
  {...stockView,readback:{maximum:1,fresh:'yes'}},
  {...stockView,readback:{maximum:-1,fresh:true}},
  {...stockView,readback:{maximum:3,fresh:true,settled:true}},
  {...stockView,scope:'0'.repeat(64)},
  {...stockView,reason:'Upper Case'},
  {...stockView,resume:undefined},
  {...stockView,resume:{lift_pause:'yes',ready:false}},
  {...stockView,resume:{lift_pause:false,ready:true}},
  {...stockView,resume:{lift_pause:true,ready:false}},
  {...stockView,resume:{lift_pause:false,lift_reason:'bad reason',ready:false}}]){
  const view=stockApplyView(raw);
  assert.equal(view.state,'unavailable',JSON.stringify(raw).slice(0,120));
  assert.equal(view.reason,'view_unavailable');
  assert.equal(view.operation,null);
 }
});

test('unknown state message warns that a matching read is not completion proof',()=>{
 const view=stockApplyView({...stockView,state:'outcome_unknown',reason:'outcome_uncertain_pending_resolution',operation:{...operation,state:'outcome_unknown'}});
 const messages=stockApplyMessages(view);
 assert.match(messages.state,/matching the old or new value is an observation, not proof of completion/);
 assert.match(messages.charged,/^Last recorded runner charge: 2 slot\(s\)\. Not a live capacity reading\./);
});

test('paused state message promises pending and unknown operations are preserved',()=>{
 const paused={...stockView,state:'paused_maintenance',reason:'operator_pause_active',paused:true,
  actions:actions.map(a=>a.action==='pause'?{...a,enabled:false,reason:'already_paused'}:a),
  resume:resume(true,'',false,'pause_must_be_lifted_first')};
 delete paused.operation;
 const messages=stockApplyMessages(stockApplyView(paused));
 assert.match(messages.state,/pending or unknown operations are preserved, never silently cleared/);
});

test('stock interface refusal explains why the path stays observe-only',()=>{
 const messages=stockApplyMessages(stockApplyView(stockView));
 assert.equal(messages.refusals.length,1);
 assert.match(messages.refusals[0],/neither conditional nor authoritative/);
});

test('pause lift and fresh-check readiness are reported as different states',()=>{
 const paused={...stockView,state:'paused_maintenance',reason:'operator_pause_active',paused:true,
  actions:actions.map(a=>a.action==='pause'?{...a,enabled:false,reason:'already_paused'}:a.action==='resume'?{...a,enabled:true,reason:'lifts_local_pause_only'}:a),
  resume:resume(true,'',false,'pause_must_be_lifted_first')};
 delete paused.operation;
 const view=stockApplyView(paused);
 assert.equal(view.resume.lift_pause,true);
 assert.equal(view.resume.ready,false);
 const messages=stockApplyMessages(view);
 assert.match(messages.resumeLift,/this panel cannot do it/);
 assert.match(messages.resumeLift,/never clears, resolves or reclassifies/);
 assert.match(messages.resumeReady,/pause must be lifted first/);
 const ready={...stockView,capability:{supported:true,refusals:[]},resume:resume(false,'not_paused',true,'')};
 assert.match(stockApplyMessages(stockApplyView(ready)).resumeReady,/every fresh drift check passes/);
 assert.equal(stockApplyView({...ready,readback:{maximum:operation.observed,fresh:false}}).reason,'view_unavailable');
 assert.equal(stockApplyView({...ready,readback:{maximum:operation.suggested,fresh:true}}).reason,'view_unavailable');
 assert.match(stockApplyMessages(stockApplyView({...stockView,resume:resume(false,'not_paused',false,'stale_evidence')})).resumeReady,/stale evidence/);
});

test('adapter requirement and physical scope refusals are rendered with their detail',()=>{
 const view=stockApplyView({...stockView,capability:{supported:false,refusals:[{code:'requirement_unestablished',detail:'exclusive_authorized_change_boundary'},{code:'physical_allocation_teardown_out_of_scope',detail:'verified_settings_only'}]}});
 const messages=stockApplyMessages(view);
 assert.ok(messages.refusals.some(line=>/exclusive_authorized_change_boundary/.test(line)));
 assert.ok(messages.refusals.some(line=>/physical allocation or teardown/.test(line)));
});

test('missing charge stays unavailable and a recorded zero is explicit',()=>{
 const unavailable=stockApplyDiagnosticsUnavailable();
 assert.equal(unavailable.charged,null);
 assert.equal(unavailable.paused,null);
 assert.equal(unavailable.resume.ready,false);
 assert.match(stockApplyMessages(unavailable).charged,/unavailable here. This is not a zero count/);
 const zero=stockApplyView({...stockView,operation:{...operation,charged:0},charged:{...stockView.charged,runners:0}});
 assert.match(stockApplyMessages(zero).charged,/Last recorded runner charge: 0 slot/);
});

test('recovery separates a local pause and settings observations from an upstream result',()=>{
 const paused={...stockView,state:'paused_maintenance',paused:true,resume:resume(true,'',false,'pause_must_be_lifted_first')};
 for(const state of ['dispatched','outcome_unknown']){
  const operationHere={...operation,state};
  for(const maximum of [0,operationHere.observed,operationHere.suggested,1000000]){
   for(const fresh of [false,true]){
    const messages=stockApplyMessages(stockApplyView({...paused,operation:operationHere,readback:{maximum,fresh}}));
    assert.match(messages.localPause,/^Applied/);
    assert.match(messages.upstream,/^Unresolved/);
    assert.equal(messages.settings,`Maximum ${maximum} (${fresh?'fresh':'stale'} observation).`);
   }
  }
 }
 assert.match(stockApplyMessages(stockApplyView({...paused,operation:{...operation,state:'proposed'}})).upstream,/No submission/);
 assert.match(stockApplyMessages(stockApplyView({...paused,operation:{...operation,state:'applied_when_proven'}})).upstream,/independently proven/);
 const unavailable=stockApplyMessages(stockApplyDiagnosticsUnavailable());
 assert.equal(unavailable.upstream,'Not observed.');
 assert.equal(unavailable.localPause,'Not observed.');
 assert.equal(unavailable.settings,'Not observed.');
});
