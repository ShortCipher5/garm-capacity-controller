import {test} from 'node:test';
import assert from 'node:assert/strict';
import {validateBrokerPolicy,validateBrokerView,saveBrokerPolicy} from './broker-policy-client.js';
const policy=()=>({revision:0,repositories:[{id:'11111111-1111-4111-8111-111111111111',weight:1,max_runners:5,pools:[{id:'22222222-2222-4222-8222-222222222222',max_runners:4,warm_idle:1}]}]});
test('advisory policy bounds and duplicate ownership',()=>{
 assert.equal(validateBrokerPolicy(policy()).repositories[0].weight,1);
 for(const change of [p=>p.repositories[0].weight=0,p=>p.repositories[0].pools[0].warm_idle=5,p=>p.repositories[0].max_runners=-1,p=>p.repositories.push(p.repositories[0]),p=>p.repositories[0].pools.push(p.repositories[0].pools[0])]){const p=policy();change(p);assert.throws(()=>validateBrokerPolicy(p));}
 assert.throws(()=>validateBrokerView({policy:policy(),advisory_only:false,writes_enabled:true}));
});
test('lost response never retried and old revision sent',async()=>{
 let calls=0;
 await assert.rejects(saveBrokerPolicy('a'.repeat(64),policy(),{fetcher:async(_,options)=>{calls++;assert.equal(JSON.parse(options.body).revision,0);throw new Error();}}),/unknown/);
 assert.equal(calls,1);
 await assert.rejects(saveBrokerPolicy('a'.repeat(64),policy(),{fetcher:async()=>({status:409})}),/changed/);
 await saveBrokerPolicy('a'.repeat(64),policy(),{fetcher:async()=>({ok:true,json:async()=>({saved:true,revision:1,advisory_only:true})})});
});
test('invalid local inputs never dispatch a policy write',async()=>{
 let calls=0;const fetcher=async()=>{calls++;throw new Error('unexpected dispatch');};
 await assert.rejects(saveBrokerPolicy('',policy(),{fetcher}),/management token/);
 const duplicate=policy();duplicate.repositories.push(duplicate.repositories[0]);
 await assert.rejects(saveBrokerPolicy('a'.repeat(64),duplicate,{fetcher}),/repository policy/);
 assert.equal(calls,0);
});

test('draft diagnostics locate the problem without echoing entered identifiers', async()=>{
 const cases=[
  [p=>p.repositories[0].id='private input',/row 1.*repository UUID/],
  [p=>p.repositories.push({...p.repositories[0]}),/row 2.*already used/],
  [p=>p.repositories[0].weight='',/row 1.*fairness weight/],
  [p=>p.repositories[0].max_runners='',/row 1.*runner cap/],
  [p=>p.repositories[0].pools[0].max_runners=6,/row 1, pool row 1.*repository cap/],
  [p=>p.repositories[0].pools[0].warm_idle='',/row 1, pool row 1.*warm idle/],
 ];
 let calls=0;
 for(const [mutate,expected] of cases){
  const p=policy();mutate(p);
  await assert.rejects(saveBrokerPolicy('a'.repeat(64),p,{fetcher:async()=>{calls++;}}),e=>expected.test(e.message)&&!e.message.includes('private input'));
 }
 assert.equal(calls,0);
});
