import test from 'node:test';import assert from 'node:assert/strict';import{connectionRequest}from './connections-client.js';
const token='c'.repeat(64);const record={id:'candidate',revision:1,state:'draft',credential_ref:'alias',endpoint:'https://192.0.2.10:8443',project:'runners',server_key_sha256:'a'.repeat(64)};
test('connection request is same-origin, nonpersistent and never automatic retry',async()=>{
 let calls=0;const view={connections:[record],targets:[{credential_ref:'alias',endpoint:record.endpoint,project:'runners'}]};
 assert.deepEqual(await connectionRequest(token,null,{fetcher:async(url,options)=>{calls++;assert.equal(url,'/admin/api/v1/onboarding/connections');assert.equal(options.method,'GET');assert.equal(options.credentials,'omit');return{ok:true,json:async()=>view}}}),view);assert.equal(calls,1);
 await assert.rejects(connectionRequest(token,{action:'revoke'},{fetcher:async()=>{calls++;throw new Error('network secret')}}),e=>e.unknown&&!e.message.includes('secret'));assert.equal(calls,2);
 await assert.rejects(connectionRequest('',null,{fetcher:()=>{throw new Error('must not dispatch')}}),/management token/);
});
test('connection writes validate durable result and stale/unknown outcomes require reload',async()=>{
 assert.deepEqual(await connectionRequest(token,{action:'save-draft'},{fetcher:async()=>({ok:true,json:async()=>record})}),record);
 for(const response of [{ok:false,status:409},{ok:false,status:503},{ok:true,json:async()=>({state:'connected'})}])await assert.rejects(connectionRequest(token,{action:'approve'},{fetcher:async()=>response}),e=>e.unknown===true);
 await assert.rejects(connectionRequest(token,null,{fetcher:async()=>({ok:false,status:404})}),/not enabled/);
});
