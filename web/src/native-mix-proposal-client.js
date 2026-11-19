import {validateMix} from './runner-mix-client.js';
const hash=v=>typeof v==='string'&&/^[a-f0-9]{64}$/.test(v);
const positive=v=>Number.isSafeInteger(v)&&v>0;
export function mixProposalFresh(p,now=Date.now()){return !!p&&Number.isFinite(Date.parse(p.selection_expires_at))&&now<Date.parse(p.selection_expires_at);}
export function validateMixProposal(value,target,saved,disk,now=Date.now()){
 const p=value?.proposal,s=saved?.selection,g=s?.partition?.Guests;
 if(!target||value?.target_id!==target.target_id||saved?.state!=='saved_configuration_not_applied'||s?.physical_capacity_qualified!==false||JSON.stringify(s.binding)!==JSON.stringify(target.binding)||!hash(saved.selection_digest)||!positive(disk)||disk>1000000000||!Number.isFinite(Date.parse(s.saved_at))||Date.parse(s.saved_at)>now||!p||p.host!==target.binding.host||p.selection_digest!==saved.selection_digest||p.selection_expires_at!==s.expires_at||!mixProposalFresh(p,now)||p.state!=='proposed_from_saved_configuration_not_applied'||p.disk_fit_verified!==false||p.configured_guest_cpu_millis!==g?.CPUMilli||p.configured_guest_ram_mib!==Math.floor(g?.MemoryBytes/1048576)||!positive(p.configured_guest_cpu_millis)||!positive(p.configured_guest_ram_mib)||!Array.isArray(p.entries)||p.entries.length>3)throw Error('Runner proposal does not match the exact fresh saved configuration. No values were transferred.');
 const mix=validateMix({host:p.host,revision:0,entries:p.entries});let cpu=0,ram=0;
 for(const e of mix.entries){if(e.disk_mib!==disk)throw Error('Proposal changed the explicit per-runner disk choice.');cpu+=e.cpu_millis*e.count;ram+=e.ram_mib*e.count;}
 if(!Number.isSafeInteger(cpu)||!Number.isSafeInteger(ram)||cpu>p.configured_guest_cpu_millis||ram>p.configured_guest_ram_mib)throw Error('Proposed counts exceed the saved guest CPU/RAM configuration.');
 return{host:p.host,selection_digest:p.selection_digest,selection_expires_at:p.selection_expires_at,entries:mix.entries,configured_guest_cpu_millis:p.configured_guest_cpu_millis,configured_guest_ram_mib:p.configured_guest_ram_mib,disk_fit_verified:false,state:p.state};
}
export async function readMixProposal(token,target,saved,disk,{fetcher=globalThis.fetch,signal}={}){
 if(!hash(token)||!hash(saved?.selection_digest)||!target?.target_id||!positive(disk)||disk>1000000000)throw Error('Enter the separate planning token and explicit per-runner disk MiB (1–1,000,000,000).');
 let r,v;try{r=await fetcher('/admin/api/v1/native-planning',{method:'POST',headers:{Authorization:`Bearer ${token}`,Accept:'application/json','Content-Type':'application/json'},body:JSON.stringify({action:'mix-proposal',target_id:target.target_id,selection_digest:saved.selection_digest,disk_mib:disk}),credentials:'omit',redirect:'error',cache:'no-store',signal});if(r.status!==200)throw Error();v=await r.json();if(signal?.aborted)throw Error();}catch{throw Error('Saved-selection sizing proposal unavailable. Reload or rediscover before explicitly requesting another proposal. No profiles or counts were saved.');}
 return validateMixProposal(v,target,saved,disk);
}
