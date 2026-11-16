import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {createServer} from 'vite';
import {validateGarm} from './garm-client.js';
import {preserveDraftWithLatestInventory} from './garm-observation.js';
const row=()=>({id:42,name:'<script>scale</script>',provider_name:'capacity',owner_kind:'organization',owner_id:'11111111-1111-4111-8111-111111111111',enabled:false,state:'created',max_runners:4,min_idle_runners:0,generation:1,tags:['linux'],discovery_fingerprint:'a'.repeat(64)});
const view=()=>({settings_available:false,writes_enabled:false,write_blocked:false,history:[],history_truncated:false,settings:{writes_enabled:false,pools:[],repositories:[]},scaleset_inventory:{scalesets:[row()]}});
test('scaleset collection uses exact local numeric identity and separate truthful observation',()=>{
 const good=view();assert.equal(validateGarm(good),good);
 for(const mutate of [s=>s.id='42',s=>s.id=0,s=>s.id=Number.MAX_SAFE_INTEGER+1,s=>s.owner_kind='pool',s=>s.state='ready',s=>s.enabled='false',s=>s.tags=[''],s=>s.min_idle_runners=5]){const bad=view();mutate(bad.scaleset_inventory.scalesets[0]);assert.throws(()=>validateGarm(bad),/unsupported/);}
 const bad=view();bad.scaleset_inventory.scalesets.push(row());assert.throws(()=>validateGarm(bad),/unsupported/);
 const empty=view();empty.scaleset_inventory.scalesets=[];assert.equal(validateGarm(empty),empty);
 const absent=view();delete absent.scaleset_inventory;assert.equal(validateGarm(absent),absent);
 const previous=view(),latest=view();latest.scaleset_inventory.scalesets=[];
 const merged=preserveDraftWithLatestInventory(previous,latest);assert.equal(merged.settings,previous.settings);assert.equal(merged.scaleset_inventory,latest.scaleset_inventory);
 delete latest.scaleset_inventory;assert.equal(preserveDraftWithLatestInventory(previous,latest).scaleset_inventory,undefined);
});
test('scalesets never render editing or infer physical capacity from metadata',async()=>{
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:'custom'});
 try{const {GarmScaleSetInventory}=await server.ssrLoadModule('/src/garm-scalesets.jsx');const render=inventory=>renderToStaticMarkup(React.createElement(GarmScaleSetInventory,{inventory}));
 const html=render(view().scaleset_inventory);assert.match(html,/Local Garm scaleset ID: 42/);assert.match(html,/&lt;script&gt;scale&lt;\/script&gt;/);assert.match(html,/Disabled, not drained/);assert.match(html,/Organization/);assert.doesNotMatch(html,/<script|<form|type="submit"|Apply reviewed|Review Garm change/);
 assert.match(render(undefined),/unavailable or unsupported/);assert.doesNotMatch(render(undefined),/No scalesets observed/);assert.match(render({scalesets:[]}),/No scalesets observed in this provider scope/);
 }finally{await server.close();}
});
test('desired target is optional legacy metadata, bounded and never clamped to maximum',async()=>{
 const good=view();good.scaleset_inventory.scalesets[0].desired_runner_count=17;assert.equal(validateGarm(good),good);
 for(const value of [-1,1.5,null,'3',Number.MAX_SAFE_INTEGER+1]){const bad=view();bad.scaleset_inventory.scalesets[0].desired_runner_count=value;assert.throws(()=>validateGarm(bad),/unsupported/);}
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:'custom'});
 try{const {GarmScaleSetInventory}=await server.ssrLoadModule('/src/garm-scalesets.jsx');const render=v=>renderToStaticMarkup(React.createElement(GarmScaleSetInventory,{inventory:v.scaleset_inventory}));
 assert.match(render(good),/Upstream requested runner target: 17/);assert.match(render(good),/Not observed runners or reserved capacity/);
 assert.match(render(view()),/Upstream requested runner target: Not reported/);
 good.scaleset_inventory.scalesets[0].desired_runner_count=0;assert.match(render(good),/Upstream requested runner target: 0/);
 }finally{await server.close();}
});
