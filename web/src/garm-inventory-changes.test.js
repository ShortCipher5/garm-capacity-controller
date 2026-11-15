import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {createServer} from 'vite';
import {advanceInventoryObservation as advance} from './garm-inventory-changes.js';
const rows=()=>[{id:1,discovery_fingerprint:'a'},{id:2,discovery_fingerprint:'b'}];
test('successful observations identify exact added/changed/absent IDs without mutating source or inferring deletion',()=>{
 const input=rows(), copy=structuredClone(input), first=advance(null,input,'first');assert.equal(first.latest,null);
 const next=advance(first,[{id:1,discovery_fingerprint:'changed'},{id:3,discovery_fingerprint:'new'}],'second');
 assert.deepEqual(next.latest.added.ids,[3]);assert.deepEqual(next.latest.changed.ids,[1]);assert.deepEqual(next.latest.absent.ids,[2]);assert.equal(next.latest.observedAt,'second');assert.deepEqual(input,copy);
 assert.deepEqual([...first.snapshot.keys()],[1,2]);
 const unchanged=advance(next,[{id:3,discovery_fingerprint:'new'},{id:1,discovery_fingerprint:'changed'}],'third');assert.equal(unchanged.latest,next.latest);
});
test('outage resets comparison while successful empty inventory remains an observation, not deletion authority',()=>{
 const first=advance(null,rows(),'first');const empty=advance(first,[],'empty');assert.equal(empty.latest.absent.count,2);
 const outage=advance(first,undefined,'outage');assert.equal(outage,null);
 const reconnect=advance(outage,[{id:9,discovery_fingerprint:'fresh'}],'reconnect');assert.equal(reconnect.latest,null);
 const typed=advance(first,[{id:'1',discovery_fingerprint:'a'}],'different-domain');assert.deepEqual(typed.latest.added.ids,['1']);assert.deepEqual(typed.latest.absent.ids,[1,2]);
});
test('summaries bound each ID category while preserving exact counts',()=>{
 const old=Array.from({length:100},(_,i)=>({id:i,discovery_fingerprint:'old'}));const current=Array.from({length:100},(_,i)=>({id:i+50,discovery_fingerprint:'new'}));
 const result=advance(advance(null,old,'first'),current,'next');
 for(const group of [result.latest.added,result.latest.changed,result.latest.absent]){assert.equal(group.count,50);assert.equal(group.ids.length,20);assert.equal(group.truncated,true);}
});
test('summary escapes IDs and explicitly withholds deletion and authority claims',async()=>{
 const server=await createServer({server:{middlewareMode:true,hmr:false},appType:'custom'});
 try{const {GarmInventoryChangeSummary}=await server.ssrLoadModule('/src/garm-inventory-changes.jsx');
 const latest=advance(advance(null,rows(),'first'),[{id:'<script>new</script>',discovery_fingerprint:'new'}],'2026-11-15T00:00:00.000Z').latest;
 const html=renderToStaticMarkup(React.createElement(GarmInventoryChangeSummary,{latest,kind:'pools'}));
 assert.match(html,/&lt;script&gt;new/);assert.match(html,/No longer observed does not prove deletion/);assert.match(html,/not an audit history/);assert.doesNotMatch(html,/<script|<button|<form/);
 const initial=renderToStaticMarkup(React.createElement(GarmInventoryChangeSummary,{latest:null,kind:'pools'}));assert.match(initial,/first successful observation establishes a baseline/);
 }finally{await server.close();}
});
