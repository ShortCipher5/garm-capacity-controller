// A conflict freezes editable settings, not independent inventory or journal status.
// An unavailable latest inventory replaces the older observation with undefined;
// never silently keep old pools or interpret missing data as deletion.
export function preserveDraftWithLatestInventory(previous, latest) {
  return {...previous,
    pool_inventory: latest.pool_inventory,
    scaleset_inventory: latest.scaleset_inventory,
    write_blocked: latest.write_blocked,
    managed_client_write_hold: latest.managed_client_write_hold,
    pending: latest.pending,
    history: latest.history,
    history_truncated: latest.history_truncated,
  };
}
export function retainGarmDraftConflict(previous, latest, conflictLatched, hasDraft, settingsChanged) {
  return conflictLatched || (hasDraft && settingsChanged(previous, latest));
}

// A failed refresh is not a successful old or empty collection observation.
// Preserve local draft settings and last-known journal details, explicitly
// marked stale by the caller, but withdraw both independent inventory cards.
export function invalidateGarmInventories(previous) {
  if (!previous) return previous;
  return {...previous, pool_inventory: undefined, scaleset_inventory: undefined};
}
