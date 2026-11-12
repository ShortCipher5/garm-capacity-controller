export class OnboardingResponseError extends Error {}
const object = (value) => value && typeof value === "object" && !Array.isArray(value);
const profileID = (value) => typeof value === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(value);
const requestID = (value) => typeof value === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(value);
const classes = ["not_checked", "match_observed", "no_match_observed"];
const blockers = ["enrollment_not_implemented", "physical_capacity_scope_unqualified", "stored_binding_match", "stored_binding_comparison_unavailable", "authenticated_identity_unavailable"];
export function validateOnboardingDiscovery(value) {
  if (!object(value) || value.mode !== "read-only-onboarding-preflight" || typeof value.probe_enabled !== "boolean" || value.enrollment_supported !== false || value.capacity_qualified !== false || !Array.isArray(value.profiles) || value.profiles.length > 32 || !value.profiles.every((profile) => object(profile) && profileID(profile.id)) || new Set(value.profiles.map((profile) => profile.id)).size !== value.profiles.length) {
    throw new OnboardingResponseError("Unsupported onboarding preflight response. No enrollment controls are available.");
  }
  return value;
}
export function validateOnboardingResult(value) {
  if (!object(value) || value.mode !== "read-only-onboarding-preflight" || !requestID(value.request_id) || !profileID(value.profile_id) || !Number.isFinite(Date.parse(value.observed_at)) || typeof value.observation_available !== "boolean" || !classes.includes(value.stored_binding_class) || value.enrollment_performed !== false || value.activation_performed !== false || value.capacity_qualified !== false || !Array.isArray(value.blockers) || !value.blockers.every((reason) => blockers.includes(reason)) || !value.blockers.includes("enrollment_not_implemented") || !value.blockers.includes("physical_capacity_scope_unqualified") || (value.observation_available && (typeof value.server_version !== "string" || value.server_version.length < 1 || !/^[A-Za-z0-9._+-]{1,64}$/.test(value.server_version))) || (!value.observation_available && value.server_version !== undefined && value.server_version !== "")) {
    throw new OnboardingResponseError("Unsupported preflight result. A probe cannot qualify or activate a backend.");
  }
  return value;
}
export async function probeOnboarding(token, request, { signal, fetcher = globalThis.fetch } = {}) {
  if (!/^[0-9a-f]{64}$/.test(token)) throw new OnboardingResponseError("Enter the separate 64-character management token.");
  let response;
  try {
    response = await fetcher.call(globalThis, "/admin/api/v1/onboarding/probe", {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json", Accept: "application/json" },
      body: JSON.stringify(request), credentials: "omit", cache: "no-store", redirect: "error", signal,
    });
  } catch {
    throw new OnboardingResponseError("The preflight response or audit is unavailable. This interface cannot enroll or activate a server. No automatic retry was made.");
  }
  if (response.status === 401) throw new OnboardingResponseError("Management token rejected. The read token cannot authorize a probe.");
  if (response.status === 400) throw new OnboardingResponseError("Invalid request or unconfigured preflight profile.");
  if (response.status === 404) throw new OnboardingResponseError("Onboarding probes are disabled on this server.");
  if (response.status === 409) throw new OnboardingResponseError("Another onboarding preflight is in progress.");
  if (!response.ok) throw new OnboardingResponseError("The preflight or its audit is unavailable. No enrollment or activation is implemented.");
  try {
    const result = validateOnboardingResult(await response.json());
    if (result.request_id !== request.request_id || result.profile_id !== request.profile_id) throw new OnboardingResponseError("Preflight result correlation rejected.");
    return result;
  }
  catch { throw new OnboardingResponseError("Unsupported preflight result. A probe cannot qualify or activate a backend."); }
}
