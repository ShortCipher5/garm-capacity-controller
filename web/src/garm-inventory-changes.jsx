import React, {useEffect,useState} from "react";
import {advanceInventoryObservation} from "./garm-inventory-changes.js";

export function GarmInventoryChangeSummary({latest,kind}) {
 if(!latest)return <p>No {kind} inventory changes recorded in this session yet. The first successful observation establishes a baseline.</p>;
 return <aside aria-label={`${kind} observation changes`}>
  <h4>Latest observed {kind} change in this session</h4>
  <p>Received in this browser: <time dateTime={latest.observedAt}>{latest.observedAt}</time>. Retained through unchanged refreshes; not an audit history or atomic upstream revision.</p>
  <p>These are complete provider-scoped observations, independent of display filters. No longer observed does not prove deletion or authorize recreation, writes or capacity release.</p>
  {[['Newly observed',latest.added],['Changed visible metadata',latest.changed],['No longer observed',latest.absent]].map(([label,group])=><div key={label}><p>{label}: {group.count}</p>{group.count>0&&<ul>{group.ids.map(id=><li key={id}>{id}</li>)}</ul>}{group.truncated&&<p>Only the first 20 IDs shown for this category.</p>}</div>)}
 </aside>;
}
// Parent inventory widgets unmount this tracker when discovery is unavailable,
// so reconnection establishes a new baseline and never compares an outage to [].
export function GarmInventoryChanges({rows,kind}) {
 const [state,setState]=useState(null);
 useEffect(()=>{setState(previous=>advanceInventoryObservation(previous,rows,new Date().toISOString()));},[rows]);
 return <GarmInventoryChangeSummary latest={state?.latest} kind={kind}/>;
}
