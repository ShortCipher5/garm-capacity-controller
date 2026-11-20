const safe = (value, maximum = 256) => typeof value === "string" && value.length <= maximum && !/[\u0000-\u001f\u007f]/.test(value) ? value : "";

export function writerRecoveryState({ conflict = false, unavailable = false, uncertain = false, snapshot = null } = {}) {
  if (uncertain || snapshot?.write_blocked === true) return "unknown-write";
  if (conflict) return "external-change";
  if (unavailable) return "unavailable";
  return null;
}

const pendingState = value => value === "pending" || value === "unknown" ? value : "";

// Report the evidence basis separately from the existing write-block state.
// Local uncertainty can describe a different operation than the recorded row.
export function writerRecoveryEvidence({ unavailable = false, uncertain = false, snapshot = null } = {}) {
  if (snapshot?.write_blocked === true && !unavailable) {
    const state = pendingState(snapshot.pending?.state);
    return state ? `durable-${state}` : "durable-unclassified";
  }
  if (uncertain) return "browser-uncertain";
  if (snapshot?.write_blocked === true) {
    const state = pendingState(snapshot.pending?.state);
    return state ? `historical-${state}` : "historical-unclassified";
  }
  return null;
}

export function writerRecoveryEvidenceGuidance(evidence) {
  switch (evidence) {
  case "durable-pending": return "The latest journal read recorded a pending intent. This does not prove a request was sent or a worker is still active.";
  case "durable-unknown": return "The latest journal read explicitly recorded an unknown outcome. The external effect remains unresolved.";
  case "durable-unclassified": return "The latest journal read recorded a write block but did not classify the intent.";
  case "browser-uncertain": return "This browser session has an uncertain outcome without a current durable classification. An earlier read cannot settle it.";
  case "historical-pending": return "An earlier journal read recorded a pending intent. The latest check failed; this is historical, not current evidence.";
  case "historical-unknown": return "An earlier journal read recorded an unknown outcome. The latest check failed; do not infer any change.";
  case "historical-unclassified": return "An earlier journal read recorded a write block. The latest check failed, so its current classification is unavailable.";
  default: return "";
  }
}

// Explicit allowlist only: never export credentials, raw errors, arbitrary
// server fields, audit reasons, settings payloads or TLS material.
export function writerRecoveryReport(input = {}, now = new Date()) {
  const state = writerRecoveryState(input);
  if (!state) return null;
  const pending = input.snapshot?.pending;
  const history = Array.isArray(input.snapshot?.history) ? input.snapshot.history : [];
  const operation = value => ({ request_id: safe(value?.request_id), kind: safe(value?.kind, 32), target: safe(value?.target), state: safe(value?.state, 32), created_at: safe(value?.created_at, 64), started_at: safe(value?.started_at, 64) });
  return { format: "garm-writer-recovery/v1", captured_at: now.toISOString(), state, evidence: writerRecoveryEvidence(input),
    snapshot_freshness: input.unavailable ? "unavailable-or-stale" : "last-successful-read-not-live-proof",
    write_blocked: input.snapshot?.write_blocked === true, uncertain_session: input.uncertain === true,
    pending: pending ? { ...operation(pending), state: pendingState(pending.state) } : null, recent_operations: history.slice(0, 25).map(operation),
    note: "Diagnostic summary only. Matching current settings does not prove an unknown write failed or succeeded. Preserve the authoritative journal; do not clear, replay or bypass ownership checks." };
}
