import test from 'node:test';import assert from 'node:assert/strict';import {mkdtemp,rm} from 'node:fs/promises';import {tmpdir} from 'node:os';import {join} from 'node:path';import React from 'react';import {renderToStaticMarkup} from 'react-dom/server';import {createServer} from 'vite';
test('idle recommendations are passive, configured-only and disappear when stale',async()=>{
 const cacheDir=await mkdtemp(join(tmpdir(),'garm-idle-preview-'));let server;
 try{
 server=await createServer({cacheDir,server:{middlewareMode:true,hmr:false},appType:'custom'});
 const {StockIdleSuggestions}=await server.ssrLoadModule('/src/stock-idle-preview.jsx');
 const id='11111111-1111-4111-8111-111111111111',now=Date.parse('2026-11-18T10:00:00Z');
 const plan={captured_at:new Date(now).toISOString(),expires_at:new Date(now+20000).toISOString(),pools:[{pool:id,repository:id}],stock_idle_preview:{state:'proposed',host_evidence:'configured_only',expires_at:new Date(now+10000).toISOString(),proposals:[{pool_id:id,repository_id:id,observed_min_idle:1,suggested_min_idle:0,unchanged_maximum:3}]}};
 const render=(extra={})=>renderToStaticMarkup(React.createElement(StockIdleSuggestions,{plan,comparison:'linked_at_capture',now,...extra}));
 assert.equal(render({plan:{}}),'');const html=render();
 assert.match(html,/Observed minimum idle/);assert.match(html,/Suggested minimum idle/);assert.match(html,/Maximum \(unchanged\)/);assert.match(html,/<td>1<\/td><td>0<\/td><td>3<\/td>/);
 assert.match(html,/provider destination is not verified/);assert.match(html,/not applied settings/);assert.doesNotMatch(html,/<button|<form/);
 for(const extra of [{now:now+10000},{comparison:'preview_superseded'}])assert.doesNotMatch(render(extra),/<table|provider destination/);
 const increase={...plan,stock_idle_decision_preview:{...plan.stock_idle_preview.proposals[0],direction:'increase',host_evidence:'configured_only',expires_at:new Date(now+5000).toISOString(),observed_min_idle:0,suggested_min_idle:1}};
 const renderedIncrease=render({plan:increase});
 assert.match(renderedIncrease,/increasing minimum idle by 1/);assert.match(renderedIncrease,/<td>0<\/td><td>1<\/td><td>3<\/td>/);
 assert.equal((renderedIncrease.match(/<tbody><tr>/g)||[]).length,1);assert.doesNotMatch(renderedIncrease,/<button|<form/);
 assert.doesNotMatch(render({plan:increase,now:now+5000}),/<table|provider destination/);
 assert.doesNotMatch(render({plan:increase,comparison:'saved_comparison_stale'}),/<table|provider destination/);
 const decrease={...plan,stock_idle_decision_preview:{...increase.stock_idle_decision_preview,direction:'decrease',observed_min_idle:1,suggested_min_idle:0}};
 assert.match(render({plan:decrease}),/decreasing minimum idle by 1/);
 }finally{try{await server?.close();}finally{await rm(cacheDir,{recursive:true,force:true});}}
});
