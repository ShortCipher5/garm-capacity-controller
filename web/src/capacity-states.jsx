import React,{useEffect,useRef,useState} from 'react';
import {capacityUnknownBreakdown} from './capacity-states.js';

function UnknownChargeExplanation({view}) {
 if(!view.rows.some(row=>row.state==='unknown'))return null;
 const split=capacityUnknownBreakdown(view);
 if(!split)return <p>Reason breakdown for unknown charges is unavailable.</p>;
 const labels={recorded_unknown:'State recorded as unknown',state_disagreement:'Reservation and inventory records disagree'};
 return <section aria-label="Why unknown capacity remains charged"><h3>Why unknown capacity remains charged</h3><p>These are parts of the unknown row, not extra charges. Neither proves physical presence or release.</p><ul>{Object.entries(labels).map(([reason,label])=>{const r=split[reason];return <li key={reason}>{label}: {r.runners} runner slots · {r.cpu_millis} mCPU · {r.ram_mib} MiB RAM · {r.disk_mib} MiB disk</li>;})}</ul></section>;
}
export function CapacityStates({read}){
 const [view,setView]=useState(null),[busy,setBusy]=useState(false),[error,setError]=useState('');const generation=useRef(0);
 useEffect(()=>()=>{generation.current++;},[]);
 async function load(){const n=++generation.current;setBusy(true);setView(null);setError('');try{const v=await read();if(n===generation.current)setView(v);}catch(e){if(n===generation.current)setError(e.message);}finally{if(n===generation.current)setBusy(false);}}
 return <section><h2>Charged capacity breakdown</h2><div className="card"><p>Recorded ledger state, not current job status or measured utilization. Released history is excluded; reappearing released instances count as orphans. Conflicting recorded states appear as unknown. Pending, failed and unknown work still consumes capacity.</p><p>Unused grants are capacity charges, not physical runners. This ledger view does not imply the automatic allocator is active.</p><button disabled={busy||!read} onClick={load}>{busy?'Loading…':'Refresh charged capacity'}</button>{error&&<p role="alert">{error}</p>}{view&&<><p>Read at {new Date(view.observed_at).toLocaleString()}. This is a separate snapshot from the overview above.</p><table><thead><tr><th>Recorded state</th><th>Runners</th><th>CPU millicores</th><th>RAM MiB</th><th>Disk MiB</th></tr></thead><tbody>{view.rows.map(r=><tr key={r.state}><td>{r.state}</td><td>{r.resources.runners}</td><td>{r.resources.cpu_millis}</td><td>{r.resources.ram_mib}</td><td>{r.resources.disk_mib}</td></tr>)}</tbody></table><UnknownChargeExplanation view={view}/>{!view.rows.length&&<p>No charged resources recorded.</p>}</>}</div></section>
}
