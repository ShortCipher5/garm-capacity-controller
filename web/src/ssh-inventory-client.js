const object=v=>v&&typeof v==='object'&&!Array.isArray(v);
const id=v=>typeof v==='string'&&/^[A-Za-z0-9_-]{1,64}$/.test(v);
const digest=v=>typeof v==='string'&&/^[0-9a-f]{64}$/.test(v);
const target=v=>object(v)&&id(v.id)&&id(v.credential_ref)&&typeof v.address==='string'&&v.address.length<=45&&Number.isInteger(v.port)&&v.port>0&&v.port<=65535&&typeof v.user==='string'&&/^[a-z_][a-z0-9_-]{0,31}$/.test(v.user)&&/^SHA256:[A-Za-z0-9+/]{43}$/.test(v.host_key_fingerprint)&&digest(v.review_digest);
export function validateSSHToken(token){if(!/^[0-9a-f]{64}$/.test(token))throw new Error('Enter the separate 64-character SSH inventory token.');}
export async function sshInventoryRequest(token,body,{fetcher=globalThis.fetch,signal}={}){
 validateSSHToken(token);
 if(body&&(!id(body.target_id)||!digest(body.review_digest)||body.verified_host_key!==true||!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(body.request_id)))throw new Error('Review the configured target and independently verify its SSH host key first.');
 let response;try{response=await fetcher('/admin/api/v1/ssh-inventory',{method:body?'POST':'GET',headers:{Authorization:`Bearer ${token}`,Accept:'application/json',...(body?{'Content-Type':'application/json'}:{})},...(body?{body:JSON.stringify(body)}:{}),credentials:'omit',redirect:'error',cache:'no-store',signal});}catch{throw new Error('SSH inventory unavailable or cancelled. No automatic retry was made.');}
 if(!response.ok){if(response.status===401)throw new Error('SSH inventory token rejected.');if(response.status===404)throw new Error('SSH inventory is not enabled for this deployment.');if(response.status===409)throw new Error('Target review changed or another probe is running. Reload targets and review before retrying.');throw new Error('SSH inventory or its audit is unavailable. No host installation or activation was requested.');}
 let value;try{value=await response.json()}catch{throw new Error('SSH inventory response unavailable.');}
 if(!body){if(!object(value)||!Array.isArray(value.targets)||value.targets.length>32||!value.targets.every(target)||new Set(value.targets.map(t=>t.id)).size!==value.targets.length)throw new Error('Unsupported SSH target list.');return value;}
 const inventory=value?.inventory;
 if(!object(value)||value.target_id!==body.target_id||value.review_digest!==body.review_digest||!object(inventory)||inventory.source!=='authenticated_linux_os_view'||inventory.physical_capacity_qualified!==false||typeof inventory.complete!=='boolean'||typeof inventory.observed_at!=='string'||!Number.isFinite(Date.parse(inventory.observed_at)))throw new Error('Unsupported or mismatched SSH inventory result.');
 for(const key of ['logical_cpus','ram_mib','available_ram_mib','root_disk_mib','root_available_mib'])if(inventory[key]!==undefined&&(!Number.isSafeInteger(inventory[key])||inventory[key]<0))throw new Error('Invalid SSH inventory dimension.');
 const dimensions=['logical_cpus','ram_mib','available_ram_mib','root_disk_mib','root_available_mib'];
 if(inventory.complete!==dimensions.every(k=>inventory[k]!==undefined))throw new Error('Inconsistent SSH inventory completeness.');
 for(const key of ['logical_cpus','ram_mib','root_disk_mib'])if(inventory[key]!==undefined&&inventory[key]===0)throw new Error('Invalid SSH inventory total.');
 if(inventory.available_ram_mib>inventory.ram_mib||inventory.root_available_mib>inventory.root_disk_mib)throw new Error('Inconsistent SSH inventory availability.');
 return value;
}
