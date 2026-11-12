// Snapshot-local selectors: never fetch, mutate the source, or infer live state.
export function filterAuditEvents(events, kind = "", host = "") {
  return events.filter((event) => (!kind || event.kind === kind) && (!host || event.host === host))
    .sort((a, b) => b.sequence - a.sequence);
}
export function auditFilterOptions(events, field) {
  return [...new Set(events.map((event) => event[field]).filter(Boolean))].sort();
}
