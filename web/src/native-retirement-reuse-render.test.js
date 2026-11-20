import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import React from 'react';
import {renderToStaticMarkup} from 'react-dom/server';
import {createServer} from 'vite';
test('reuse explanation stays in a passive diagnostic section with legacy fallback',async()=>{
 const cacheDir=await mkdtemp(join(tmpdir(),'garm-retirement-reuse-'));let server;
 try{
  server=await createServer({cacheDir,server:{middlewareMode:true,hmr:false},appType:'custom'});
  const {RetirementReuseDiagnostics}=await server.ssrLoadModule('/src/native-retirement-status.jsx');
  const render=view=>renderToStaticMarkup(React.createElement(RetirementReuseDiagnostics,{view}));
  assert.equal(render({}),'');
  const view={freshness:'within_cycle_window',capacity_reuse:{availability:'last_cycle_diagnostics',reuse_authorized:false,reasons:['physical_capacity_reuse_not_established']}};
  const html=render(view);
  assert.match(html,/Historical cycle explanation only/);assert.match(html,/Physical capacity reuse was not established/);assert.doesNotMatch(html,/<button|<form/);
  const unknown=render({...view,capacity_reuse:{...view.capacity_reuse,reasons:['private_error']}});
  assert.match(unknown,/unavailable or unsupported/);assert.doesNotMatch(unknown,/private_error/);
  assert.match(render({...view,freshness:'stale'}),/unavailable or unsupported/);
 }finally{try{await server?.close();}finally{await rm(cacheDir,{recursive:true,force:true});}}
});
