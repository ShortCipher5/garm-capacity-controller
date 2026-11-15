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

// Inventory is separate from editable settings. Never feed discovery hashes into
// a write review or infer deletion/release from an absent configured pool.
export function GarmPoolInventory({inventory}) {
  if(!inventory)return <p>Existing-pool inventory is unavailable. No missing pool is recreated.</p>;
  return <section aria-label="Existing Garm pool inventory">
    <h3>Existing Garm pools</h3>
    <p>Live read-only inventory for configured providers. Discovering a pool does not authorize editing it. Refreshes are observations, not atomic revisions. Owner metadata does not grant access or identify a physical host.</p>
    {inventory.pools.map(pool=><article key={pool.id}>
      <h4>Pool {pool.id}</h4><p>Provider: {pool.provider_name}</p>
      <p>Owner: {({repository:"Repository",organization:"Organization",enterprise:"Enterprise"})[pool.owner_kind] || "Unknown"}{pool.owner_id ? ` · ${pool.owner_id}` : ""}</p>
      <p>Future-runner tags: {pool.tags.length ? pool.tags.join(", ") : "No tags reported"}</p>
      <p>{pool.configured?"Configured for management; separate write safeguards still apply.":"Discovered outside the management allowlist; read-only."}</p>
      <p>{pool.enabled?"Enabled":"Disabled, not drained"} · Maximum runners: {pool.max_runners} · Minimum idle runners: {pool.min_idle_runners} · Priority: {pool.priority}</p>
    </article>)}
    {!inventory.pools.length && <p>No pools observed in this provider scope.</p>}
    {inventory.missing_configured_pool_ids.length>0 && <div role="status"><h4>Configured pools not observed</h4><p>These pools may have been removed or moved outside provider scope. Absence is not proof of deletion; no runners or capacity are released and nothing is recreated.</p><ul>{inventory.missing_configured_pool_ids.map(id=><li key={id}>{id}</li>)}</ul></div>}
  </section>;
}
