import { garmConflictDetails } from './garm-conflict.js';

// Read-side reconciliation only: bounded, public-field differences between two
// successful observations. Never use these as a write precondition or adoption
// signal. Missing/unavailable collections cannot prove a pool was deleted.
export function reconcileGarmRead(previous, latest) {
  if (!previous || !latest) return null;
  const settings = garmConflictDetails(previous, latest);
  let inventory = { changes: [], truncated: false };
  if (previous.pool_inventory && latest.pool_inventory) {
    const project = snapshot => ({settings_available:true, settings:{
      pools:snapshot.pool_inventory.pools, repositories:[],
    }});
    inventory = garmConflictDetails(project(previous), project(latest));
  }
  // Managed pools appear in both collections. Keep only one identical display
  // difference; inventory-only existing pools must not silently disappear.
  const seen = new Set(), changes = [];
  let truncated = settings.truncated || inventory.truncated;
  for (const change of [...settings.changes, ...inventory.changes]) {
    const key = JSON.stringify(change);
    if (seen.has(key)) continue;
    seen.add(key);
    if (changes.length < 50) changes.push(change); else truncated = true;
  }
  return changes.length ? {changes, truncated} : null;
}
