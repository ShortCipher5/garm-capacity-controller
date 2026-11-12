// Filters describe the recorded snapshot, not live health or placement eligibility.
export function filterHosts(hosts, filter) {
  switch (filter) {
    case "paused": return hosts.filter(host => host.new_claims_paused);
    case "stale": return hosts.filter(host => !host.observation_fresh);
    case "unhealthy": return hosts.filter(host => !host.last_observed_healthy);
    default: return hosts.slice();
  }
}
