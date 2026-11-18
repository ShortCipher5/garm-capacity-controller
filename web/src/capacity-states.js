export function validateCapacityStates(v) {
 const keys=['cpu_millis','ram_mib','disk_mib','runners'];
 const resource=r=>r&&keys.every(k=>Number.isSafeInteger(r[k])&&r[k]>=0);
 if(!v||v.source!=='charged-ledger'||v.grants_available!==false||!Number.isFinite(Date.parse(v.observed_at))||!resource(v.total)||!Array.isArray(v.rows)||v.rows.length>32)throw new Error('Capacity breakdown unavailable.');
 const allowed=new Set(['creating','idle','running','failed','unknown','deleting','orphan']);
 const seen=new Set(),sum=Object.fromEntries(keys.map(k=>[k,0]));
 for(const r of v.rows){if(!r||typeof r.state!=='string'||!allowed.has(r.state)||seen.has(r.state)||!resource(r.resources))throw new Error('Invalid capacity breakdown.');seen.add(r.state);for(const k of keys){sum[k]+=r.resources[k];if(!Number.isSafeInteger(sum[k]))throw new Error('Capacity breakdown overflow.');}}
 if(keys.some(k=>sum[k]!==v.total[k]))throw new Error('Capacity breakdown totals disagree.');
 return v;
}
