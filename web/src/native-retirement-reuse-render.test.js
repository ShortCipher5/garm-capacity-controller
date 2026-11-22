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
  const view={
   scope:'canonical_retirement_operations_only',state:'processing',freshness:'within_cycle_window',release_authorized:false,
   status:{At:'2026-11-18T10:00:00Z',Incomplete:false,Result:{Examined:0,AwaitingEvidence:0,OutsideScope:0,OperationsCompleted:0,Unresolved:0,RetainedDiagnosticsAvailable:0,RetainedDiagnosticsUnavailable:0,ReleaseAuthorized:false}},
   capacity_reuse:{availability:'last_cycle_diagnostics',reuse_authorized:false,reasons:['physical_capacity_reuse_not_established']},
  };
  const html=render(view);
  assert.match(html,/Historical cycle explanation only/);assert.match(html,/Physical capacity reuse was not established/);assert.doesNotMatch(html,/<button|<form/);
  const unknown=render({...view,capacity_reuse:{...view.capacity_reuse,reasons:['private_error']}});
  assert.match(unknown,/unavailable or unsupported/);assert.doesNotMatch(unknown,/private_error/);
  const inconsistent=render({...view,capacity_reuse:{...view.capacity_reuse,reasons:['operation_outcome_unresolved','physical_capacity_reuse_not_established']}});
  assert.match(inconsistent,/unavailable or unsupported/);assert.doesNotMatch(inconsistent,/Some operation outcomes remained unresolved/);
  assert.match(render({...view,freshness:'stale'}),/unavailable or unsupported/);
 }finally{try{await server?.close();}finally{await rm(cacheDir,{recursive:true,force:true});}}
});
