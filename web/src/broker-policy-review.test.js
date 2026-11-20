import test from 'node:test';
import assert from 'node:assert/strict';
import {brokerPolicyDraftReview} from './broker-policy-review.js';
const repo='11111111-1111-4111-8111-111111111111',pool='22222222-2222-4222-8222-222222222222',other='33333333-3333-4333-8333-333333333333';
const policy=()=>({revision:4,repositories:[{id:repo,weight:1,max_runners:5,pools:[{id:pool,max_runners:4,warm_idle:1}]}]});
test('exact review preserves saved values, zero, add/remove and verifies no automatic edit',()=>{
 const saved=policy(), draft=structuredClone(saved);
 draft.repositories[0].weight=2;
 draft.repositories[0].pools[0].warm_idle=0;
 assert.deepEqual(brokerPolicyDraftReview(saved,draft,false),{state:'changed',rows:[
  {repository:repo,fields:[{label:'Fairness weight',before:1,after:2}]},
  {repository:repo,pool,fields:[{label:'Warm idle target',before:1,after:0}]},
 ]});
 assert.deepEqual(saved,policy());
 draft.repositories[0].pools[0].id=other;
 const {rows}=brokerPolicyDraftReview(saved,draft,false);
 assert.equal(rows[1].pool,pool);assert.equal(rows[1].fields[0].after,undefined);
 assert.equal(rows[2].pool,other);assert.equal(rows[2].fields[1].after,0);
 assert.equal(brokerPolicyDraftReview(saved,policy(),false).state,'unchanged');
});
test('stale and unreviewable drafts never expose a misleading comparison',()=>{
 const saved=policy(),draft=policy();
 draft.repositories.push(draft.repositories[0]);
 assert.deepEqual(brokerPolicyDraftReview(saved,draft,false),{state:'invalid',rows:[]});
 assert.deepEqual(brokerPolicyDraftReview(saved,draft,true),{state:'stale',rows:[]});
 assert.deepEqual(brokerPolicyDraftReview(saved,{...draft,revision:5},false),{state:'stale',rows:[]});
});
