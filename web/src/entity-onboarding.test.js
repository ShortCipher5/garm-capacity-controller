import assert from "node:assert/strict";
import test from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";
import { entityOnboardingView, existingEntityChoice } from "./entity-onboarding.js";
const row = { type: "organization", id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", pool_count: 2 };
test("only current validated entity observations can request inspection", () => {
  const value={available:true,entities:[row]};
  assert.deepEqual(existingEntityChoice(value,`${row.type}/${row.id}`),{type:row.type,id:row.id});
  assert.equal(existingEntityChoice({...value,available:false},`${row.type}/${row.id}`),null);
  assert.equal(existingEntityChoice(value,"organization/unknown"),null);
  for(const entities of [[{...row,id:[row.id]}], [row,row],[{...row,pool_count:0}],[{...row,type:"<script>"}],[{...row,id:"00000000-0000-0000-0000-000000000000"}],[{...row,pool_count:1001}]]) assert.equal(entityOnboardingView({available:true,entities}).available,false);
  const clean=entityOnboardingView({available:true,entities:[{...row,token:"secret"}]});assert.equal(JSON.stringify(clean).includes("secret"),false);
});
test("onboarding SSR renders read-only stale and empty states without write controls",async()=>{
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:"custom"});
 try{const {ExistingEntityOnboarding}=await server.ssrLoadModule("/src/entity-onboarding.jsx");let calls=0;
 const render=snapshot=>renderToStaticMarkup(React.createElement(ExistingEntityOnboarding,{snapshot,onInspect:()=>calls++}));
 assert.match(render({available:false,entities:[row]}),/stale/);assert.match(render({available:false,entities:[row]}),/select[^>]*disabled/);
 assert.match(render({available:true,entities:[]}),/No pool-owning entities returned/);
 const html=render({available:true,entities:[row]});assert.match(html,/View existing pools/);assert.doesNotMatch(html,/<form|Save|Apply|Enable|Create/);assert.equal(calls,0);
 }finally{await server.close()}
});
