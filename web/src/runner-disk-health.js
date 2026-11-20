const maxCounter = 9223372036854775807n;
const poolName = /^[A-Za-z0-9][A-Za-z0-9-]{0,62}$/;
const utcTime = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/;
const value = raw => typeof raw === "string" && /^(0|[1-9]\d{0,18})$/.test(raw) && BigInt(raw) <= maxCounter ? BigInt(raw) : null;
const time = raw => {
  if (typeof raw !== "string" || !utcTime.test(raw) || raw.startsWith("0000")) return NaN;
  const ms = Date.parse(raw);
  // Date.parse can roll Feb 30 or 24:00 into the next day. Check the
  // calendar prefix round-trips, preserving accepted sub-ms precision on wire.
  return Number.isFinite(ms) && new Date(ms).toISOString().slice(0, 19) === raw.slice(0, 19) ? ms : NaN;
};
const metricFields = ["total_bytes", "available_bytes", "total_inodes", "available_inodes"];

// Reject an unknown shape without disconnecting the rest of the overview.
// Current counts may expire in an already-open tab. Stale data is never used
// as a pressure value, and a server-reported stale state cannot become current.
export function runnerDiskHealthRows(input, now) {
  if (!Array.isArray(input) || input.length > 8 || !Number.isFinite(now)) return [];
  const rows = [];
  const seen = new Set();
  for (const row of input) {
    if (!row || typeof row !== "object" || Array.isArray(row) || typeof row.storage_pool !== "string" ||
        !poolName.test(row.storage_pool) || seen.has(row.storage_pool) || !["current", "stale"].includes(row.state)) return [];
    const observed = time(row.observed_at);
    const expires = time(row.expires_at);
    if (!Number.isFinite(observed) || !Number.isFinite(expires) || observed > now || expires <= observed || expires - observed > 300000) return [];
    seen.add(row.storage_pool);
    if (row.state === "stale") {
      if (metricFields.some(k => Object.hasOwn(row, k))) return [];
      rows.push({ pool: row.storage_pool, observed, expires, state: "stale" });
      continue;
    }
    const [totalBytes, availableBytes, totalInodes, availableInodes] = metricFields.map(k => value(row[k]));
    if (totalBytes === null || totalBytes === 0n || availableBytes === null || availableBytes > totalBytes ||
        totalInodes === null || totalInodes === 0n || availableInodes === null || availableInodes > totalInodes) return [];
    if (expires <= now) {
      rows.push({ pool: row.storage_pool, observed, expires, state: "stale" });
      continue;
    }
    rows.push({ pool: row.storage_pool, observed, expires, state: "current", totalBytes, availableBytes, totalInodes, availableInodes });
  }
  return rows;
}
