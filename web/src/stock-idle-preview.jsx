import React from 'react';
import {stockIdlePreviewView,stockIdlePreviewMessage} from './stock-idle-preview.js';
export function StockIdleSuggestions({plan,comparison,now}){
 const view=stockIdlePreviewView(plan,comparison,now);
 if(!view)return null;
 return <section aria-label="Stock pool minimum-idle suggestions"><h4>Stock pool minimum-idle suggestions</h4><p>{stockIdlePreviewMessage(view)}</p>
 {view.hostEvidence==='configured_only'&&<p>Configured host only; the provider destination is not verified by this preview.</p>}
 {view.state==='proposed'&&<><p>Evidence expires {new Date(view.expiresAt).toLocaleString()}.</p><div className="table-wrap"><table><thead><tr><th>Repository / pool</th><th>Observed minimum idle</th><th>Suggested minimum idle</th><th>Maximum (unchanged)</th></tr></thead><tbody>{view.proposals.map(p=><tr key={p.pool_id}><td>{p.repository_id}<p>{p.pool_id}</p></td><td>{p.observed_min_idle}</td><td>{p.suggested_min_idle}</td><td>{p.unchanged_maximum}</td></tr>)}</tbody></table></div></>}
 <p>Read-only advice, not applied settings. These observations do not prove complete queue coverage or physical capacity reuse. No runner starts, drains, cleanup or capacity releases are performed by this preview.</p></section>;
}
