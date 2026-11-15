// A conflict freezes editable settings, not independent read-only inventory.
// An unavailable latest inventory replaces the older observation with undefined;
// never silently keep old pools or interpret missing data as deletion.
export function preserveDraftWithLatestInventory(previous, latest) {
  return {...previous, pool_inventory: latest.pool_inventory};
}
export function retainGarmDraftConflict(previous, latest, conflictLatched, hasDraft, settingsChanged) {
  return conflictLatched || (hasDraft && settingsChanged(previous, latest));
}
