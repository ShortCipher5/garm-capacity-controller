import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {createServer} from 'vite';

const scope='ab'.repeat(32);
const operation={operation_id:'01'.repeat(16),scope,pool_id:'pool',repository_id:'repo',source:'checked-source',fingerprint:'checked-settings',observed:3,suggested:4,charged:2,direction:'increase',state:'proposed',drift:false,plan_expires_at:'2026-11-24T10:00:20Z',created_at:'2026-11-24T10:00:00Z',updated_at:'2026-11-24T10:00:00Z'};
const action=(action,enabled,reason)=>({action,enabled,...(reason?{reason}:{})});
const actions=[action('apply',false,'no_supported_adapter_implemented'),action('withdraw_proposal',true),action('pause',true),action('resume',false,'not_paused'),action('resolve_outcome',false,'receipt_resolution_unavailable'),action('rollback',false,'rollback_requires_original_outcome_and_current_state')];
const view={scope,state:'proposed',reason:'',paused:false,drift:false,operation,actions,
 charged:{runners:2,reason:'a proposal changes nothing upstream; the configured maximum and all existing runner charges remain fully counted'},
 capability:{supported:false,refusals:[{code:'stock_garm_v0_2_1_observe_only',detail:'stock_garm_v0_2_1'}]}};

test('controller state renders fully disabled with charges, reasons and no permission grant',async()=>{
 const cacheDir=await mkdtemp(join(tmpdir(),'garm-stock-apply-vite-'));
 let server;
 try{
  server=await createServer({cacheDir,server:{middlewareMode:true,hmr:false},appType:'custom'});
  const {StockApplyControllerView}=await server.ssrLoadModule('/src/stock-apply-view.jsx');
  const {stockApplyView}=await server.ssrLoadModule('/src/stock-apply-view.js');
  const render=(delta={})=>renderToStaticMarkup(React.createElement(StockApplyControllerView,{view:stockApplyView({...view,...delta})}));
  const html=render();
  assert.match(html,/Stock settings apply \(controller state\)/);
  assert.match(html,/grants no write authority/);
  assert.match(html,/Still charged: 2 runner slot\(s\)\./);
  assert.match(html,/neither conditional nor authoritative/);
  assert.match(html,/pool.*maximum from 3 to 4/);
  assert.match(html,/disabled/);
  assert.match(html,/Nothing on this panel changes Garm settings, fences another writer, or grants production permission/);
  // No command renders enabled, no input surface exists at all.
  assert.doesNotMatch(html,/<button(?![^>]*\bdisabled\b)/);
  assert.doesNotMatch(html,/<form|<input|<select|<textarea|<a /);
  // Unknown outcome tells the operator a matching read is not completion proof.
  assert.match(render({state:'outcome_unknown',reason:'outcome_uncertain_pending_resolution',operation:{...operation,state:'outcome_unknown'}}),/matching the old or new value is an observation, not proof of completion/);
  // Pause preserves pending and unknown state visibly.
  const paused={...view,state:'paused_maintenance',reason:'operator_pause_active',paused:true,
   actions:actions.map(a=>a.action==='pause'?{...a,enabled:false,reason:'already_paused'}:a)};
  delete paused.operation;
  assert.match(render(paused),/pending or unknown operations are preserved, never silently cleared/);
  // Drift warns and blocks.
  assert.match(render({drift:true,operation:{...operation,drift:true}}),/drifted from the journaled operation/);
  // Proven outcome stays humble about capacity.
  assert.match(render({state:'applied_when_proven',reason:'outcome_proven_by_authoritative_receipt',operation:{...operation,state:'applied_when_proven'}}),/not a statement about current settings or released capacity/);
  // Untrusted payloads collapse to the unavailable rendering.
  assert.match(render({actions:actions.map(a=>a.action==='apply'?{...a,enabled:true}:a)}),/shown as unavailable/);
 }finally{
  if(server)await server.close();
  await rm(cacheDir,{recursive:true,force:true});
 }
});
