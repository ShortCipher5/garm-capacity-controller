const resource=v=>v&&['cpu_millis','ram_mib','disk_mib','runners'].every(k=>Number.isSafeInteger(v[k])&&v[k]>=0);
export function validateBrokerStatus(status, policy) {
 if(!status||status.source!=='saved-policy-and-charged-ledger'||!Number.isSafeInteger(status.sequence)||status.sequence<0||status.policy_revision!==policy.revision||typeof status.ledger_ready!=='boolean'||!Number.isFinite(Date.parse(status.observed_at))||!Array.isArray(status.pools)||status.pools.length>1500)throw new Error('Policy and charged-capacity comparison unavailable.');
 const expected=new Map(policy.repositories.flatMap(r=>r.pools.map(p=>[p.id,{repository:r.id,desired:p}]))),seen=new Set();
 for(const row of status.pools){
  if(typeof row.pool!=='string'||!row.pool||row.pool.length>256||seen.has(row.pool)||!['not-recorded','recorded','repository-mismatch'].includes(row.mapping)||(row.budget===undefined)!==(row.charged===undefined)||(row.budget!==undefined&&(!resource(row.budget)||!resource(row.charged))))throw new Error('Invalid policy comparison.');
  seen.add(row.pool);const saved=expected.get(row.pool);
  if(saved){if(row.repository!==saved.repository||!row.desired||row.desired.id!==row.pool||row.desired.max_runners!==saved.desired.max_runners||row.desired.warm_idle!==saved.desired.warm_idle)throw new Error('Saved targets disagree with policy.');}
  else if(row.desired!==undefined||row.repository!==undefined)throw new Error('Unexpected saved target.');
 }
 if([...expected.keys()].some(id=>!seen.has(id)))throw new Error('Saved policy pool missing from comparison.');
 return status;
}
