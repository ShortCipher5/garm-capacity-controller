// Read-only onboarding discovery and explicit host enrollment clients.
// Discovery requires the separate management credential; it suggests but never
// applies a budget. Enrollment is the only write here and is operator-reviewed:
// the initial mode is forced to disabled or draining. Error bodies
// {"error": ...} are rendered as returned.
import { ProfileResponseError } from "./profiles-client.js";
export class DiscoveryResponseError extends Error {}
const object = (value) => value && typeof value === "object" && !Array.isArray(value);
const integer = (value) => Number.isSafeInteger(value) && value >= 0;
const positive = (value) => Number.isSafeInteger(value) && value > 0;
const optionalPositive = (value) => value === undefined || positive(value);
const time = (value) => typeof value === "string" && Number.isFinite(Date.parse(value));
const profileID = (value) => typeof value === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(value);
const validProposal = (value) => object(value) && value.version === 1 && object(value.budget) && object(value.reserve)
  && [value.budget,value.reserve].every(values => Object.entries(values).every(([key,n]) => ["cpu_millis","ram_mib","disk_mib"].includes(key) && integer(n)))
  && value.budget.disk_mib !== 0
  && Array.isArray(value.warnings) && value.warnings.every(w => typeof w === "string")
  && ["saved-profiles","illustrative-presets","unavailable"].includes(value.size_source)
  && Array.isArray(value.sizes) && value.sizes.length <= 512
  && value.sizes.every(s => object(s) && typeof s.name === "string" && /^[a-z0-9-]{1,64}$/.test(s.name) && positive(s.cpu_millis) && positive(s.ram_mib) && integer(s.disk_mib));
const bindingClasses = ["not_checked", "match_observed", "no_match_observed"];
export function validateDiscoveryResult(value) {
  if (
    !object(value) ||
    !profileID(value.profile_id) ||
    (value.proposal !== undefined && !validProposal(value.proposal)) ||
    !time(value.observed_at) ||
    !object(value.identity) ||
    typeof value.identity.observation_available !== "boolean" ||
    (value.identity.server_version !== undefined && (typeof value.identity.server_version !== "string" || !/^[A-Za-z0-9._+-]{1,64}$/.test(value.identity.server_version))) ||
    !bindingClasses.includes(value.identity.stored_binding_class) ||
    (value.totals !== undefined &&
      (!object(value.totals) ||
        typeof value.totals.complete !== "boolean" ||
        !optionalPositive(value.totals.cpu_millis) ||
        !optionalPositive(value.totals.ram_mib) ||
        !optionalPositive(value.totals.disk_mib) ||
        !optionalPositive(value.totals.disk_mib_used) ||
        (value.totals.storage_pool !== undefined && typeof value.totals.storage_pool !== "string") ||
        !optionalPositive(value.totals.storage_pool_disk_mib) ||
        !optionalPositive(value.totals.storage_pool_disk_mib_used))) ||
    !Array.isArray(value.guests) ||
    value.guests.length > 512 ||
    !value.guests.every(
      (guest) =>
        object(guest) &&
        typeof guest.name === "string" &&
        guest.name.length > 0 &&
        typeof guest.status === "string" &&
        typeof guest.managed === "boolean" &&
        (guest.controller === undefined || typeof guest.controller === "string") &&
        (guest.pool === undefined || typeof guest.pool === "string") &&
        (guest.reservation === undefined || typeof guest.reservation === "string") &&
        ["cpu_millis", "ram_mib", "root_disk_mib"].every((key) => guest[key] === undefined || positive(guest[key])),
    ) ||
    (value.utilization !== undefined &&
      (!object(value.utilization) ||
        typeof value.utilization.source !== "string" ||
        !integer(value.utilization.cpu_cumulative_millis) ||
        !integer(value.utilization.ram_used_mib) ||
        !integer(value.utilization.disk_used_mib) ||
        !integer(value.utilization.guests) ||
        (value.utilization.errors !== undefined && (!Array.isArray(value.utilization.errors) || !value.utilization.errors.every((entry) => typeof entry === "string"))))) ||
    !object(value.suggested_budget) ||
    !["cpu_millis", "ram_mib", "disk_mib", "runners"].every((key) => integer(value.suggested_budget[key])) ||
    !Array.isArray(value.warnings) ||
    !value.warnings.every((entry) => typeof entry === "string") ||
    !Array.isArray(value.blockers) ||
    !value.blockers.every((entry) => typeof entry === "string") ||
    !value.blockers.includes("physical_capacity_scope_unqualified")
  ) {
    throw new DiscoveryResponseError("Unsupported discovery observation. Nothing was enrolled or suggested by this view.");
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
export async function fetchProfileDiscovery(token, profile, { signal, fetcher = globalThis.fetch } = {}) {
  if (!/^[0-9a-f]{64}$/.test(token)) throw new DiscoveryResponseError("Enter the 64-character lowercase hexadecimal management token.");
  if (!profileID(profile)) throw new DiscoveryResponseError("Choose an allowlisted onboarding profile.");
  let response;
  try {
    response = await fetcher.call(globalThis, `/admin/api/v1/onboarding/profiles/${encodeURIComponent(profile)}/discovery`, {
      method: "GET",
      headers: { Authorization: `Bearer ${token}`, Accept: "application/json" },
      credentials: "omit",
      cache: "no-store",
      redirect: "error",
      signal,
    });
  } catch {
    throw new DiscoveryResponseError("Discovery outcome unknown. No enrollment was performed. No automatic retry was made.");
  }
  if (response.status === 401) throw new DiscoveryResponseError("Management token rejected. The read token cannot authorize discovery.");
  if (response.status === 403) throw new DiscoveryResponseError(await serverError(response, "Same origin required. This console only reads its own admin API."));
  if (response.status === 404) throw new DiscoveryResponseError(await serverError(response, "Onboarding discovery is unavailable for this profile."));
  if (response.status === 409) throw new DiscoveryResponseError(await serverError(response, "Another onboarding discovery is in progress."));
  if (!response.ok) throw new DiscoveryResponseError(await serverError(response, "Discovery outcome unknown. No enrollment was performed."));
  try {
    return validateDiscoveryResult(await response.json());
  } catch {
    throw new DiscoveryResponseError("Unsupported discovery observation. Nothing was enrolled or suggested by this view.");
  }
}
// enrollHost posts one reviewed explicit enrollment. The server rejects
// duplicate names, enabled initial modes and invalid budgets; those durable
// outcomes are rendered as returned. Enrollment never adopts an existing host.
export async function enrollHost(token, request, { signal, fetcher = globalThis.fetch } = {}) {
  if (!/^[0-9a-f]{64}$/.test(token)) throw new ProfileResponseError("Enter the 64-character lowercase hexadecimal management token.");
  const { name, budget, review, detected, mode } = request || {};
  if (typeof name !== "string" || name.length < 1 || name.length > 256 || /[\u0000-\u001f\u007f]/.test(name)) throw new ProfileResponseError("Host names use 1–256 characters without control characters.");
  if (!object(budget) || !["cpu_millis", "ram_mib", "disk_mib", "runners"].every((key) => integer(budget[key]))) throw new ProfileResponseError("Budgets must be non-negative safe integers.");
  if (detected !== undefined) throw new ProfileResponseError("Detected totals must come from a server discovery review.");
  if (review !== undefined && (!object(review) || !/^[0-9a-f-]{36}$/.test(review.id) || !/^[0-9a-f]{64}$/.test(review.revision) || !profileID(review.profile_id))) throw new ProfileResponseError("Refresh discovery for a valid review.");
  if (mode !== "disabled" && mode !== "draining") throw new ProfileResponseError("Initial mode must be disabled or draining. Enabling is a separate action after qualification.");
  let response;
  try {
    response = await fetcher.call(globalThis, "/admin/api/v1/hosts", {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json", Accept: "application/json" },
      body: JSON.stringify({ name, budget, ...(review ? { review } : {}), mode }),
      credentials: "omit",
      cache: "no-store",
      redirect: "error",
      signal,
    });
  } catch {
    throw new ProfileResponseError("Enrollment outcome unknown. Refresh the snapshot before attempting another enrollment. No automatic retry was made.");
  }
  if (response.status === 401) throw new ProfileResponseError("Management token rejected. The read token cannot authorize enrollment.");
  if (response.status === 403) throw new ProfileResponseError(await serverError(response, "Same origin required. This console only writes to its own admin API."));
  if (response.status === 409) throw new ProfileResponseError(await serverError(response, "Host already enrolled. Enrollment never rewrites an existing host."));
  if (response.status === 400) throw new ProfileResponseError(await serverError(response, "Invalid host enrollment. No host was enrolled."));
  if (!response.ok) throw new ProfileResponseError(await serverError(response, "Enrollment outcome unknown. Refresh the snapshot before attempting another enrollment."));
}
