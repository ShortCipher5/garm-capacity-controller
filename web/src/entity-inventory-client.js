import {validateGarm} from "./garm-client.js";
import {entityOnboardingView} from "./entity-onboarding.js";
const uuid=/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
function inventory(snapshot){
 const observed=validateGarm(snapshot);
 const pools=observed.pool_inventory?.pools;
 if(!Array.isArray(pools)||pools.length>1000)throw new Error("Existing Garm scope observation unavailable.");
 const counts=new Map();
 if(observed.settings_available){
  for(const repo of observed.settings.repositories){
   if(!uuid.test(repo.id)||repo.id==="00000000-0000-0000-0000-000000000000")throw new Error("Existing Garm scope observation unavailable.");
   counts.set("repository/"+repo.id,{type:"repository",id:repo.id,pool_count:0,configured:true});
  }
 }
 for(const p of pools){
  if(!uuid.test(p.id)||p.id==="00000000-0000-0000-0000-000000000000"||!["organization","repository","enterprise"].includes(p.owner_kind)||typeof p.owner_id!=="string")throw new Error("Existing Garm scope observation unavailable.");
  const key=p.owner_kind+"/"+p.owner_id;const row=counts.get(key)||{type:p.owner_kind,id:p.owner_id,pool_count:0};row.pool_count++;counts.set(key,row);
 }
 const view=entityOnboardingView({available:true,entities:[...counts.entries()].sort(([a],[b])=>a<b?-1:a>b?1:0).map(([,v])=>v)});
 if(!view.available)throw new Error("Existing Garm scope observation unavailable.");
 return {view,pools};
}
// Fixed authenticated controller read only. Counts cover configured providers,
// not every pool on the upstream server. No discovered URLs or write clients.
export function createEntityInventoryReader(read){
 return {
  async readEntities(options){return inventory(await read(options)).view;},
  async readEntityPools(choice,options){
   const {view,pools}=inventory(await read(options));
   if(!view.entities.some(e=>e.type===choice?.type&&e.id===choice?.id))throw new Error("Existing Garm scope observation unavailable.");
   return {available:true,pools:pools.filter(p=>p.owner_kind===choice.type&&p.owner_id===choice.id).map(p=>({id:p.id,enabled:p.enabled,priority:p.priority,max_runners:p.max_runners,min_idle_runners:p.min_idle_runners})).sort((a,b)=>a.id<b.id?-1:a.id>b.id?1:0)};
  }
 };
}
