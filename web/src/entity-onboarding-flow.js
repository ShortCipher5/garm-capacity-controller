import { entityOnboardingView, existingEntityChoice } from "./entity-onboarding.js";
const uuid=/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
function poolView(value){
 if(!value||value.available!==true||!Array.isArray(value.pools)||value.pools.length>1000)throw new Error("unavailable");
 let last="";return value.pools.map(p=>{
 if(!p||typeof p.id!=="string"||!uuid.test(p.id)||p.id==="00000000-0000-0000-0000-000000000000"||p.id<=last||typeof p.enabled!=="boolean"||![p.priority,p.max_runners,p.min_idle_runners].every(n=>Number.isSafeInteger(n)&&n>=0&&n<=1000000)||p.min_idle_runners>p.max_runners)throw new Error("unavailable");
 last=p.id;return {id:p.id,enabled:p.enabled,priority:p.priority,max_runners:p.max_runners,min_idle_runners:p.min_idle_runners};
 });
}
// Host supplies fixed authenticated read-only functions, never browser credentials
// or a URL from discovered data. No draft, enrollment or mutation callbacks exist.
export function createEntityOnboardingFlow({readEntities,readEntityPools,onChange}){
 let generation=0,active=null,disposed=false;
 let state={snapshot:{available:false,entities:[]},pools:[],entity:null,busy:false,message:"",changes:null};
 const emit=()=>{if(!disposed)onChange({...state,changes:state.changes?{added:[...state.changes.added],absent:[...state.changes.absent],changed:[...state.changes.changed]}:null,pools:state.pools.map(p=>({...p})),snapshot:{...state.snapshot,entities:state.snapshot.entities.map(e=>({...e}))}})};
 const begin=()=>{active?.abort();active=new AbortController();return {version:++generation,signal:active.signal}};
 const current=request=>!disposed&&request.version===generation&&!request.signal.aborted;
 const api = {
 async refresh(){if(disposed)return;const request=begin();state={...state,snapshot:{...state.snapshot,available:false},pools:[],entity:null,changes:null,busy:true,message:""};emit();
 try{const value=entityOnboardingView(await readEntities({signal:request.signal}));if(!current(request))return;if(!value.available)throw new Error("unavailable");state={...state,snapshot:value};}
 catch{if(current(request))state={...state,message:"Entity discovery unavailable."};}
 finally{if(current(request)){state={...state,busy:false};emit()}}
 },
 async inspect(choice){if(disposed)return;const valid=choice&&typeof choice.type==="string"&&typeof choice.id==="string"&&existingEntityChoice(state.snapshot,`${choice.type}/${choice.id}`);if(!valid)return;
 const previous=state.entity?.type===valid.type&&state.entity?.id===valid.id&&state.message===""&&!state.busy?state.pools:null;
 const request=begin();state={...state,pools:[],entity:valid,changes:null,busy:true,message:""};emit();
 try{const pools=poolView(await readEntityPools({...valid},{signal:request.signal}));if(current(request)){
 const before=new Map((previous||[]).map(p=>[p.id,p]));const after=new Map(pools.map(p=>[p.id,p]));
 const changes=previous===null?null:{added:pools.filter(p=>!before.has(p.id)).map(p=>p.id),absent:previous.filter(p=>!after.has(p.id)).map(p=>p.id),changed:pools.filter(p=>before.has(p.id)&&["enabled","priority","max_runners","min_idle_runners"].some(k=>p[k]!==before.get(p.id)[k])).map(p=>p.id)};
 state={...state,pools,changes};
 }}
 catch{if(current(request))state={...state,pools:[],entity:null,changes:null,message:"Pool inspection unavailable."};}
 finally{if(current(request)){state={...state,busy:false};emit()}}
 },
 async refreshViewed(){if(disposed||state.busy||!state.entity||state.message)return;return api.inspect({...state.entity});},
 cancel(){if(disposed)return;generation++;active?.abort();active=null;state={...state,snapshot:{...state.snapshot,available:false},pools:[],entity:null,changes:null,busy:false,message:"Reading canceled. Refresh existing entities to continue."};emit();},
 dispose(){disposed=true;generation++;active?.abort();}
 };
 return api;
}
