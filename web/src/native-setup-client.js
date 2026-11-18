const name=v=>typeof v==='string'&&/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/.test(v);
const hostName=v=>typeof v==='string'&&v.length>0&&new TextEncoder().encode(v).length<=256&&v.trim()===v&&!/[\p{Cc}\p{Cs}]/u.test(v);
const digest=v=>typeof v==='string'&&/^[a-f0-9]{64}$/.test(v);
const requestID=v=>typeof v==='string'&&/^[a-f0-9]{32}$/.test(v);
const sequence=v=>Number.isSafeInteger(v)&&v>0;
const time=v=>typeof v==='string'&&Number.isFinite(Date.parse(v))&&Date.parse(v)>=0;
const absentTime=v=>v===undefined||v===''||v==='0001-01-01T00:00:00Z';
const array=(v,max)=>Array.isArray(v)&&v.length<=max;
const unique=(rows,key)=>new Set(rows.map(v=>v[key])).size===rows.length;
const fail=()=>{throw new Error('Unsupported native setup response. Reload trusted configuration and recorded status.');};
export function validateCollectionOperation(v,choice){
 if(!v||!requestID(v.request_id)||!name(v.recipe)||!digest(v.recipe_revision)||!['pending','complete','partial','failed','unknown'].includes(v.state)||!time(v.started_at)||!array(v.results,10)||!unique(v.results,'binding_sequence'))fail();
 if(['complete','partial','failed'].includes(v.state)){if(!time(v.finished_at)||Date.parse(v.finished_at)<Date.parse(v.started_at))fail();}
 else if(!absentTime(v.finished_at)&&!time(v.finished_at))fail();
 for(const r of v.results)if(!sequence(r.binding_sequence)||!(r.domain_sequence===undefined||sequence(r.domain_sequence))||!(r.artifact_sequence===undefined||sequence(r.artifact_sequence))||(!r.domain_sequence&&!r.artifact_sequence))fail();
 if(v.state==='partial'&&v.results.length===0||['pending','failed'].includes(v.state)&&v.results.length!==0)fail();
 if(choice){
  if(v.recipe!==choice.id||v.recipe_revision!==choice.revision)fail();
  for(const r of v.results){const target=choice.targets.find(t=>t.binding_sequence===r.binding_sequence);if(!target||target.kind==='domain'&&r.artifact_sequence||target.kind==='artifact'&&r.domain_sequence)fail();}
  if(v.state==='complete'&&(v.results.length!==choice.targets.length||choice.targets.some(t=>!v.results.some(r=>r.binding_sequence===t.binding_sequence&&(t.kind==='artifact'||r.domain_sequence>0)&&(t.kind==='domain'||r.artifact_sequence>0)))))fail();
 }
 return v;
}
export function validateCollectionView(v){
 if(!v||v.accounting_only!==true||v.collection_enabled!==true||typeof v.running!=='boolean'||typeof v.can_start!=='boolean'||!['','collection_running','receipt_history_full'].includes(v.blocked_reason)||!array(v.recipes,100)||!unique(v.recipes,'id')||!array(v.operations,100)||!unique(v.operations,'request_id'))fail();
 if(v.can_start!==(v.blocked_reason==='')||v.running!==(v.blocked_reason==='collection_running'))fail();
 for(const r of v.recipes){if(!name(r.id)||!digest(r.revision)||!array(r.targets,10)||r.targets.length===0||!unique(r.targets,'binding_sequence')||r.targets.some(t=>!hostName(t.host)||!sequence(t.binding_sequence)||!['domain','artifact','both'].includes(t.kind)))fail();}
 for(const op of v.operations){const choice=v.recipes.find(r=>r.id===op.recipe&&r.revision===op.recipe_revision);validateCollectionOperation(op,choice);}
 if(v.operations.some(op=>op.state==='pending')&&!v.running)fail();
 return v;
}
export function newCollectionRequestID(){
 const bytes=new Uint8Array(16);globalThis.crypto.getRandomValues(bytes);return [...bytes].map(v=>v.toString(16).padStart(2,'0')).join('');
}
export async function startCollection(token,choice,id,{signal,fetcher=globalThis.fetch}={}){
 if(!digest(token)||!choice||!name(choice.id)||!digest(choice.revision)||!requestID(id))throw new Error('Review an approved collection recipe and enter the separate management token.');
 const body={recipe:choice.id,recipe_revision:choice.revision,request_id:id};
 let response;try{response=await fetcher('/admin/api/v1/overhead-refresh',{method:'POST',headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json',Accept:'application/json'},body:JSON.stringify(body),credentials:'omit',cache:'no-store',redirect:'error',signal});}
 catch{throw new Error('Collection outcome unknown. Reload durable receipts. Cancelling this view does not cancel accepted work; no write was retried.');}
 if(response.status===401)throw new Error('Management authorization rejected. Reload status before another request.');
 if(response.status===409)throw new Error('Collection configuration or state changed. Reload recipes and durable receipts; no automatic retry was made.');
 if(!response.ok||response.status!==202)throw new Error('Collection outcome unconfirmed. Reload durable receipts before any new request.');
 let op;try{op=validateCollectionOperation(await response.json(),choice);}catch{throw new Error('Collection response invalid; outcome unknown. Reload durable receipts.');}
 if(op.request_id!==id)throw new Error('Collection receipt does not match this request. Reload durable receipts.');
 return op;
}
export function validateEnrollmentView(v){
 if(!v||v.readiness_authorized!==false||typeof v.writes_enabled!=='boolean'||!sequence(v.ledger_sequence)||!array(v.recipes,1000)||!unique(v.recipes,'id')||!array(v.enrolled,1000)||!unique(v.enrolled,'sequence'))fail();
 for(const r of v.recipes)if(!name(r.id)||!digest(r.revision)||!['initial','existing'].includes(r.mode)||!array(r.hosts,1000)||r.hosts.length===0||new Set(r.hosts).size!==r.hosts.length||!r.hosts.every(hostName))fail();
 for(const e of v.enrolled)if(!hostName(e.host)||!digest(e.domain_id)||!sequence(e.sequence)||e.sequence>v.ledger_sequence)fail();
 return v;
}
export async function enrollNativeDomains(token,choice,expected,{signal,fetcher=globalThis.fetch}={}){
 if(!digest(token)||!choice||!name(choice.id)||!digest(choice.revision)||!sequence(expected))throw new Error('Load a current enrollment review and enter the separate management token.');
 const body={recipe:choice.id,recipe_revision:choice.revision,expected_sequence:expected};
 let response;try{response=await fetcher('/admin/api/v1/native-enrollment',{method:'POST',headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json',Accept:'application/json'},body:JSON.stringify(body),credentials:'omit',cache:'no-store',redirect:'error',signal});}
 catch{throw new Error('Enrollment outcome unknown. Reload recorded enrollments before any new request. Cancelling the view does not undo recorded bindings; no retry was made.');}
 if(response.status===401)throw new Error('Management authorization rejected. Reload enrollment review.');
 let v;try{v=await response.json();}catch{throw new Error('Enrollment response unavailable; outcome unknown. Reload recorded enrollments.');}
 if(v?.recipe!==body.recipe||v.recipe_revision!==body.recipe_revision||v.expected_sequence!==expected||v.configuration_only!==true||v.readiness_authorized!==false||typeof v.partial!=='boolean'||!Number.isSafeInteger(v.confirmed_domains)||v.confirmed_domains<0||v.confirmed_domains>choice.hosts.length)throw new Error('Enrollment state or configuration changed, or response could not be correlated. Reload recorded enrollments; no automatic retry was made.');
 if(response.status===200&&response.ok){if(v.partial||v.confirmed_domains!==choice.hosts.length)fail();return{...v,outcome:'confirmed'};}
 if(![409,503].includes(response.status))throw new Error('Enrollment outcome unconfirmed. Reload recorded enrollments.');
 return{recipe:v.recipe,recipe_revision:v.recipe_revision,expected_sequence:v.expected_sequence,confirmed_domains:v.confirmed_domains,partial:v.partial,configuration_only:true,readiness_authorized:false,outcome:'partial_or_failed'};
}
