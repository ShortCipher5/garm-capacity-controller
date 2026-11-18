const safe = (value, maximum = 256) => typeof value === "string" && value.length <= maximum && !/[\u0000-\u001f\u007f]/.test(value) ? value : "";

export function writerRecoveryState({ conflict = false, unavailable = false, uncertain = false, snapshot = null } = {}) {
  if (uncertain || snapshot?.write_blocked === true) return "unknown-write";
  if (conflict) return "external-change";
  if (unavailable) return "unavailable";
  return null;
}

// Explicit allowlist only: never export credentials, raw errors, arbitrary
// server fields, audit reasons, settings payloads or TLS material.
export function writerRecoveryReport(input = {}, now = new Date()) {
  const state = writerRecoveryState(input);
  if (!state) return null;
  const pending = input.snapshot?.pending;
  const history = Array.isArray(input.snapshot?.history) ? input.snapshot.history : [];
  const operation = value => ({ request_id: safe(value?.request_id), kind: safe(value?.kind, 32), target: safe(value?.target), state: safe(value?.state, 32), created_at: safe(value?.created_at, 64), started_at: safe(value?.started_at, 64) });
  return { format: "garm-writer-recovery/v1", captured_at: now.toISOString(), state,
    snapshot_freshness: input.unavailable ? "unavailable-or-stale" : "last-successful-read-not-live-proof",
    write_blocked: input.snapshot?.write_blocked === true, uncertain_session: input.uncertain === true,
    pending: pending ? operation(pending) : null, recent_operations: history.slice(0, 25).map(operation),
    note: "Diagnostic summary only. Matching current settings does not prove an unknown write failed or succeeded. Preserve the authoritative journal; do not clear, replay or bypass ownership checks." };
}
