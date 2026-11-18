import React from 'react';
export function BrokerStatus({status,stale=false}) {
 if(!status)return <p>Saved-target comparison is unavailable on this server.</p>;
 return <section aria-label="Saved targets and charged capacity"><h3>Saved targets and charged capacity</h3>
 <p>Read at {new Date(status.observed_at).toLocaleString()} · policy revision {status.policy_revision}, ledger sequence {status.sequence}. {stale?'This comparison is stale; reload before relying on it.':'This is a recorded ledger view, not live runner status.'}</p>
 <p>Targets below are the saved policy, not your unsaved draft. Charged runners include unused grants and unknown/in-flight resources; they are not running or idle counts. No live demand-derived allocation plan is reported here.</p>
 <p>Ledger reconciliation ready: {status.ledger_ready?'yes':'no'}. Neither readiness nor a recorded mapping proves runtime authority or enables Garm writes.</p>
 <div className="table-wrap"><table><thead><tr><th>Pool / repository</th><th>Saved pool cap</th><th>Saved warm-idle target</th><th>Charged runners</th><th>Declared ledger runner ceiling</th><th>Mapping record</th></tr></thead><tbody>{status.pools.map(row=><tr key={row.pool}><td>{row.pool}<p>{row.repository||'No saved repository policy'}</p></td><td>{row.desired?.max_runners??'Not configured'}</td><td>{row.desired?.warm_idle??'Not configured'}</td><td>{row.charged?.runners??'Pool not in ledger'}</td><td>{row.budget?.runners??'Pool not in ledger'}</td><td>{row.mapping}</td></tr>)}</tbody></table></div>
 {!status.pools.length&&<p>No saved policy or ledger pools.</p>}<p>Lower targets do not release existing charges. Missing mappings and repository mismatches require explicit configuration review, not automatic adoption.</p></section>;
}
