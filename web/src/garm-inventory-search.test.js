import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {createServer} from 'vite';
import {selectInventoryPage,inventoryProviders} from './garm-inventory-search.js';
const rows=()=>Array.from({length:205},(_,i)=>({id:i+1,name:`set ${i+1}`,provider_name:i%2?'east':'west',owner_kind:'organization',owner_id:'owner',tags:i===204?['special']:[]}));
test('local display filters combine exact provider and searchable public metadata without mutation',()=>{
 const source=rows(),copy=structuredClone(source);
 assert.equal(selectInventoryPage(source,{query:'SPECIAL'}).rows[0].id,205);
 assert.equal(selectInventoryPage(source,{query:'special',provider:'east'}).matched,0);
 assert.equal(selectInventoryPage(source,{provider:'eas'}).matched,0);
 assert.equal(selectInventoryPage(source,{query:'organization',provider:'east'}).matched,102);
 assert.equal(selectInventoryPage(source,{query:'205'}).rows[0].id,205);
 assert.deepEqual(source,copy);
 assert.equal(selectInventoryPage([{id:'legacy',provider_name:'east',tags:[]}],{query:'unknown'}).matched,1);
 assert.deepEqual(inventoryProviders(source,'removed'),['east','removed','west']);
});
test('bounded paging covers all rows once, clamps on refresh and never invents empty discovery',()=>{
 const source=rows();const all=[];for(let page=0;page<3;page++)all.push(...selectInventoryPage(source,{page}).rows);
 assert.deepEqual(all,source);assert.equal(selectInventoryPage(source).rows.length,100);
 assert.equal(selectInventoryPage(source,{page:2}).rows.length,5);
 assert.equal(selectInventoryPage(source.slice(0,3),{page:2}).page,0);
 const noMatch=selectInventoryPage(source,{query:'absent'});assert.equal(noMatch.total,205);assert.equal(noMatch.matched,0);
 assert.equal(selectInventoryPage([]).total,0);
});
test('inventory browser limits initial cards and has only local display controls',async()=>{
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:'custom'});
 try{const {GarmInventoryBrowser}=await server.ssrLoadModule('/src/garm-inventory-browser.jsx');
 const html=renderToStaticMarkup(React.createElement(GarmInventoryBrowser,{rows:rows(),kind:'scalesets',children:row=>React.createElement('article',{key:row.id},row.name)}));
 assert.equal((html.match(/<article/g)||[]).length,100);assert.match(html,/Showing 100 of 205 matching scalesets/);assert.match(html,/Page 1 of 3/);assert.match(html,/Next scalesets page/);assert.doesNotMatch(html,/<form|type="submit"|set 205/);
 }finally{await server.close();}
});
