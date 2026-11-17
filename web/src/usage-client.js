// Measured host utilization history client. Monitoring data only: an empty
// history means unavailable, never a zero-filled series, and missing samples
// stay gaps. This client has no persistence and no configurable endpoint.
export class UsageResponseError extends Error {}
const object = (value) => value && typeof value === "object" && !Array.isArray(value);
const integer = (value) => Number.isSafeInteger(value) && value >= 0;
export function validateUsageHistory(value) {
  if (
    !Array.isArray(value) ||
    !value.every(
      (sample) =>
        object(sample) &&
        typeof sample.host === "string" &&
        sample.host.length > 0 &&
        Number.isFinite(Date.parse(sample.sampled_at)) &&
        integer(sample.cpu_used_millis) &&
        integer(sample.ram_used_mib) &&
        integer(sample.disk_used_mib) &&
        typeof sample.source === "string" &&
        sample.source.length > 0,
    )
  ) {
    throw new UsageResponseError("Unsupported usage history. No chart was drawn from invented values.");
  }
  return value;
}
async function serverError(response, fallback) {
  try {
    const body = await response.json();
    if (object(body) && typeof body.error === "string" && body.error) return body.error;
  } catch {}
  return fallback;
}
export async function fetchUsageHistory(token, host, { since, limit, signal, fetcher = globalThis.fetch } = {}) {
  if (!/^[0-9a-f]{64}$/.test(token)) throw new UsageResponseError("Enter the 64-character lowercase hexadecimal read token.");
  const query = new URLSearchParams();
  if (since !== undefined) query.set("since", since instanceof Date ? since.toISOString() : String(since));
  if (limit !== undefined) query.set("limit", String(limit));
  const suffix = query.size ? `?${query}` : "";
  let response;
  try {
    response = await fetcher.call(globalThis, `/admin/api/v1/usage/${encodeURIComponent(host)}${suffix}`, {
      method: "GET",
      headers: { Authorization: `Bearer ${token}`, Accept: "application/json" },
      credentials: "omit",
      cache: "no-store",
      redirect: "error",
      signal,
    });
  } catch {
    throw new UsageResponseError("Usage history is unavailable. No automatic retry was made.");
  }
  if (response.status === 401) throw new UsageResponseError("Read token rejected. Reconnect with a valid token.");
  if (response.status === 400) throw new UsageResponseError(await serverError(response, "Invalid usage history request."));
  if (!response.ok) throw new UsageResponseError(await serverError(response, "Usage history is unavailable. No automatic retry was made."));
  try {
    return validateUsageHistory(await response.json());
  } catch {
    throw new UsageResponseError("Unsupported usage history. No chart was drawn from invented values.");
  }
}
// splitUsageSegments groups ascending samples into contiguous line segments.
// A sampling interval wider than twice the median interval is a monitoring
// gap: the line breaks there instead of bridging it with fabricated points.
export function splitUsageSegments(samples) {
  if (!Array.isArray(samples) || samples.length < 3) return samples && samples.length ? [samples.slice()] : [];
  const ordered = samples.slice().sort((a, b) => Date.parse(a.sampled_at) - Date.parse(b.sampled_at));
  const intervals = [];
  for (let index = 1; index < ordered.length; index++) intervals.push(Date.parse(ordered[index].sampled_at) - Date.parse(ordered[index - 1].sampled_at));
  const median = intervals.slice().sort((a, b) => a - b)[Math.floor(intervals.length / 2)];
  if (!(median > 0)) return [ordered];
  const segments = [[ordered[0]]];
  for (let index = 1; index < ordered.length; index++) {
    if (Date.parse(ordered[index].sampled_at) - Date.parse(ordered[index - 1].sampled_at) > 2 * median) segments.push([]);
    segments[segments.length - 1].push(ordered[index]);
  }
  return segments;
}
