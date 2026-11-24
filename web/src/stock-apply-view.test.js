import test from 'node:test';
import assert from 'node:assert/strict';
import { stockApplyView, stockApplyMessages } from './stock-apply-view.js';

const scope='ab'.repeat(32);
const operation={operation_id:'01'.repeat(16),scope,pool_id:'pool',repository_id:'repo',source:'checked-source',fingerprint:'checked-settings',observed:3,suggested:4,charged:2,direction:'increase',state:'proposed',drift:false,plan_expires_at:'2026-11-24T10:00:20Z',created_at:'2026-11-24T10:00:00Z',updated_at:'2026-11-24T10:00:00Z'};
const action=(action,enabled,reason)=>({action,enabled,...(reason?{reason}:{})});
const actions=[action('apply',false,'no_supported_adapter_implemented'),action('withdraw_proposal',true),action('pause',true),action('resume',false,'not_paused'),action('resolve_outcome',false,'receipt_resolution_unavailable'),action('rollback',false,'rollback_requires_original_outcome_and_current_state')];
const stockView={scope,state:'proposed',reason:'',paused:false,drift:false,operation,actions,
 charged:{runners:2,reason:'a proposal changes nothing upstream; the configured maximum and all existing runner charges remain fully counted'},
 capability:{supported:false,refusals:[{code:'stock_garm_v0_2_1_observe_only',detail:'stock_garm_v0_2_1'}]}};

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
  {...stockView,reason:'Upper Case'}]){
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
 assert.match(messages.charged,/^Still charged: 2 runner slot\(s\)\./);
});

test('paused state message promises pending and unknown operations are preserved',()=>{
 const paused={...stockView,state:'paused_maintenance',reason:'operator_pause_active',paused:true,
  actions:actions.map(a=>a.action==='pause'?{...a,enabled:false,reason:'already_paused'}:a)};
 delete paused.operation;
 const messages=stockApplyMessages(stockApplyView(paused));
 assert.match(messages.state,/pending or unknown operations are preserved, never silently cleared/);
});

test('stock interface refusal explains why the path stays observe-only',()=>{
 const messages=stockApplyMessages(stockApplyView(stockView));
 assert.equal(messages.refusals.length,1);
 assert.match(messages.refusals[0],/neither conditional nor authoritative/);
});

test('adapter requirement and physical scope refusals are rendered with their detail',()=>{
 const view=stockApplyView({...stockView,capability:{supported:false,refusals:[{code:'requirement_unestablished',detail:'exclusive_authorized_change_boundary'},{code:'physical_allocation_teardown_out_of_scope',detail:'verified_settings_only'}]}});
 const messages=stockApplyMessages(view);
 assert.ok(messages.refusals.some(line=>/exclusive_authorized_change_boundary/.test(line)));
 assert.ok(messages.refusals.some(line=>/physical allocation or teardown/.test(line)));
});
