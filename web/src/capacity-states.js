export function validateCapacityStates(v) {
 const keys=['cpu_millis','ram_mib','disk_mib','runners'];
 const resource=r=>r&&keys.every(k=>Number.isSafeInteger(r[k])&&r[k]>=0);
 if(!v||v.source!=='charged-ledger'||typeof v.grants_available!=='boolean'||!Number.isFinite(Date.parse(v.observed_at))||!resource(v.total)||!Array.isArray(v.rows)||v.rows.length>32)throw new Error('Capacity breakdown unavailable.');
 const allowed=new Set(['creating','idle','running','failed','unknown','deleting','orphan','granted']);
 const seen=new Set(),sum=Object.fromEntries(keys.map(k=>[k,0]));
 for(const r of v.rows){if(!r||typeof r.state!=='string'||!allowed.has(r.state)||(r.state==='granted'&&!v.grants_available)||seen.has(r.state)||!resource(r.resources))throw new Error('Invalid capacity breakdown.');seen.add(r.state);for(const k of keys){sum[k]+=r.resources[k];if(!Number.isSafeInteger(sum[k]))throw new Error('Capacity breakdown overflow.');}}
 if(keys.some(k=>sum[k]!==v.total[k]))throw new Error('Capacity breakdown totals disagree.');
 return v;
}

// Optional explanation only. An unsupported or inconsistent split must not
// replace, weaken, or hide the authoritative charged-capacity table.
export function capacityUnknownBreakdown(view) {
 const unknown=Array.isArray(view?.rows)?view.rows.find(row=>row?.state==='unknown')?.resources:null;
 const split=view?.unknown_breakdown;
 const reasons=['recorded_unknown','state_disagreement'];
 const keys=['cpu_millis','ram_mib','disk_mib','runners'];
 const exact=(value,allowed)=>value&&typeof value==='object'&&!Array.isArray(value)&&Object.keys(value).length===allowed.length&&allowed.every(key=>Object.hasOwn(value,key));
 const isCount=value=>Number.isSafeInteger(value)&&value>=0;
 if(!unknown||!exact(split,reasons)||!reasons.every(reason=>exact(split[reason],keys)))return null;
 if(!keys.every(k=>{const a=split.recorded_unknown[k],b=split.state_disagreement[k];return isCount(a)&&isCount(b)&&isCount(unknown[k])&&Number.isSafeInteger(a+b)&&a+b===unknown[k];}))return null;
 return split;
}
