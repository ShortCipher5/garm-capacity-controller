import React,{useEffect,useState} from "react";
import {ExistingEntityOnboarding} from "./entity-onboarding.jsx";
import {createEntityOnboardingFlow} from "./entity-onboarding-flow.js";
export function ExistingEntityOnboardingPanel({readEntities,readEntityPools}){
 const [state,setState]=useState({snapshot:{available:false,entities:[]},pools:[],entity:null,busy:false,message:""});
 const [flow,setFlow]=useState(null);
 useEffect(()=>{setState({snapshot:{available:false,entities:[]},pools:[],entity:null,busy:false,message:""});const next=createEntityOnboardingFlow({readEntities,readEntityPools,onChange:setState});setFlow(next);return()=>next.dispose();},[readEntities,readEntityPools]);
 useEffect(()=>{if(!flow)return;const refresh=()=>{if(document.visibilityState!=="hidden")void flow.refreshViewed();};const timer=setInterval(refresh,15000);window.addEventListener("focus",refresh);document.addEventListener("visibilitychange",refresh);return()=>{clearInterval(timer);window.removeEventListener("focus",refresh);document.removeEventListener("visibilitychange",refresh);};},[flow]);
 return <section aria-label="Existing GARM onboarding"><button type="button" disabled={!flow} onClick={()=>flow?.refresh()}>Refresh existing entities</button>
 {state.busy&&<button type="button" onClick={()=>flow?.cancel()}>Cancel reading</button>}
 <p>Once inspected, this entity is observed every 15 seconds while visible and on focus. Cancel or a failed observation stops automatic reads until you inspect again. No automatic writes.</p>
 {state.busy&&<p role="status">Reading existing GARM scope…</p>}{state.message&&<p role="status">{state.message}</p>}
 <ExistingEntityOnboarding snapshot={state.snapshot} onInspect={choice=>flow?.inspect(choice)}/>
 {state.entity&&!state.busy&&!state.message&&<div aria-label="Existing pools read only"><h4>{state.entity.type} {state.entity.id}</h4>
 {state.entity.type==="repository"&&<p>The provider-scoped listing may omit other pools or scale sets. Repository and pool retirement require an authoritative view of active runners and unsettled operations. For exact pool IDs, review <a href="#pools">configured runner-size and host mappings</a>.</p>}
 <button type="button" onClick={()=>flow?.inspect(state.entity)}>Refresh viewed entity</button>
 {state.changes&&<div aria-label="Entity observation changes"><p>Compared with the previous successful read of this entity: {state.changes.added.length} newly observed, {state.changes.changed.length} changed, {state.changes.absent.length} absent from this provider-scoped observation. Absence does not prove deletion or authorize recreation.</p>{state.changes.changed.map(id=><p key={id}>Changed pool {id}</p>)}</div>}{state.pools.map(pool=><article key={pool.id}><h5>Pool {pool.id}</h5><p>Maximum {pool.max_runners} · Minimum idle {pool.min_idle_runners} · Priority {pool.priority}</p><p>{pool.enabled?"Enabled":"Disabled, not drained"}</p></article>)}{!state.pools.length&&<p>No pools returned in this observation. No deletion or recreation is implied.</p>}</div>}
 </section>;
}
