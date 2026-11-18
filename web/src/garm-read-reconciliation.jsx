import React from 'react';
export function GarmReadChanges({changes, stale, onDismiss}) {
  return <section aria-label="Observed Garm changes">
    <h3>Changes detected by read-only refresh</h3>
    <p role="status">These are the most recent detected differences, not a complete change history. No settings were written or merged. The observations do not identify the writer or prove deletion, runner retirement or available capacity.</p>
    {stale && <p>Refresh is currently unavailable. These differences are from the last successful observations, not confirmed current state.</p>}
    <ul>{changes.changes.map((change,index)=><li key={index}>{change.kind} {change.id} · {change.field}: {change.before} → {change.after}</li>)}</ul>
    {changes.truncated && <p>Only the first 50 differences are shown.</p>}
    <button className="quiet" type="button" onClick={onDismiss}>Dismiss change notice</button>
    <p>Dismissing this notice does not discard your draft, clear a conflict, resolve a pending write or authorize adoption.</p>
  </section>;
}
