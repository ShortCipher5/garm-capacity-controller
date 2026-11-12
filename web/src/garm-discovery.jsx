import React from "react";

export function canEditGarm(snapshot) {
  return snapshot?.settings_available === true && snapshot.writes_enabled === true && snapshot.settings.writes_enabled === true;
}

// These are separate allowlisted collections. Do not infer a pool/repository
// relationship from their order, names or simultaneous presence in discovery.
export function GarmSettingsPresentation({ snapshot, children }) {
  if (!snapshot.settings_available) return null;
  if (canEditGarm(snapshot)) return children;
  return <div className="garm-discovery" aria-label="Discovered Garm settings">
    <h3>Discovered pools</h3>
    {snapshot.settings.pools.map((pool) => <article key={pool.id}>
      <h4>Pool {pool.id}</h4>
      <p>Provider: {pool.provider_name}</p>
      <p>Pool state: {pool.enabled ? "Enabled" : "Disabled, not drained"}</p>
      <p>Maximum runners: {pool.max_runners} · Minimum idle runners: {pool.min_idle_runners}</p>
      <p>Priority: {pool.priority} · Generation: {pool.generation}</p>
      <p>Future-runner tags: {pool.tags.length ? pool.tags.join(", ") : "No tags reported"}</p>
    </article>)}
    {!snapshot.settings.pools.length && <p>No pools returned in this configured discovery scope.</p>}
    <h3>Discovered repositories</h3>
    {snapshot.settings.repositories.map((repo) => <article key={repo.id}>
      <h4>{repo.owner}/{repo.name}</h4>
      <p>Repository ID: {repo.id}</p>
      <p>Repository balancing: {repo.pool_balancing_type === "pack" ? "Pack" : repo.pool_balancing_type === "roundrobin" ? "Round robin" : "Upstream default"}</p>
    </article>)}
    {!snapshot.settings.repositories.length && <p>No repositories returned in this configured discovery scope.</p>}
  </div>;
}
