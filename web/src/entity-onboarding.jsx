import React, { useEffect, useState } from "react";
import { entityOnboardingView, existingEntityChoice } from "./entity-onboarding.js";
// Unmounted read-only component: host supplies authenticated observation and a
// read-only inspection callback. This does not configure/enroll a write client.
export function ExistingEntityOnboarding({ snapshot, onInspect }) {
  const [selection, setSelection] = useState(null);
  const selected = selection?.snapshot === snapshot ? selection.key : "";
  const view = entityOnboardingView(snapshot);
  const choice = existingEntityChoice(snapshot, selected);
  useEffect(() => { if (!choice) setSelection(null); }, [snapshot, selected, !!choice]);
  return <section aria-label="Existing GARM entities">
    <h3>Choose existing GARM scope</h3>
    <p>Viewing existing pools does not recreate them or authorize changes. Counts cover only configured providers, not all pools on the GARM server.</p>
    {!view.available && <p role="status">Discovery unavailable. Any rows shown are stale and cannot be selected.</p>}
    {view.available && view.entities.length === 0 && <p>No pool-owning entities returned by this server observation.</p>}
    <label>Existing entity <select aria-label="Existing entity" value={choice ? selected : ""} disabled={!view.available} onChange={event => setSelection({ key: event.target.value, snapshot })}>
      <option value="">Select an entity</option>
      {view.entities.map(row => <option key={`${row.type}/${row.id}`} value={`${row.type}/${row.id}`}>{row.type} {row.id} ({row.pool_count} pools)</option>)}
    </select></label>
    <button type="button" disabled={!choice || typeof onInspect !== "function"} onClick={() => { const current = existingEntityChoice(snapshot, selected); if (current && typeof onInspect === "function") onInspect(current); }}>View existing pools</button>
  </section>;
}
