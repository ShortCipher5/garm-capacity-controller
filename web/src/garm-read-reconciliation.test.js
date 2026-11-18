import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {createServer} from 'vite';
import {reconcileGarmRead} from './garm-read-reconciliation.js';
const state=()=>({settings_available:true,settings:{pools:[],repositories:[]},pool_inventory:{pools:[{id:'existing',provider_name:'provider',max_runners:2,tags:['b','a'],owner_kind:'repository',owner_id:'repo',configured:false}]}});
test('existing unmanaged pools expose direct edits without adopting or mutating observations',()=>{
 const a=state(),b=state();b.pool_inventory.pools[0].max_runners=4;
 assert.deepEqual(reconcileGarmRead(a,b),{changes:[{kind:'Pool',id:'existing',field:'max_runners',before:'2',after:'4'}],truncated:false});
 assert.equal(a.pool_inventory.pools[0].max_runners,2);
 b.pool_inventory.pools[0].owner_id='other';assert.equal(reconcileGarmRead(a,b).changes.length,2);
});
test('first read, reordering, unavailable inventories and journals do not invent deletion or drift',()=>{
 const a=state(),b=state();b.pool_inventory.pools[0].tags.reverse();b.history=[{state:'unknown'}];
 assert.equal(reconcileGarmRead(null,b),null);assert.equal(reconcileGarmRead(a,b),null);
 delete b.pool_inventory;assert.equal(reconcileGarmRead(a,b),null);
 b.pool_inventory={pools:[]};assert.equal(reconcileGarmRead(a,b).changes[0].after,'Not observed');
});
test('managed duplicates collapse; arbitrary secrets are never projected; bounded changes',()=>{
 const a=state(),b=state();a.settings.pools=a.pool_inventory.pools;b.settings.pools=b.pool_inventory.pools;
 b.settings.pools[0].max_runners=5;b.settings.pools[0].token='secret';
 assert.equal(reconcileGarmRead(a,b).changes.length,1);assert.doesNotMatch(JSON.stringify(reconcileGarmRead(a,b)),/secret/);
 b.pool_inventory.pools=Array.from({length:100},(_,i)=>({id:String(i)}));assert.equal(reconcileGarmRead(a,b).changes.length,50);assert.equal(reconcileGarmRead(a,b).truncated,true);
});
test('notice escapes fields, distinguishes stale observation and dismissal from conflict resolution',async()=>{
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:'custom'});
 try{const {GarmReadChanges}=await server.ssrLoadModule('/src/garm-read-reconciliation.jsx');const a=state(),b=state();b.pool_inventory.pools[0].tags=['<script>bad</script>'];const html=renderToStaticMarkup(React.createElement(GarmReadChanges,{changes:reconcileGarmRead(a,b),stale:true,onDismiss(){}}));assert.match(html,/&lt;script&gt;/);assert.match(html,/not confirmed current state/);assert.match(html,/does not discard your draft/);assert.doesNotMatch(html,/<script|<input|<form/);}finally{await server.close();}
});
