// Recorded resource observations client. Monitoring data only: an empty
// history means unavailable, never a zero-filled series, and missing samples
// stay gaps. This client has no persistence and no configurable endpoint.
export class UsageResponseError extends Error {}
const object = (value) => value && typeof value === "object" && !Array.isArray(value);
const integer = (value) => Number.isSafeInteger(value) && value >= 0;
// The server emits Go time.Time JSON (RFC3339 with up to nine fractional
// digits). Date.parse alone coerces other JSON types and rolls invalid dates
// such as February 30 into a different observation day.
const usageTimestamp = /^(\d{4})-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;
function validUsageTimestamp(value) {
  if (typeof value !== "string") return false;
  const match = usageTimestamp.exec(value);
  if (!match || match[0] !== value) return false;
  const [, yearText, monthText, dayText] = match;
  const year = Number(yearText), month = Number(monthText), day = Number(dayText);
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const lastDay = month === 2 ? (leap ? 29 : 28) : ([4, 6, 9, 11].includes(month) ? 30 : 31);
  return day <= lastDay;
}

export function validateUsageHistory(value, expectedHost) {
  if (
    !Array.isArray(value) ||
    !value.every(
      (sample) =>
        object(sample) &&
        typeof sample.host === "string" &&
        sample.host.length > 0 &&
        (expectedHost === undefined || sample.host === expectedHost) &&
        validUsageTimestamp(sample.sampled_at) &&
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
    return validateUsageHistory(await response.json(), host);
  } catch {
    throw new UsageResponseError("Unsupported usage history. No chart was drawn from invented values.");
  }
}
export function usageScope(samples) {
  if (!samples?.length) return "Observation scope unavailable until samples are returned.";
  const sources = [...new Set(samples.map(sample => sample.source))];
  if (sources.every(source => source === "guest-state" || source === "guest-state-v2")) {
    const legacy = sources.includes("guest-state") ? " A stored zero in legacy history is not proof of zero usage." : "";
    return "Project guest observations, not whole-host load. These values do not establish physical host pressure or headroom." + legacy;
  }
  return "Source-specific resource observations, not verified whole-host load. Different sources may have different scopes; points are not connected across observations.";
}
