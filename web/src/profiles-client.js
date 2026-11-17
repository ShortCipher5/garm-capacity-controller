// Runner-size profile client: list, upsert and delete. Listing accepts the
// read credential and embeds per-enabled-host fit estimates; writes require
// the separate management credential like every other policy surface. Error
// bodies {"error": ...} are rendered as returned.
export class ProfileResponseError extends Error {}
const object = (value) => value && typeof value === "object" && !Array.isArray(value);
const integer = (value) => Number.isSafeInteger(value) && value >= 0;
export const profileNamePattern = /^[a-z0-9-]{1,64}$/;
export function validateProfilesList(value) {
  if (
    !object(value) ||
    !Array.isArray(value.profiles) ||
    !value.profiles.every(
      (profile) =>
        object(profile) &&
        profileNamePattern.test(profile.name) &&
        Number.isSafeInteger(profile.cpu_millis) &&
        profile.cpu_millis > 0 &&
        profile.cpu_millis % 1000 === 0 &&
        Number.isSafeInteger(profile.ram_mib) &&
        profile.ram_mib > 0 &&
        integer(profile.disk_mib) &&
        Number.isFinite(Date.parse(profile.created_at)) &&
        Number.isFinite(Date.parse(profile.updated_at)) &&
        Array.isArray(profile.estimates) &&
        profile.estimates.every(
          (estimate) => object(estimate) && typeof estimate.host === "string" && estimate.host.length > 0 && integer(estimate.estimate),
        ),
    ) ||
    new Set(value.profiles.map((profile) => profile.name)).size !== value.profiles.length
  ) {
    throw new ProfileResponseError("Unsupported runner-size profile list. No fit estimates were rendered.");
  }
  return value;
}
// validateProfileInput applies the admission store's durable rules to the
// operator form: whole vCPUs, positive MiB RAM, non-negative MiB disk (zero
// ignores the dimension) and the store's profile name alphabet.
export function validateProfileInput({ name, cpuVcpus, ramMiB, diskMiB }) {
  const clean = String(name ?? "").trim();
  if (!profileNamePattern.test(clean)) throw new ProfileResponseError("Profile names use 1–64 lowercase letters, digits or hyphens.");
  const vcpus = Number(cpuVcpus);
  if (!Number.isSafeInteger(vcpus) || vcpus <= 0) throw new ProfileResponseError("CPU must be a whole number of vCPUs, at least 1.");
  const ram = Number(ramMiB);
  if (!Number.isSafeInteger(ram) || ram <= 0) throw new ProfileResponseError("Memory must be a whole number of MiB, at least 1.");
  const disk = Number(diskMiB);
  if (!Number.isSafeInteger(disk) || disk < 0) throw new ProfileResponseError("Disk must be a whole number of MiB, 0 or more (0 ignores the disk dimension).");
  return { name: clean, cpu_millis: vcpus * 1000, ram_mib: ram, disk_mib: disk };
}
async function serverError(response, fallback) {
  try {
    const body = await response.json();
    if (object(body) && typeof body.error === "string" && body.error) return body.error;
  } catch {}
  return fallback;
}
async function request(token, method, path, body, { signal, fetcher = globalThis.fetch } = {}) {
  if (!/^[0-9a-f]{64}$/.test(token)) throw new ProfileResponseError(method === "GET" ? "Enter the 64-character lowercase hexadecimal read token." : "Enter the 64-character lowercase hexadecimal management token.");
  let response;
  try {
    response = await fetcher.call(globalThis, path, {
      method,
      headers: { Authorization: `Bearer ${token}`, ...(body ? { "Content-Type": "application/json" } : {}), Accept: "application/json" },
      ...(body ? { body: JSON.stringify(body) } : {}),
      credentials: "omit",
      cache: "no-store",
      redirect: "error",
      signal,
    });
  } catch {
    throw new ProfileResponseError("Profile outcome unknown. Refresh before attempting another change. No automatic retry was made.");
  }
  return response;
}
export async function fetchProfiles(token, options = {}) {
  const response = await request(token, "GET", "/admin/api/v1/profiles", null, options);
  if (response.status === 401) throw new ProfileResponseError("Read token rejected. Reconnect with a valid token.");
  if (!response.ok) throw new ProfileResponseError(await serverError(response, "Runner-size profiles are unavailable. No automatic retry was made."));
  try {
    return validateProfilesList(await response.json());
  } catch {
    throw new ProfileResponseError("Unsupported runner-size profile list. No fit estimates were rendered.");
  }
}
export async function upsertProfile(token, profile, options = {}) {
  const response = await request(token, "POST", "/admin/api/v1/profiles", profile, options);
  if (response.status === 401) throw new ProfileResponseError("Management token rejected. The read token cannot authorize profile writes.");
  if (response.status === 403) throw new ProfileResponseError(await serverError(response, "Same origin required. This console only writes to its own admin API."));
  if (response.status === 404) throw new ProfileResponseError(await serverError(response, "Profile management is unavailable on this server."));
  if (response.status === 400) throw new ProfileResponseError(await serverError(response, "Invalid runner-size profile. No change was applied."));
  if (!response.ok) throw new ProfileResponseError(await serverError(response, "Profile outcome unknown. Refresh before attempting another change. No automatic retry was made."));
}
export async function deleteProfile(token, name, options = {}) {
  const response = await request(token, "DELETE", `/admin/api/v1/profiles/${encodeURIComponent(name)}`, null, options);
  if (response.status === 401) throw new ProfileResponseError("Management token rejected. The read token cannot authorize profile writes.");
  if (response.status === 403) throw new ProfileResponseError(await serverError(response, "Same origin required. This console only writes to its own admin API."));
  if (response.status === 404) throw new ProfileResponseError(await serverError(response, "Runner-size profile unknown. No change was applied."));
  if (response.status === 400) throw new ProfileResponseError(await serverError(response, "Invalid runner-size profile name. No change was applied."));
  if (!response.ok) throw new ProfileResponseError(await serverError(response, "Profile outcome unknown. Refresh before attempting another change. No automatic retry was made."));
}
