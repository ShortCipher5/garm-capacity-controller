import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {createServer} from 'vite';
import {stateGuidance,hostPauseReasons,drainView,blockerGuidance} from './lifecycle.js';
const host={id:'server-a',mode:'enabled',new_claims_paused:false,backend_binding_status:'bound_observation',observation_fresh:true,last_observed_healthy:true};
const data={experimental_provisioning_enabled:true,admission_ready:true,states:{unknown:2,creating:1},hosts:[host],observed_at:'2026-11-18T00:00:00Z'};
test('all admission gates explained without treating enablement as ready',()=>{
 assert.deepEqual(hostPauseReasons(host,data),[]);
 const reasons=hostPauseReasons({...host,mode:'disabled',new_claims_paused:true,backend_binding_status:'legacy_scope_import_required',observation_fresh:false,last_observed_healthy:false},{...data,experimental_provisioning_enabled:false,admission_ready:false});
 assert.equal(reasons.length,6);assert.match(reasons.join(' '),/qualified import/);
 assert.match(hostPauseReasons({...host,new_claims_paused:true},data)[0],/additional gate/);
});
test('unknown states and blockers fail closed',()=>{
 assert.match(stateGuidance('future').action,/cannot infer completion/);
 assert.match(stateGuidance('unknown').action,/Do not repeat creation/);
 assert.match(stateGuidance('failed').action,/does not prove/);
 assert.match(stateGuidance('deleting').action,/authoritative reconciliation/);
 assert.match(blockerGuidance('new_gate'),/Keep retirement blocked/);
});
test('missing or malformed retirement assessment is unavailable, not clear',()=>{
 for(const x of [null,{}, {tracked_allocations:-1}, {tracked_allocations:0,unobserved_reservations:0,unreserved_resources:0,blockers:[{}]}])assert.equal(drainView(x),null);
 assert.equal(drainView({tracked_allocations:0,unobserved_reservations:0,unreserved_resources:0,blockers:[]}).tracked,0);
});
test('rendered status shows uncertainty and replaces old snapshot data',async()=>{
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:'custom'});
 try{
 const {Lifecycle}=await server.ssrLoadModule('/src/lifecycle.jsx');
 const render=d=>renderToStaticMarkup(React.createElement(Lifecycle,{data:d}));
 const html=render(data);assert.match(html,/Outcome unknown/);assert.match(html,/Pending creation/);assert.match(html,/Retirement assessment unavailable/);assert.match(html,/not live GitHub job status/);assert.match(html,/not a runtime, backend capacity or production-readiness guarantee/);
 const empty=render({...data,states:{},hosts:[]});assert.doesNotMatch(empty,/server-a|Outcome unknown/);assert.match(empty,/does not prove no runners exist/);
 const zero=render({...data,hosts:[{...host,drain_assessment:{tracked_allocations:0,unobserved_reservations:0,unreserved_resources:0,blockers:[]}}]});assert.match(zero,/empty blocker list is not retirement approval/);
 }finally{await server.close();}
});
