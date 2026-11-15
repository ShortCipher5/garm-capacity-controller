// Snapshot-only diagnostics. Never authorize placement, create a reservation,
// probe a server, or infer spare capacity from these counts.
export function poolPlacementSummary(pool, hosts) {
  const ids = [...new Set(pool.hosts || [])];
  if (!ids.length) return "No placement hosts configured.";
  let allowing = 0, paused = 0, unavailable = 0;
  for (const id of ids) {
    const matches = hosts.filter((host) => host.id === id);
    if (matches.length !== 1 || typeof matches[0].new_claims_paused !== "boolean") unavailable++;
    else if (matches[0].new_claims_paused) paused++;
    else allowing++;
  }
  return `${allowing} host gates not paused · ${paused} paused · ${unavailable} unavailable in this snapshot`;
}
