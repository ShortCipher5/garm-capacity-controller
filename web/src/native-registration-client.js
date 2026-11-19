import {validSSHTarget} from './ssh-inventory-client.js';
import {validateNativePlanningView} from './native-planning-client.js';
// The legacy inventory widget accepts only short aliases; native inventory also permits bounded opaque references.
const registrationInventory=t=>t&&typeof t.credential_ref==='string'&&new TextEncoder().encode(t.credential_ref).length>0&&new TextEncoder().encode(t.credential_ref).length<=256&&!/[\x00\r\n]/.test(t.credential_ref)&&validSSHTarget({...t,credential_ref:'validated-reference'});
const hash=v=>typeof v==='string'&&/^[a-f0-9]{64}$/.test(v);
const id=v=>typeof v==='string'&&/^[A-Za-z0-9_-]{1,64}$/.test(v);
const object=v=>v&&typeof v==='object'&&!Array.isArray(v);
const stable=v=>JSON.stringify(v&&typeof v==='object'?(Array.isArray(v)?v.map(x=>JSON.parse(stable(x))):Object.fromEntries(Object.keys(v).sort().map(k=>[k,JSON.parse(stable(v[k]))]))):v);
const equal=(a,b)=>stable(a)===stable(b);
const refs=['template','identity','administrator','artifact_reference','storage_reference','network_reference'];
function report(v,states){
 if(!object(v)||!id(v.target_id)||!hash(v.registration_digest)||!hash(v.preparation_id)||!hash(v.revision)||!states.includes(v.state)||v.activation_authorized!==false||!refs.every(k=>id(v[k])))throw Error('Unsupported registration history or review; preserve reserved identities.');
 validateNativePlanningView({targets:[{target_id:v.target_id,binding:v.binding,binding_digest:v.registration_digest}]});
 if(v.binding.preparation_id!==v.preparation_id||v.binding.inventory_target.id!==v.target_id)throw Error('Registration identity mismatch.');return v;
}
export function validateNativeRegistrationView(v){
 const c=v?.catalog;
 if(!object(c)||!hash(c.revision)||!Array.isArray(c.templates)||c.templates.length>64||!Array.isArray(c.identities)||c.identities.length>256||!Array.isArray(c.administrators)||c.administrators.length>64||!c.administrators.every(id)||new Set(c.administrators).size!==c.administrators.length||!c.templates.every(t=>id(t?.name)&&['artifact_reference','storage_reference','network_reference'].every(k=>id(t[k])))||new Set(c.templates.map(t=>t.name)).size!==c.templates.length||!c.identities.every(i=>id(i?.name)&&(i.host_prefix===''||id(i.host_prefix))&&c.templates.some(t=>t.name===i.template)&&c.administrators.includes(i.administrator))||new Set(c.identities.map(i=>i.name)).size!==c.identities.length||!Array.isArray(v.inventory_targets)||v.inventory_targets.length>256||!v.inventory_targets.every(t=>hash(t?.target_digest)&&registrationInventory(t.target))||new Set(v.inventory_targets.map(t=>t.target.id)).size!==v.inventory_targets.length||!Array.isArray(v.registrations)||v.registrations.length>8)throw Error('Native server registration catalog unavailable. No identity availability inferred.');
 v.registrations.forEach(r=>report(r,['registration_unknown','configuration_registered']));return v;
}
export function registrationConfiguration(view,form){
 validateNativeRegistrationView(view);const c=view.catalog,i=c.identities.find(i=>i.name===form.identity),t=c.templates.find(t=>t.name===i?.template),inventory=view.inventory_targets.find(x=>x.target.id===form.inventory_target_id);
 if(!i||!t||!inventory||!hash(form.preparation_id)||!id(form.host)||!form.host.startsWith(i.host_prefix))throw Error('Choose the saved inventory target and approved identity; use its host prefix and a fresh preparation ID.');
 const native={address:form.address,port:Number(form.port),admin_user:form.admin_user,host_key:form.host_key,admin_key_fingerprint:form.admin_key_fingerprint};
 validateNativePlanningView({targets:[{target_id:inventory.target.id,binding_digest:inventory.target_digest,binding:{preparation_id:form.preparation_id,host:form.host,native_target:native,inventory_target:inventory.target}}]});
 return{revision:c.revision,template:t.name,identity:i.name,administrator:i.administrator,artifact_reference:t.artifact_reference,storage_reference:t.storage_reference,network_reference:t.network_reference,preparation_id:form.preparation_id,host:form.host,inventory_target_id:inventory.target.id,inventory_target_digest:inventory.target_digest,native_target:native};
}
export function validateRegistrationReview(v,configuration,view){
 report(v,['configuration_review_only']);const inventory=view.inventory_targets.find(t=>t.target.id===configuration.inventory_target_id);
 if(v.target_id!==configuration.inventory_target_id||v.preparation_id!==configuration.preparation_id||v.revision!==configuration.revision||v.binding.host!==configuration.host||!equal(v.binding.native_target,configuration.native_target)||!equal(v.binding.inventory_target,inventory?.target)||!refs.every(k=>v[k]===configuration[k]))throw Error('Server review differs from the exact selected configuration.');return v;
}
export async function nativeRegistrationRequest(token,action,{view,form,review,fetcher=globalThis.fetch,signal}={}){
 if(!hash(token))throw Error('Enter the separate native preparation token.');let body;
 if(action){if(!['review','register'].includes(action))throw Error('Unsupported registration action.');const configuration=registrationConfiguration(view,form);body={action,configuration};if(action==='register'){validateRegistrationReview(review,configuration,view);body={...body,review_digest:review.registration_digest,confirm_register:true};}}
 const uncertain='Registration outcome unknown. Load registration history; never automatically retry or reuse reserved identities.';
 let response,value;try{response=await fetcher('/admin/api/v1/native-registration',{method:body?'POST':'GET',headers:{Authorization:`Bearer ${token}`,Accept:'application/json',...(body?{'Content-Type':'application/json'}:{})},...(body?{body:JSON.stringify(body)}:{}),credentials:'omit',redirect:'error',cache:'no-store',signal});if(response.status!==200)throw Error();value=await response.json();if(signal?.aborted)throw Error();}catch{throw Error(action==='register'?uncertain:'Registration read/review unavailable. Reload the catalog; no availability or success inferred.');}
 if(!action)return validateNativeRegistrationView(value);
 if(action==='review')return validateRegistrationReview(value?.review,body.configuration,view);
 if(!object(value)||value.target_id!==review.target_id||value.registration_digest!==review.registration_digest||value.state!=='registered_configuration_without_preparation_plan')throw Error(uncertain);return value;
}
