import React from 'react';

// Configured membership is distinct from any observed host location and from
// runner load. In particular edits do not add up repository and host charges.
export function ServerGroups({view,savedPolicy,stale,change}) {
  // Older APIs don't understand group writes. Don't present a control that can
  // silently disappear on upgrade/downgrade.
  if (!Array.isArray(view.eligible_hosts) || !view.group_charges) return null;
  const groups=view.policy.groups||[], repos=view.policy.repositories;
  const set=(i,patch)=>change(groups.map((g,n)=>n===i?{...g,...patch}:g));
  const toggle=(list,id)=>list.includes(id)?list.filter(v=>v!==id):[...list,id];
  function pick(i,g,field,id,label){
    return <label key={id}><input type="checkbox" checked={g[field].includes(id)} onChange={()=>set(i,{[field]:toggle(g[field],id)})}/>{label}</label>;
  }
  return <section aria-label="Server groups"><h3>Shared server groups</h3>
    <p>Partition servers across repositories. Selecting a repository includes all its policy pools, including pools added later. Alternatively, select individual pools. Zero means no new capacity in that dimension.</p>
    <p>These caps apply inside the SQLite admission ledger. They do not prove that a deployed GARM provider routes every creation through this controller.</p>
    <h4>Saved group limits and charges {stale?'(stale, reload required)':''}</h4>
    {!(savedPolicy.groups||[]).length && <p>No saved groups.</p>}
    {(savedPolicy.groups||[]).map(g=>{const used=view.group_charges[g.id];return <p key={g.id}>{g.name}: {used ? `${used.cpu_millis}/${g.limit.cpu_millis} millicores, ${used.ram_mib}/${g.limit.ram_mib} MiB, ${used.runners}/${g.limit.runners} slots charged/limit` : 'charge unavailable'}. Global, host and pool limits also apply.</p>;})}
    <h4>Draft groups</h4>
    {groups.map((g,i)=><fieldset key={g.id}><legend>Group {i+1}</legend>
      <label>Group name<input required value={g.name} onChange={e=>set(i,{name:e.target.value})}/></label>
      {[['cpu_millis','CPU ceiling (millicores)'],['ram_mib','RAM ceiling (MiB)'],['runners','Runner slot ceiling']].map(([k,label])=><label key={k}>{label}<input type="number" min="0" step="1" required value={g.limit[k]} onChange={e=>set(i,{limit:{...g.limit,[k]:e.target.value===''?'':Number(e.target.value)}})}/></label>)}
      <p>Eligible servers</p>{view.eligible_hosts.map(h=>pick(i,g,'hosts',h,'Server '+h))}
      <p>Member repositories or pools</p>{repos.filter(r=>r.id).map(r=><div key={r.id}>{pick(i,g,'repositories',r.id,'All pools of '+r.id)}{r.pools.filter(p=>p.id).map(p=>pick(i,g,'pools',p.id,'Pool '+p.id))}</div>)}
      <button type="button" onClick={()=>change(groups.filter(x=>x.id!==g.id))}>Remove group from draft</button>
    </fieldset>)}
    <button type="button" onClick={()=>change([...groups,{id:crypto.randomUUID(),name:'',hosts:[],repositories:[],pools:[],limit:{cpu_millis:0,ram_mib:0,disk_mib:0,runners:0}}])}>Add server group</button>
  </section>;
}
