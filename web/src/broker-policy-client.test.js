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
