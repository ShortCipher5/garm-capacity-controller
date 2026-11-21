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
  const {AllocatorPlan,StockMaximumSuggestions,StockMaximumBatchSuggestions}=await server.ssrLoadModule('/src/allocator-plan.jsx');
  const initial=renderToStaticMarkup(React.createElement(AllocatorPlan,{readPlan:()=>{throw Error('must not fetch on mount');}}));
  assert.match(initial,/Why automatic cross-repository scaling is unavailable here/);
  assert.match(initial,/every runner start must pass/);
  assert.match(initial,/configuration, not proof/);
  assert.match(initial,/lower existing maxima only/);
  const id='11111111-1111-4111-8111-111111111111', now=Date.parse('2026-11-18T10:00:00Z');
  const plan={captured_at:new Date(now).toISOString(),expires_at:new Date(now+20000).toISOString(),
   pools:[{pool:id,repository:id}],stock_maximum_preview:{state:'proposed',expires_at:new Date(now+10000).toISOString(),
    proposals:[{pool_id:id,repository_id:id,observed:2,suggested:3}]}};
  const render=(delta={})=>renderToStaticMarkup(React.createElement(StockMaximumSuggestions,{plan,comparison:'linked_at_capture',now,...delta}));
  assert.equal(render({plan:{...plan,stock_maximum_preview:undefined}}),'');
  const fresh=render();
  assert.match(fresh,/Observed maximum/);assert.match(fresh,/Suggested maximum/);assert.match(fresh,/<td>2<\/td><td>3<\/td>/);
  assert.doesNotMatch(fresh,/<button|<form/);assert.match(fresh,/No settings changes/);
  assert.doesNotMatch(fresh,/Minimum idle:/);
  const withIdle={...plan,stock_maximum_preview:{...plan.stock_maximum_preview,proposals:plan.stock_maximum_preview.proposals.map(p=>({...p,observed_min_idle:0}))}};
  withIdle.stock_maximum_preview.host_evidence="configured_only";
  const idleHTML=render({plan:withIdle});assert.match(idleHTML,/provider destination is not verified/);assert.match(idleHTML,/Minimum idle: 0 \(unchanged\)/);assert.doesNotMatch(idleHTML,/<button|<form/);
  assert.doesNotMatch(render({plan:withIdle,now:now+10000}),/Minimum idle:|provider destination/);
  assert.doesNotMatch(render({plan:withIdle,comparison:'preview_superseded'}),/Minimum idle:/);
  const blocked={...plan,stock_maximum_preview:{...plan.stock_maximum_preview,state:'unavailable',reason:'pressure_evidence_unavailable',proposals:[]}};
  assert.match(render({plan:blocked}),/Required pressure evidence/);
  for(const reason of ['private error', ['pressure_evidence_unavailable'], 'awaiting_matching_sample', null]){
   const html=render({plan:{...blocked,stock_maximum_preview:{...blocked.stock_maximum_preview,reason}}});
   assert.match(html,/optional stock maximum preview is unavailable/);assert.doesNotMatch(html,/private error|Required pressure evidence/);
  }
  assert.doesNotMatch(render({plan:blocked,now:now+10000}),/Required pressure evidence/);
  assert.doesNotMatch(render({plan:blocked,comparison:'preview_superseded'}),/Required pressure evidence/);
  const fit=(reason,state='no_confirmed_proposals')=>({...plan,stock_maximum_preview:{...plan.stock_maximum_preview,state,reason,proposals:[]}});
  const veto=fit('configured_maximum_fit_veto');
  const vetoHTML=render({plan:veto});
  assert.match(vetoHTML,/would exceed a configured budget/);assert.match(vetoHTML,/not a measured host limit or a settings change/);
  assert.doesNotMatch(vetoHTML,/<button|<form/);
  assert.match(render({plan:fit('configured_maximum_fit_unavailable')}),/could not be compared/);
  const slots=fit('configured_runner_slot_veto');
  const slotsHTML=render({plan:slots});
  assert.match(slotsHTML,/configured global or repository runner-slot ceiling/);
  assert.match(slotsHTML,/retained charges can use those slots/);
  assert.match(slotsHTML,/a suggested decrease does not free capacity/);
  assert.doesNotMatch(slotsHTML,/<button|<form|<table/);
  assert.doesNotMatch(render({plan:fit('configured_runner_slot_veto','unavailable')}),/runner-slot ceiling/);
  assert.doesNotMatch(render({plan:slots,now:now+10000}),/runner-slot ceiling/);
  assert.doesNotMatch(render({plan:slots,comparison:'preview_superseded'}),/runner-slot ceiling/);

  assert.doesNotMatch(render({plan:fit('configured_maximum_fit_veto','unavailable')}),/would exceed a configured budget/);
  assert.doesNotMatch(render({plan:veto,now:now+10000}),/would exceed a configured budget/);
  assert.doesNotMatch(render({plan:veto,comparison:'preview_superseded'}),/would exceed a configured budget/);
  const renderBatch=(delta={})=>renderToStaticMarkup(React.createElement(StockMaximumBatchSuggestions,{plan:{...withIdle,stock_maximum_batch_preview:withIdle.stock_maximum_preview},comparison:'linked_at_capture',now,...delta}));
  assert.equal(renderBatch({plan}),"");
  const slotsBatch={...slots.stock_maximum_preview,host_evidence:"configured_only"};
  assert.doesNotMatch(renderBatch({plan:{...withIdle,stock_maximum_batch_preview:slotsBatch}}),/runner-slot ceiling/);
  assert.match(renderBatch(),/does not monitor applied changes, enforce a cooldown, or queue any action/);
  assert.match(renderBatch(),/Minimum idle: 0/);
  assert.doesNotMatch(renderBatch(),/<button|<form/);
  assert.doesNotMatch(renderBatch({now:now+10000}),/<table|Minimum idle:/);
  assert.doesNotMatch(renderBatch({comparison:'preview_superseded'}),/<table|Minimum idle:/);
  const mismatch={...withIdle.stock_maximum_preview,proposals:[{...withIdle.stock_maximum_preview.proposals[0],suggested:1}]};
  assert.doesNotMatch(renderBatch({plan:{...withIdle,stock_maximum_batch_preview:mismatch}}),/<table/);
  const waiting={...withIdle.stock_maximum_preview,state:'no_confirmed_proposals',reason:'awaiting_cadence_window',proposals:[]};
  assert.match(renderBatch({plan:{...withIdle,stock_maximum_batch_preview:waiting}}),/Waiting for a longer span of matching observations/);
  assert.doesNotMatch(render({plan:{...withIdle,stock_maximum_preview:waiting}}),/Waiting for a longer span of matching observations/);
  const stale=render({now:now+10000});
  assert.match(stale,/preview expired/);assert.doesNotMatch(stale,/<table|<button/);
  const changed=render({comparison:'preview_superseded'});
  assert.match(changed,/does not match/);assert.doesNotMatch(changed,/<table|<button/);
 }finally{
  try {await server?.close();} finally {await rm(cacheDir,{recursive:true,force:true});}
 }
});
