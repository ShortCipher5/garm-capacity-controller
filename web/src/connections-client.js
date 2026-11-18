const object=v=>v&&typeof v==='object'&&!Array.isArray(v);
const id=v=>typeof v==='string'&&/^[A-Za-z0-9_-]{1,64}$/.test(v);
export function validateConnectionToken(token){if(!/^[0-9a-f]{64}$/.test(token))throw new Error('Enter the 64-character management token.');}
function record(v){return object(v)&&id(v.id)&&id(v.credential_ref)&&Number.isSafeInteger(v.revision)&&v.revision>0&&['draft','approved','revoked'].includes(v.state)&&typeof v.endpoint==='string'&&typeof v.project==='string'&&/^[0-9a-f]{64}$/.test(v.server_key_sha256);}
export async function connectionRequest(token,body,{fetcher=globalThis.fetch,signal}={}){
 validateConnectionToken(token);
 const unknown=message=>Object.assign(new Error(message),{unknown:true});
 let response;
 try{response=await fetcher('/admin/api/v1/onboarding/connections',{method:body?'POST':'GET',headers:{Authorization:`Bearer ${token}`,Accept:'application/json',...(body?{'Content-Type':'application/json'}:{})},...(body?{body:JSON.stringify(body)}:{}),credentials:'omit',redirect:'error',cache:'no-store',signal});}catch{throw unknown('Connection outcome unknown. Reload connection state before retrying. No automatic retry was made.');}
 if(!response.ok){
  if(response.status===409)throw unknown('Connection changed. Reload and review the new revision.');
  if(response.status===401)throw new Error('Management token rejected.');
  if(response.status===404)throw new Error('Dynamic connection management is not enabled on this deployment.');
  if(response.status>=500)throw unknown('Connection outcome unavailable. Reload before retrying.');
  throw new Error('Connection request rejected. Check the configured destination policy and reviewed key.');
 }
 let value;try{value=await response.json()}catch{throw unknown('Connection response unavailable. Reload before retrying.');}
 if(body){if(!record(value))throw unknown('Unsupported connection response. Reload before retrying.');}
 else if(!object(value)||!Array.isArray(value.connections)||value.connections.length>32||!value.connections.every(record)||!Array.isArray(value.targets)||value.targets.length>32||!value.targets.every(t=>object(t)&&id(t.credential_ref)&&typeof t.endpoint==='string'&&typeof t.project==='string'&&(t.expected_key===undefined||/^[0-9a-f]{64}$/.test(t.expected_key))))throw unknown('Unsupported connection list. No connection was changed.');
 return value;
}
