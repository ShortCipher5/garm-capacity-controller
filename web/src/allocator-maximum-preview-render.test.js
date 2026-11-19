import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {createServer} from 'vite';

test('optional maximum advice renders in the existing panel with no apply control',async()=>{
 const cacheDir=await mkdtemp(join(tmpdir(),'garm-maximum-preview-vite-'));
 let server;
 try{
  server=await createServer({cacheDir,server:{middlewareMode:true,hmr:false},appType:'custom'});
  const {StockMaximumSuggestions}=await server.ssrLoadModule('/src/allocator-plan.jsx');
  const id='11111111-1111-4111-8111-111111111111', now=Date.parse('2026-11-18T10:00:00Z');
  const plan={captured_at:new Date(now).toISOString(),expires_at:new Date(now+20000).toISOString(),
   pools:[{pool:id,repository:id}],stock_maximum_preview:{state:'proposed',expires_at:new Date(now+10000).toISOString(),
    proposals:[{pool_id:id,repository_id:id,observed:2,suggested:3}]}};
  const render=(delta={})=>renderToStaticMarkup(React.createElement(StockMaximumSuggestions,{plan,comparison:'linked_at_capture',now,...delta}));
  assert.equal(render({plan:{...plan,stock_maximum_preview:undefined}}),'');
  const fresh=render();
  assert.match(fresh,/Observed maximum/);assert.match(fresh,/Suggested maximum/);assert.match(fresh,/<td>2<\/td><td>3<\/td>/);
  assert.doesNotMatch(fresh,/<button|<form/);assert.match(fresh,/No settings changes/);
  const blocked={...plan,stock_maximum_preview:{...plan.stock_maximum_preview,state:'unavailable',reason:'pressure_evidence_unavailable',proposals:[]}};
  assert.match(render({plan:blocked}),/Required pressure evidence/);
  for(const reason of ['private error', ['pressure_evidence_unavailable'], 'awaiting_matching_sample', null]){
   const html=render({plan:{...blocked,stock_maximum_preview:{...blocked.stock_maximum_preview,reason}}});
   assert.match(html,/optional stock maximum preview is unavailable/);assert.doesNotMatch(html,/private error|Required pressure evidence/);
  }
  assert.doesNotMatch(render({plan:blocked,now:now+10000}),/Required pressure evidence/);
  assert.doesNotMatch(render({plan:blocked,comparison:'preview_superseded'}),/Required pressure evidence/);
  const stale=render({now:now+10000});
  assert.match(stale,/preview expired/);assert.doesNotMatch(stale,/<table|<button/);
  const changed=render({comparison:'preview_superseded'});
  assert.match(changed,/does not match/);assert.doesNotMatch(changed,/<table|<button/);
 }finally{
  try {await server?.close();} finally {await rm(cacheDir,{recursive:true,force:true});}
 }
});
