const hash=v=>typeof v==='string'&&/^[a-f0-9]{64}$/.test(v);
const id=v=>typeof v==='string'&&/^[A-Za-z0-9_-]{1,64}$/.test(v);
const text=(v,n)=>typeof v==='string'&&v.length>0&&v.length<=n&&!/[\x00-\x1f\x7f]/.test(v);
const hostKey=v=>{if(typeof v!=='string'||v.length>1024)return false;const normalized=v.replace(/(?:\r?\n)+$/,'');return text(normalized,1024)&&/^ssh-ed25519 [A-Za-z0-9+/]+={0,2}(?: .*)?$/.test(normalized);};
const fields=['preparation_id','catalog_digest','assembly_digest','selection_digest','plan_digest','asset_id','manifest_digest','transfer_digest','review_digest'];
export function validateAssetPlan(p){if(!p||!id(p.slot)||!id(p.target_id)||!fields.every(k=>hash(p[k]))||!text(p.target?.address,255)||!Number.isSafeInteger(p.target.port)||p.target.port<1||p.target.port>65535||!text(p.target.admin_user,64)||!hostKey(p.target.host_key)||!/^SHA256:[A-Za-z0-9+/]{43}$/.test(p.target.admin_key_fingerprint))throw Error('Approved asset plan is unavailable or malformed. Reload local review; no registration is authorized.');return p;}
export function validateAssetPlans(v){if(v?.activation_authorized!==false||!Array.isArray(v.slots)||v.slots.length>8||!Array.isArray(v.plans)||v.plans.length>64||new Set(v.slots.map(s=>s?.slot)).size!==v.slots.length||!v.slots.every(s=>id(s?.slot)&&['available','registered','consumed_unknown','unavailable'].includes(s.state)))throw Error('Asset plan catalog unavailable. No unused slot can be inferred.');const seen=new Set();for(const p of v.plans){validateAssetPlan(p);const key=p.slot+':'+p.target_id;if(seen.has(key)||!v.slots.some(s=>s.slot===p.slot&&s.state==='available'))throw Error('Asset plan catalog is inconsistent. Reload without retrying a registration.');seen.add(key);}return v;}
export async function assetPlansRequest(token,plan=null,{fetcher=fetch,signal}={}){
 if(!hash(token))throw Error('Enter the separate asset-staging token.');if(plan)validateAssetPlan(plan);
 const body=plan?{slot:plan.slot,target_id:plan.target_id,preparation_id:plan.preparation_id,review_digest:plan.review_digest,confirm_asset_plan_registration:true}:null;
 const unknown='Local asset registration outcome unknown. Reload slot status; never repeat a consumed or uncertain registration. No SSH upload was requested.';
 let r,v;try{r=await fetcher('/admin/api/v1/native-asset-plans',{method:body?'POST':'GET',headers:{Authorization:`Bearer ${token}`,Accept:'application/json',...(body?{'Content-Type':'application/json'}:{})},...(body?{body:JSON.stringify(body)}:{}),credentials:'omit',redirect:'error',cache:'no-store',signal});if(signal?.aborted)throw Error();}catch{throw Error(body?unknown:'Asset plan catalog unavailable. No registration was requested.');}
 if(r.status!==(body?201:200))throw Error(body?unknown:r.status===404?'Local asset plan registration is not configured.':'Asset plan catalog unavailable or token rejected.');
 try{v=await r.json();if(signal?.aborted)throw Error();}catch{throw Error(body?unknown:'Asset plan catalog response unavailable.');}
 if(!body)return validateAssetPlans(v);
 if(v?.state!=='registered_not_uploaded'||v.activation_authorized!==false||v.slot!==plan.slot||v.review_digest!==plan.review_digest||v.target_id!==plan.target_id||v.preparation_id!==plan.preparation_id)throw Error(unknown);
 return v;
}
