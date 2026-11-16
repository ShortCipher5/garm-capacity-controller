import React from "react";
import { GarmInventoryBrowser } from "./garm-inventory-browser.jsx";
// Numeric local GARM scaleset IDs are not pool UUIDs or forge scaleset IDs.
export function GarmScaleSetInventory({inventory}) {
 if(!inventory)return <p>Existing scaleset inventory is unavailable or unsupported. This does not mean no scalesets exist.</p>;
 return <section aria-label="Existing Garm scaleset inventory"><h3>Existing Garm scalesets</h3>
 <p>Separate read-only provider-scoped observations, not pool enrollment, physical hosts or capacity grants. No scaleset changes or recreation.</p>
 <GarmInventoryBrowser rows={inventory.scalesets} kind="scalesets">{s=><article key={s.id}><h4>{s.name}</h4>
 <p>Local Garm scaleset ID: {s.id} · Provider: {s.provider_name}</p>
 <p>Owner: {({repository:"Repository",organization:"Organization",enterprise:"Enterprise"})[s.owner_kind] || "Unknown"}{s.owner_id ? ` · ${s.owner_id}` : ""}</p>
 <p>Observed state: {s.state} · {s.enabled ? "Enabled" : "Disabled, not drained"}</p>
 <p>Maximum runners: {s.max_runners} · Minimum idle runners: {s.min_idle_runners} · Generation: {s.generation}</p>
 <p>Upstream requested runner target: {s.desired_runner_count === undefined ? "Not reported" : s.desired_runner_count}. Not observed runners or reserved capacity.</p>
 <p>Future-runner tags: {s.tags.length ? s.tags.join(", ") : "No tags reported"}</p></article>}</GarmInventoryBrowser>
 {!inventory.scalesets.length && <p>No scalesets observed in this provider scope.</p>}
 </section>;
}
