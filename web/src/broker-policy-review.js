import {validateBrokerPolicy} from './broker-policy-client.js';

function changes(before,after,fields){
 return fields.filter(([key])=>before?.[key]!==after?.[key])
  .map(([key,label])=>({label,before:before?.[key],after:after?.[key]}));
}
const repoFields=[['weight','Fairness weight'],['max_runners','Repository runner cap']];
const poolFields=[['max_runners','Pool runner cap'],['warm_idle','Warm idle target']];

function byID(before,after){
 const previous=new Map(before.map(row=>[row.id,row])),next=new Map(after.map(row=>[row.id,row]));
 return [...new Set([...previous.keys(),...next.keys()])].map(id=>[id,previous.get(id),next.get(id)]);
}

export function brokerPolicyDraftReview(saved,draft,stale){
 if(!saved||!draft||stale||saved.revision!==draft.revision)return {state:'stale',rows:[]};
 try{validateBrokerPolicy(draft);}catch{return {state:'invalid',rows:[]};}
 const rows=[];
 for(const [repository,before,after] of byID(saved.repositories,draft.repositories)){
  const repoChanges=changes(before,after,repoFields);
  if(repoChanges.length)rows.push({repository,fields:repoChanges});
  for(const [pool,oldPool,newPool] of byID(before?.pools||[],after?.pools||[])){
   const poolChanges=changes(oldPool,newPool,poolFields);
   if(poolChanges.length)rows.push({repository,pool,fields:poolChanges});
  }
 }
 return {state:rows.length?'changed':'unchanged',rows};
}
