import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import {renderToStaticMarkup} from "react-dom/server";
import {createServer} from "vite";
import {createEntityOnboardingFlow} from "./entity-onboarding-flow.js";
const row={type:"organization",id:"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",pool_count:1};
const pool={id:"11111111-1111-4111-8111-111111111111",priority:1,enabled:false,max_runners:2,min_idle_runners:0};
const deferred=()=>{let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve}};
test("refresh cancels obsolete inspection and never recreates or enrolls",async()=>{
 const pending=deferred();let view,signal,calls=0;
 const flow=createEntityOnboardingFlow({readEntities:async()=>({available:true,entities:[row]}),readEntityPools:async(choice,options)=>{assert.deepEqual(choice,{type:row.type,id:row.id});signal=options.signal;calls++;return pending.promise},onChange:v=>view=v});
 await flow.refresh();const inspection=flow.inspect(row);assert.equal(calls,1);
 await flow.refresh();assert.equal(signal.aborted,true);pending.resolve({available:true,pools:[pool]});await inspection;assert.deepEqual(view.pools,[]);assert.equal(view.entity,null);
 await flow.inspect({...row,id:[row.id]});assert.equal(calls,1);flow.dispose();await flow.refresh();assert.equal(calls,1);
});
test("late discovery and error details do not replace current read-only view",async()=>{
 const first=deferred();let n=0,view;
 const flow=createEntityOnboardingFlow({readEntities:()=>++n===1?first.promise:Promise.resolve({available:true,entities:[row]}),readEntityPools:async()=>{throw new Error("private-token")},onChange:v=>view=v});
 const old=flow.refresh();await flow.refresh();first.resolve({available:true,entities:[]});await old;assert.equal(view.snapshot.entities.length,1);
 await flow.inspect(row);assert.equal(view.message,"Pool inspection unavailable.");assert.equal(JSON.stringify(view).includes("private-token"),false);flow.dispose();
});
test("optional panel integrates existing management without loading or edit grants",async()=>{
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});try{
 const {GarmManagement}=await server.ssrLoadModule("/src/garm-management.jsx");let calls=0;const read=()=>{calls++;throw new Error("no automatic reads")};
 const plain=renderToStaticMarkup(React.createElement(GarmManagement,{read}));assert.doesNotMatch(plain,/Existing GARM onboarding/);
 const invalid=renderToStaticMarkup(React.createElement(GarmManagement,{read,readEntities:true,readEntityPools:{}}));assert.doesNotMatch(invalid,/Existing GARM onboarding/);
 const html=renderToStaticMarkup(React.createElement(GarmManagement,{read,readEntities:read,readEntityPools:read}));assert.match(html,/Existing GARM onboarding/);assert.match(html,/Refresh existing entities/);assert.equal(calls,0);
 }finally{await server.close()}
});
test("pool projection accepts exact sorted scalars and strips unrelated data",async()=>{
 let view;const flow=createEntityOnboardingFlow({readEntities:async()=>({available:true,entities:[row]}),readEntityPools:async()=>({available:true,pools:[{...pool,secret:"not-present"}]}),onChange:v=>view=v});
 await flow.refresh();await flow.inspect(row);assert.deepEqual(view.pools,[pool]);assert.deepEqual(view.entity,{type:row.type,id:row.id});assert.equal(view.busy,false);assert.equal(JSON.stringify(view).includes("not-present"),false);flow.dispose();
});
test("malformed or unavailable pool observations never retain a prior result",async()=>{
 const second={...pool,id:"22222222-2222-4222-8222-222222222222"};
 const invalid=[{available:false,pools:[pool]},{available:true,pools:null},...[[pool,pool],[second,pool],[{...pool,id:[pool.id]}],[{...pool,priority:1.5}],[{...pool,max_runners:1000001}],[{...pool,min_idle_runners:3}],[{...pool,enabled:"false"}]].map(pools=>({available:true,pools}))];
 for(const response of invalid){let view,n=0;const flow=createEntityOnboardingFlow({readEntities:async()=>({available:true,entities:[row]}),readEntityPools:async()=>++n===1?{available:true,pools:[pool]}:response,onChange:v=>view=v});await flow.refresh();await flow.inspect(row);assert.equal(view.pools.length,1);await flow.inspect(row);assert.deepEqual(view.pools,[]);assert.equal(view.entity,null);assert.equal(view.message,"Pool inspection unavailable.");flow.dispose();}
});
test("disposal aborts in-flight reads and suppresses every later notification",async()=>{
 for(const phase of ["discovery","inspection"]){const pending=deferred();let calls=0,signal;const flow=createEntityOnboardingFlow({readEntities:async options=>{if(phase==="discovery"){signal=options.signal;return pending.promise}return {available:true,entities:[row]}},readEntityPools:async(_choice,options)=>{signal=options.signal;return pending.promise},onChange:()=>calls++});let work;if(phase==="discovery")work=flow.refresh();else{await flow.refresh();work=flow.inspect(row)}const before=calls;flow.dispose();assert.equal(signal.aborted,true);pending.resolve(phase==="discovery"?{available:true,entities:[row]}:{available:true,pools:[pool]});await work;assert.equal(calls,before);}
});
