import React from 'react';
import {garmConflictDetails} from './garm-conflict.js';
export function GarmConflictDetails({previous, latest, stale = false}) {
  const detail = garmConflictDetails(previous, latest);
  return <section aria-label="Observed Garm conflict details">
    <h3>{stale ? "Earlier settings → last successful read" : "Earlier settings → latest observed settings"}</h3>
    {stale && <p>The latest refresh failed. This retained comparison is not current settings evidence.</p>}
    <p>Your draft is not changed or merged. These observations cannot identify who changed settings or prove deletion, draining or available capacity.</p>
    {detail.unavailable ? <p>{stale ? "No field comparison was available on the last successful read." : "Latest settings are unavailable; no field comparison is possible."}</p> : <>
      <ul>{detail.changes.map((change, index) => <li key={index}>{change.kind} {change.id} · {change.field}: {change.before} → {change.after}</li>)}</ul>
      {detail.truncated && <p>Only the first 50 differences are shown. Discard and refresh to inspect current settings.</p>}
    </>}
  </section>;
}
