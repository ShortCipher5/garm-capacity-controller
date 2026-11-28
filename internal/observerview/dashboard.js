"use strict";

const byID = (id) => document.getElementById(id);
const textNode = (kind, text) => {
  const item = document.createElement(kind);
  item.textContent = String(text);
  return item;
};
const value = (n, unit, factor = 1) =>
  typeof n === "number" && Number.isFinite(n) && n >= 0
    ? `${(n / factor).toLocaleString(undefined, { maximumFractionDigits: 1 })} ${unit}`
    : "unobserved";

let loaded = false;
byID("token").addEventListener("input", () => { loaded = false; byID("snapshot").hidden = true; });
setInterval(() => {
  if (loaded && !document.hidden && byID("auto").checked && !byID("refresh").disabled) byID("refresh").click();
}, 30000);
byID("refresh").addEventListener("click", async () => {
  const token = byID("token").value.trim();
  if (!/^[a-f0-9]{64}$/.test(token)) {
    byID("status").textContent = "Enter the 64-character dashboard token.";
    return;
  }
  byID("refresh").disabled = true;
  byID("snapshot").hidden = true;
  byID("status").textContent = "Loading current observations…";
  try {
    const result = await fetch("/admin/api/v1/observer", {
      headers: { Authorization: `Bearer ${token}` },
      cache: "no-store",
      redirect: "error",
      credentials: "omit",
    });
    if (result.status === 401) throw new Error("Dashboard token was not accepted.");
    if (result.status !== 200 && result.status !== 503) throw new Error("Observation is unavailable. Try again.");
    const observed = await result.json();
    if (observed.mode !== "observation_only") throw new Error("Unexpected observation response.");
    byID("status").textContent = `Observed at ${new Date(observed.observed_at).toLocaleString()}.`;
    byID("pools").replaceChildren();
    byID("hosts").replaceChildren();
    byID("queue").textContent = observed.stock
      ? `${observed.stock.jobs?.length ?? 0} queued jobs reported by GARM. This feed does not prove webhook completeness.`
      : "GARM data is unavailable.";
    for (const p of observed.stock?.supply ?? []) {
      const row = document.createElement("tr");
      for (const cell of [p.pool_id, p.idle, p.creating, p.busy, p.other]) row.append(textNode("td", cell));
      byID("pools").append(row);
    }
    byID("headroom").replaceChildren();
    for (const row of observed.whole_hosts ?? []) {
      const s = row.sample;
      const at = row.last_observed_at ? new Date(row.last_observed_at).toLocaleString() : "never";
      const usable = s && ["observed", "partial"].includes(row.status);
      const details = usable ? `CPU idle ${value(s.cpu_idle_basis_points, "%", 100)} (non-idle incl. wait/steal: ${value(s.cpu_idle_basis_points == null ? null : 10000 - s.cpu_idle_basis_points, "%", 100)}); RAM available ${value(s.available_ram_mib, "MiB")} of ${value(s.ram_mib, "MiB")}; / available ${value(s.root_available_mib, "MiB")} of ${value(s.root_disk_mib, "MiB")}.` : "No current measurement.";
      byID("headroom").append(textNode("p", `${row.host}: ${row.status}. Last observation: ${at}. ${details} Runner storage pool: unavailable (no verified pool binding).`));
    }
    if (!observed.whole_hosts?.length) byID("headroom").append(textNode("p", "No whole-server source configured."));
    for (const host of observed.hosts ?? []) {
      byID("hosts").append(textNode("h3", host.host));
      if (host.status !== "running_guest_telemetry_only" || !host.guests?.length) {
        byID("hosts").append(textNode("p", host.status === "unavailable" ? "Telemetry unavailable." : "No guest telemetry observed."));
        continue;
      }
      const list = document.createElement("ul");
      for (const g of host.guests) list.append(textNode("li", `${g.name}: CPU ${value(g.cpu_used_seconds, "s")}; memory ${value(g.memory_used_bytes, "MiB", 1048576)}`));
      byID("hosts").append(list);
    }
    if (!observed.hosts?.length) byID("hosts").append(textNode("p", "No Incus telemetry source configured."));
    loaded = true;
    byID("snapshot").hidden = false;
  } catch (error) {
    loaded = false;
    byID("status").textContent = error instanceof Error ? error.message : "Observation is unavailable.";
  } finally {
    byID("refresh").disabled = false;
  }
});
