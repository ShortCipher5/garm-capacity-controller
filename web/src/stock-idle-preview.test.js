import test from 'node:test';import assert from 'node:assert/strict';
import {stockIdlePreviewView,stockIdlePreviewMessage} from './stock-idle-preview.js';
const id='11111111-1111-4111-8111-111111111111',now=Date.parse('2026-11-18T10:00:00Z');
const plan=()=>({captured_at:new Date(now).toISOString(),expires_at:new Date(now+20000).toISOString(),pools:[{pool:id,repository:id}],stock_idle_preview:{state:'proposed',host_evidence:'configured_only',expires_at:new Date(now+10000).toISOString(),proposals:[{pool_id:id,repository_id:id,observed_min_idle:1,suggested_min_idle:0,unchanged_maximum:3}]}});
test('optional idle preview supports legacy omission and zero suggested idle',()=>{
 assert.equal(stockIdlePreviewView({},'linked_at_capture',now),null);
 const v=stockIdlePreviewView(plan(),'linked_at_capture',now);assert.equal(v.state,'proposed');assert.equal(v.proposals[0].suggested_min_idle,0);
});
test('idle preview rejects malformed, unlinked and authority-like data',()=>{
 for(const mutate of [
 p=>p.stock_idle_preview=null,p=>p.stock_idle_preview.state='applied',p=>delete p.stock_idle_preview.host_evidence,p=>p.stock_idle_preview.host_evidence='verified',
 p=>p.stock_idle_preview.proposals[0].suggested_min_idle=2,p=>p.stock_idle_preview.proposals[0].observed_min_idle='1',
 p=>p.stock_idle_preview.proposals[0].unchanged_maximum=0,p=>p.stock_idle_preview.proposals[0].unchanged_maximum=1000001,
 p=>p.stock_idle_preview.proposals[0].repository_id='22222222-2222-4222-8222-222222222222',
 p=>p.stock_idle_preview.proposals.push({...p.stock_idle_preview.proposals[0]}),
 p=>p.stock_idle_preview.extra='private',p=>p.stock_idle_preview.proposals[0].actionable=true,
 p=>p.stock_idle_preview.expires_at=p.captured_at,p=>p.stock_idle_preview.expires_at=new Date(now+30000).toISOString(),
 p=>p.stock_idle_preview.proposals=Array(9).fill(p.stock_idle_preview.proposals[0]),
 ]){
 const p=plan();mutate(p);assert.deepEqual(stockIdlePreviewView(p,'linked_at_capture',now),{state:'unavailable',proposals:[]});
 }
});
test('expired and changed comparison remove rows and configured evidence',()=>{
 assert.deepEqual(stockIdlePreviewView(plan(),'linked_at_capture',now+10000),{state:'expired',proposals:[]});
 for(const state of ['preview_superseded','saved_comparison_stale','policy_changed'])assert.deepEqual(stockIdlePreviewView(plan(),state,now),{state:'comparison_unavailable',proposals:[]});
});
test('reason allowlist hides raw and malformed diagnostics',()=>{
 for(const reason of ['private error',['awaiting_matching_sample'],null]){
 const p=plan();Object.assign(p.stock_idle_preview,{state:'no_confirmed_proposals',proposals:[],reason});
 const v=stockIdlePreviewView(p,'linked_at_capture',now);assert.equal(v.reason,undefined);assert.doesNotMatch(stockIdlePreviewMessage(v),/private error/);
 }
 const p=plan();Object.assign(p.stock_idle_preview,{state:'no_confirmed_proposals',proposals:[],reason:'awaiting_matching_sample'});
 assert.match(stockIdlePreviewMessage(stockIdlePreviewView(p,'linked_at_capture',now)),/does not prove an empty queue/);
});

test('bounded scope reason is specific only for current unavailable previews',()=>{
 const p=plan();p.stock_idle_preview={state:'unavailable',reason:'scope_limit_exceeded',expires_at:new Date(now+10000).toISOString(),proposals:[]};
 const v=stockIdlePreviewView(p,'linked_at_capture',now);
 assert.equal(v.reason,'scope_limit_exceeded');
 assert.match(stockIdlePreviewMessage(v),/8 observed pools/);
 assert.match(stockIdlePreviewMessage(v),/Disabled pools still count/);
 assert.match(stockIdlePreviewMessage(v),/no configuration was changed/);
 for(const [comparison,at] of [['preview_superseded',now],['linked_at_capture',now+10000]]){
  const stale=stockIdlePreviewView(p,comparison,at);assert.equal(stale.reason,undefined);assert.doesNotMatch(stockIdlePreviewMessage(stale),/scope exceeds/);
 }
 const mismatch=plan();mismatch.stock_idle_preview.reason='scope_limit_exceeded';
 assert.equal(stockIdlePreviewView(mismatch,'linked_at_capture',now).reason,undefined);
 p.stock_idle_preview.reason=['scope_limit_exceeded'];
 assert.equal(stockIdlePreviewView(p,'linked_at_capture',now).reason,undefined);
});
