import React from 'react';
import {garmConflictDetails} from './garm-conflict.js';
export function GarmConflictDetails({previous, latest}) {
  const detail = garmConflictDetails(previous, latest);
  return <section aria-label="Observed Garm conflict details">
    <h3>Earlier settings → latest observed settings</h3>
    <p>Your draft is not changed or merged. These observations cannot identify who changed settings or prove deletion, draining or available capacity.</p>
    {detail.unavailable ? <p>Latest settings are unavailable; no field comparison is possible.</p> : <>
      <ul>{detail.changes.map((change, index) => <li key={index}>{change.kind} {change.id} · {change.field}: {change.before} → {change.after}</li>)}</ul>
      {detail.truncated && <p>Only the first 50 differences are shown. Discard and refresh to inspect current settings.</p>}
    </>}
  </section>;
}
