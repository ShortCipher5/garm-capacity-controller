const digest=v=>typeof v==='string'&&/^[0-9a-f]{64}$/.test(v);
const text=(v,max)=>typeof v==='string'&&new TextEncoder().encode(v).length<=max&&!/[\u0000-\u001f\u007f-\u009f]/.test(v);
export function validPreflightRecipe(v){return !!v&&v.mode==='read_only_unqualified'&&digest(v.review_digest)&&digest(v.artifact_sha256)&&digest(v.script_sha256);}
export function validateNativePreflight(v){
 if(!v||v.version!=='native-host-preflight/v1'||v.state!=='unqualified_no_activation'||typeof v.observed_at!=='string'||!Number.isFinite(Date.parse(v.observed_at))||!Array.isArray(v.checks)||v.checks.length===0||v.checks.length>64||new Set(v.checks.map(c=>c?.name)).size!==v.checks.length||v.checks.some(c=>!c||!text(c.name,64)||!c.name||!['observed_prerequisite_only','missing_or_unsupported','informational','unavailable','observed_requirement_only','not_exercised','unqualified'].includes(c.state)||!text(c.required,4096)||!c.required||(c.observed!==undefined&&!text(c.observed,4096))))throw new Error('Unsupported native preflight report. No qualification or readiness was established.');
 return v;
}
export async function requestNativePreflight(token,target,{signal,fetcher=globalThis.fetch}={}){
 if(!digest(token)||!target||! /^[A-Za-z0-9_-]{1,64}$/.test(target.id)||!validPreflightRecipe(target.preflight))throw new Error('Load an approved native preflight recipe and enter the separate firstboot token.');
 let r;try{r=await fetcher('/admin/api/v1/firstboot/preflight',{method:'POST',headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json',Accept:'application/json'},body:JSON.stringify({target_id:target.id,review_digest:target.preflight.review_digest,confirm_read:true}),credentials:'omit',cache:'no-store',redirect:'error',signal});}catch{throw new Error('Diagnostic observation unavailable or cancelled. No report is confirmed; reload the reviewed target before another explicit read. No automatic retry was made.');}
 if(r.status!==200||!r.ok)throw new Error(r.status===401?'Firstboot authorization rejected.':r.status===409?'Preflight target changed, is busy, or is unavailable. Reload the approved target before another explicit read.':'Native diagnostic read unavailable. No qualification was established.');
 try{return validateNativePreflight(await r.json());}catch{throw new Error('Native diagnostic response could not be validated. No qualification or readiness was established.');}
}
