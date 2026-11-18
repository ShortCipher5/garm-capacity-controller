import React from 'react';
import {stateGuidance, hostPauseReasons, drainView} from './lifecycle.js';
export function Lifecycle({data}) {
 const rows=Object.entries(data.states).filter(([,count])=>count>0).sort(([a],[b])=>a.localeCompare(b));
 return <section id="lifecycle" aria-labelledby="lifecycle-title"><h2 id="lifecycle-title">Lifecycle attention</h2><div className="card">
 <p>Recorded snapshot at {new Date(data.observed_at).toLocaleString()}. Use Refresh snapshot above to reload. This is not live GitHub job status, an operation queue, or proof that the automatic allocator is running.</p>
 <p>Pending and uncertain outcomes require reconciliation, not a new request. This panel cannot retry work, enable runtime, drain runners or release capacity.</p>
 <h3>Recorded lifecycle states</h3>
 {rows.length ? <div className="table-wrap" role="region" aria-label="Recorded lifecycle states" tabIndex={0}><table><thead><tr><th>Recorded state</th><th>Records</th><th>Meaning and next step</th></tr></thead><tbody>{rows.map(([state,count])=>{const g=stateGuidance(state);return <tr key={state}><td>{g.label} <code>{state}</code></td><td>{count}</td><td>{g.action}</td></tr>;})}</tbody></table></div> : <p>No lifecycle records reported. This does not prove no runners exist or that deployment is ready.</p>}
 <p>Counts are overview records, including released history when reported, not charged resource totals. Use the charged capacity breakdown for capacity accounting.</p>
 <h3>Host admission and retirement blockers</h3>
 {!data.hosts.length&&<p>No hosts are registered in this snapshot. Registration alone does not activate execution.</p>}
 {data.hosts.map(host=>{const reasons=hostPauseReasons(host,data), drain=drainView(host.drain_assessment);return <article key={host.id}><h4>{host.id}: {host.new_claims_paused?'new claims paused':'no admission pause reported'}</h4>
 {reasons.length ? <ul>{reasons.map(r=><li key={r}>{r}</li>)}</ul> : <p>No admission pause is reported. This is not a runtime, backend capacity or production-readiness guarantee.</p>}
 <details><summary>Retirement assessment for {host.id}</summary>{drain ? <><p>{drain.tracked} tracked allocation(s); {drain.unobserved} reservation(s) without inventory; {drain.unreserved} inventory resource(s) without a reservation.</p><ul>{drain.blockers.map(({code,action},i)=><li key={`${code}-${i}`}><code>{code}</code>: {action}</li>)}</ul><p>Even an empty blocker list is not retirement approval or proof of physical absence.</p></> : <p>Retirement assessment unavailable or unsupported. Do not infer drained status from host mode or zero counts.</p>}</details></article>;})}
 <p><a href="#servers">Inspect server observations</a> · <a href="#management">Review ledger policy</a> · <a href="#events">Inspect audit events</a></p>
 </div></section>;
}
