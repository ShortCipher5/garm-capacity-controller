const keys=['cpu_millis','ram_mib','disk_mib','runners'];
const resources=v=>v&&keys.every(k=>Number.isSafeInteger(v[k])&&v[k]>=0);
export function validateOverhead(v,global){
 if(!v||!resources(v.domain)||!resources(v.artifact)||!resources(global)||v.domain.runners!==0||v.artifact.runners!==0||v.artifact.cpu_millis!==0||v.artifact.ram_mib!==0||keys.some(k=>v.domain[k]>global[k]||v.artifact[k]>global[k]-v.domain[k]))throw new Error('Retained overhead does not match the charged snapshot.');
 return v;
}
export function overheadSummary(v){return v?`Domain overhead: ${v.domain.ram_mib} MiB RAM; artifact-root overhead: ${v.artifact.disk_mib} MiB disk. Already included above.`:'Retained non-pool overhead breakdown is unavailable on this server; do not assume it is zero.';}
