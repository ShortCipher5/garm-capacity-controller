import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {createServer} from 'vite';
import {garmConflictDetails} from './garm-conflict.js';
const snapshot=()=>({settings_available:true,writes_enabled:true,settings:{writes_enabled:true,pools:[{id:'pool',fingerprint:'secret-hash',enabled:true,tags:['b','a'],max_runners:2}],repositories:[]}});
test('explicit field diff does not mutate draft or expose protected values',()=>{
 const before=snapshot(), next=structuredClone(before);next.settings.pools[0].max_runners=5;next.settings.pools[0].fingerprint='other-secret';next.settings.pools[0].secret='never render';
 assert.deepEqual(garmConflictDetails(before,next).changes,[{kind:'Pool',id:'pool',field:'max_runners',before:'2',after:'5'}]);
 assert.equal(before.settings.pools[0].max_runners,2);
 next.settings.pools[0].max_runners=2; const hidden=JSON.stringify(garmConflictDetails(before,next));assert.match(hidden,/Protected settings/);assert.doesNotMatch(hidden,/secret|never render/);
});
test('tag order is not drift; unavailable and missing are not deletion',()=>{
 const a=snapshot(), b=snapshot();b.settings.pools[0].tags.reverse();assert.equal(garmConflictDetails(a,b).changes.length,0);
 b.settings.pools=[];assert.equal(garmConflictDetails(a,b).changes[0].after,'Not observed');
 b.settings_available=false;assert.deepEqual(garmConflictDetails(a,b),{unavailable:true,changes:[],truncated:false});
});
test('bounded number and value sizes',()=>{
 const a=snapshot(), b=snapshot();b.settings.pools=Array.from({length:100},(_,i)=>({id:String(i)}));const d=garmConflictDetails(a,b);assert.equal(d.changes.length,50);assert.equal(d.truncated,true);
 b.settings.pools=[{...a.settings.pools[0],tags:['x'.repeat(900)]}];assert.ok(garmConflictDetails(a,b).changes[0].after.length<540);
});
test('conflict presentation escapes values, has no write controls and explains authority',async()=>{
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:'custom'});
 try {const {GarmConflictDetails}=await server.ssrLoadModule('/src/garm-conflict.jsx');const a=snapshot(),b=snapshot();b.settings.pools[0].tags=['<script>bad</script>'];const html=renderToStaticMarkup(React.createElement(GarmConflictDetails,{previous:a,latest:b}));assert.match(html,/&lt;script&gt;/);assert.match(html,/draft is not changed or merged/);assert.match(html,/cannot identify who/);assert.doesNotMatch(html,/<script|<button|<input|<form/);}finally{await server.close();}
});

test('a retained comparison is explicitly historical after a failed refresh',async()=>{
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:'custom'});
 try {
  const {GarmConflictDetails}=await server.ssrLoadModule('/src/garm-conflict.jsx');
  const previous=snapshot(),latest=snapshot();
  latest.settings.pools[0].max_runners=5;
  const fresh=renderToStaticMarkup(React.createElement(GarmConflictDetails,{previous,latest}));
  const stale=renderToStaticMarkup(React.createElement(GarmConflictDetails,{previous,latest,stale:true}));
  assert.match(fresh,/latest observed settings/);
  assert.match(stale,/last successful read/);
  assert.match(stale,/not current settings evidence/);
  assert.match(stale,/max_runners: 2 → 5/);
  assert.doesNotMatch(stale,/latest observed settings|<button|<input|<form/);
 } finally {await server.close();}
});
