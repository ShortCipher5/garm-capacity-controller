import {validSSHTarget} from './ssh-inventory-client.js';
const object=v=>v&&typeof v==='object'&&!Array.isArray(v);
const id=v=>typeof v==='string'&&/^[A-Za-z0-9_-]{1,64}$/.test(v);
export async function sshTargetsRequest(token,body,{fetcher=globalThis.fetch,signal}={}){
 if(!/^[0-9a-f]{64}$/.test(token))throw new Error('Enter the separate SSH target-management token.');
 if(body&&(!['review','approve'].includes(body.action)||!id(body.target_id)||!object(body.config)))throw new Error('Complete the server configuration first.');
 let response;try{response=await fetcher('/admin/api/v1/ssh-targets',{method:body?'POST':'GET',headers:{Authorization:`Bearer ${token}`,Accept:'application/json',...(body?{'Content-Type':'application/json'}:{})},...(body?{body:JSON.stringify(body)}:{}),credentials:'omit',redirect:'error',cache:'no-store',signal});}catch{throw new Error('Request unavailable or cancelled. Approval may have saved: reload approved SSH targets before retrying. No server was contacted.');}
 if(!response.ok){if(response.status===401)throw new Error('SSH target-management token rejected.');if(response.status===404)throw new Error('SSH target registration is not enabled.');throw new Error('Configuration rejected or unavailable. Check the approved scope, unique target ID and reviewed host key.');}
 let value;try{value=await response.json()}catch{throw new Error('Unsupported target registration response.');}
 if(!body){const scopes=value?.scopes;if(!object(scopes)||Object.keys(scopes).length>32||!Object.entries(scopes).every(([alias,s])=>id(alias)&&object(s)&&Array.isArray(s.networks)&&s.networks.length>0&&s.networks.length<=32&&s.networks.every(n=>typeof n==='string'&&n.length<=50)&&Array.isArray(s.users)&&s.users.length>0&&s.users.length<=16&&s.users.every(u=>typeof u==='string'&&/^[a-z_][a-z0-9_-]{0,31}$/.test(u))&&Array.isArray(s.ports)&&s.ports.length>0&&s.ports.length<=16&&s.ports.every(p=>Number.isInteger(p)&&p>0&&p<=65535)))throw new Error('Unsupported registration scopes.');return value;}
 if(!validSSHTarget(value?.target)||value.target.id!==body.target_id||value.target.address!==body.config.address||value.target.port!==body.config.port||value.target.user!==body.config.user||value.target.credential_ref!==body.config.credential_ref||value.saved!==(body.action==='approve')||(body.action==='approve'&&value.target.review_digest!==body.review_digest))throw new Error('Mismatched target registration response.');
 return value;
}
