// Compare complete successful observations, never filtered display rows.
// IDs retain their type: local numeric scaleset IDs are not pool UUIDs.
const group = ids => ({count: ids.length, ids: ids.slice(0,20), truncated: ids.length > 20});
export function advanceInventoryObservation(previous, rows, observedAt) {
 if (!rows) return null; // Unavailable is a baseline reset, not an empty list.
 const snapshot = new Map(rows.map(row => [row.id,row.discovery_fingerprint]));
 if (!previous) return {snapshot, latest: null};
 const added=[], changed=[], absent=[];
 for (const [id,fingerprint] of snapshot) {
  if (!previous.snapshot.has(id)) added.push(id);
  else if (previous.snapshot.get(id)!==fingerprint) changed.push(id);
 }
 for (const id of previous.snapshot.keys()) if (!snapshot.has(id)) absent.push(id);
 return {snapshot, latest: added.length+changed.length+absent.length ?
   {observedAt,added:group(added),changed:group(changed),absent:group(absent)} : previous.latest};
}
