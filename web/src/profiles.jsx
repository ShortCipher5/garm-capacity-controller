import React, { useEffect, useRef, useState } from "react";
import { fetchProfiles, upsertProfile, deleteProfile, validateProfileInput } from "./profiles-client.js";

const fmt = (n) => n.toLocaleString();
const date = (value) => new Date(value).toLocaleString();
export function ProfileEstimates({ estimates }) {
  if (!Array.isArray(estimates) || estimates.length === 0)
    return <span className="muted">No enabled hosts — estimates cover enabled hosts only.</span>;
  return (
    <ul className="estimate-list">
      {estimates.map((entry) => (
        <li key={entry.host}>
          ~{fmt(entry.estimate)} runners fit on {entry.host}
          {entry.estimate === 0 ? " (no remaining budget)" : ""}
        </li>
      ))}
    </ul>
  );
}
export function Profiles({ readProfiles }) {
  const [view, setView] = useState(null);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState(null); // null | {name, cpu_millis, ram_mib, disk_mib} (name "" = create)
  const [confirming, setConfirming] = useState("");
  const mounted = useRef(true), generation = useRef(0), active = useRef(null), writeToken = useRef(null), deleteToken = useRef(null);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; generation.current++; active.current?.abort(); if (writeToken.current) writeToken.current.value = ""; if (deleteToken.current) deleteToken.current.value = ""; };
  }, []);
  async function load() {
    if (active.current) return;
    const controller = new AbortController(); active.current = controller;
    const current = ++generation.current;
    const timeout = setTimeout(() => controller.abort(), 15000);
    setBusy(true); setMessage("");
    try {
      const next = await readProfiles({ signal: controller.signal });
      if (mounted.current && current === generation.current) setView(next);
    } catch (error) {
      if (mounted.current && current === generation.current) setMessage(error.message);
    } finally {
      clearTimeout(timeout); active.current = null;
      if (mounted.current && current === generation.current) setBusy(false);
    }
  }
  useEffect(() => { void load(); }, []);
  async function save(event) {
    event.preventDefault();
    if (active.current) return;
    const form = new FormData(event.currentTarget);
    const credential = writeToken.current?.value || "";
    let profile;
    try {
      // Disabled fields (the immutable name on edit) drop out of FormData;
      // the reviewed edit target is the editing identity itself.
      profile = validateProfileInput({ name: form.get("name") ?? editing?.name, cpuVcpus: form.get("cpu"), ramMiB: form.get("ram"), diskMiB: form.get("disk") });
    } catch (error) { setMessage(error.message); return; }
    const controller = new AbortController(); active.current = controller;
    const current = ++generation.current;
    const timeout = setTimeout(() => controller.abort(), 15000);
    setBusy(true); setMessage("");
    try {
      await upsertProfile(credential, profile, { signal: controller.signal });
      if (mounted.current && current === generation.current) {
        active.current = null;
        clearTimeout(timeout);
        if (writeToken.current) writeToken.current.value = "";
        setEditing(null);
        await load();
        // load() clears status lines and bumps the generation guard at start,
        // so the confirmation renders unconditionally afterwards.
        if (mounted.current) setMessage(`Runner-size profile ${profile.name} saved. Pool policies referencing it use it at next dispatch.`);
      }
    } catch (error) {
      if (mounted.current && current === generation.current) setMessage(error.message);
    } finally {
      clearTimeout(timeout);
      if (active.current === controller) active.current = null;
      if (writeToken.current) writeToken.current.value = "";
      if (mounted.current && current === generation.current) setBusy(false);
    }
  }
  async function remove(event) {
    event.preventDefault();
    if (active.current || !confirming) return;
    const credential = deleteToken.current?.value || "";
    const controller = new AbortController(); active.current = controller;
    const current = ++generation.current;
    const timeout = setTimeout(() => controller.abort(), 15000);
    setBusy(true); setMessage("");
    try {
      await deleteProfile(credential, confirming, { signal: controller.signal });
      if (mounted.current && current === generation.current) {
        const removed = confirming;
        active.current = null;
        clearTimeout(timeout);
        if (deleteToken.current) deleteToken.current.value = "";
        setConfirming("");
        await load();
        // load() clears status lines and bumps the generation guard at start,
        // so the confirmation renders unconditionally afterwards.
        if (mounted.current) setMessage(`Runner-size profile ${removed} deleted. Pool policies referencing it fail closed at next dispatch.`);
      }
    } catch (error) {
      if (mounted.current && current === generation.current) setMessage(error.message);
    } finally {
      clearTimeout(timeout);
      if (active.current === controller) active.current = null;
      if (deleteToken.current) deleteToken.current.value = "";
      if (mounted.current && current === generation.current) setBusy(false);
    }
  }
  return (
    <section id="profiles">
      <div className="section-heading">
        <h2>Runner-size profiles <small>{view ? view.profiles.length : "…"}</small></h2>
        <span className="muted">Per-runner sizing used for host fit estimates</span>
      </div>
      <div className="card management" id="profile-management">
        <h2>Runner-size profile management</h2>
        <p>Saved sizes estimate how many runners fit each enabled host's remaining budget. Estimates use the same dimension math as pool admission; a zero disk size ignores the disk dimension.</p>
        <p className="muted">Gateway pool policy configuration is not exposed by this admin API, so pools referencing a profile by name are not listed here. Deleting a profile does not edit pool policies: they fail closed at next dispatch.</p>
        <div className="profile-toolbar">
          <button className="quiet" type="button" disabled={busy} onClick={load}>{busy ? "Working…" : "Refresh profiles"}</button>
          <button className="quiet" type="button" disabled={busy} onClick={() => { setEditing({ name: "", cpu_millis: 0, ram_mib: 0, disk_mib: 0 }); setConfirming(""); setMessage(""); }}>New profile</button>
        </div>
        {view && (
          view.profiles.length === 0 ? (
            <p className="empty">No runner-size profiles saved. Estimates need at least one profile.</p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr><th>Profile</th><th>CPU</th><th>Memory</th><th>Disk</th><th>Fits on enabled hosts</th><th>Updated</th><th>Actions</th></tr>
                </thead>
                <tbody>
                  {view.profiles.map((profile) => (
                    <tr key={profile.name}>
                      <td><strong>{profile.name}</strong></td>
                      <td>{fmt(profile.cpu_millis / 1000)} vCPU</td>
                      <td>{fmt(profile.ram_mib)} MiB</td>
                      <td>{profile.disk_mib === 0 ? "ignored" : `${fmt(profile.disk_mib)} MiB`}</td>
                      <td><ProfileEstimates estimates={profile.estimates} /></td>
                      <td className="muted">{date(profile.updated_at)}</td>
                      <td>
                        <button className="quiet" type="button" disabled={busy} onClick={() => { setEditing(profile); setConfirming(""); setMessage(""); }}>Edit</button>{" "}
                        {confirming === profile.name ? (
                          <form className="inline-confirm" onSubmit={remove}>
                            <label>Deletion management token<input aria-label="Deletion management token" ref={deleteToken} type="password" autoComplete="off" spellCheck={false} required disabled={busy} /></label>
                            <button className="primary" type="submit" disabled={busy}>{busy ? "Working…" : `Confirm delete ${profile.name}`}</button>
                            <button className="quiet" type="button" disabled={busy} onClick={() => { setConfirming(""); if (deleteToken.current) deleteToken.current.value = ""; }}>Cancel</button>
                          </form>
                        ) : (
                          <button className="quiet" type="button" disabled={busy} onClick={() => { setConfirming(profile.name); setEditing(null); setMessage(""); }}>Delete</button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        )}
        {editing && (
          <form className="profile-form" onSubmit={save}>
            <h3>{editing.name ? `Edit profile ${editing.name}` : "New runner-size profile"}</h3>
            <label>Profile name
              <input aria-label="Profile name" name="name" defaultValue={editing.name} maxLength={64} required disabled={Boolean(editing.name)} title="1–64 lowercase letters, digits or hyphens" />
            </label>
            <div className="budget-fields">
              <label>CPU (vCPUs)<input aria-label="CPU (vCPUs)" name="cpu" type="number" min="1" max={Number.MAX_SAFE_INTEGER / 1000} step="1" defaultValue={editing.name ? editing.cpu_millis / 1000 : ""} required /></label>
              <label>Memory (MiB)<input aria-label="Memory (MiB)" name="ram" type="number" min="1" max={Number.MAX_SAFE_INTEGER} step="1" defaultValue={editing.name ? editing.ram_mib : ""} required /></label>
              <label>Disk (MiB)<input aria-label="Disk (MiB)" name="disk" type="number" min="0" max={Number.MAX_SAFE_INTEGER} step="1" defaultValue={editing.name ? editing.disk_mib : 0} required /></label>
            </div>
            <label>Management write token<input aria-label="Profile management write token" ref={writeToken} type="password" autoComplete="off" spellCheck={false} required disabled={busy} /></label>
            <p className="muted">Used once and cleared on submission. Saving replaces the size for this name; it never changes pool policies.</p>
            <button className="primary" type="submit" disabled={busy}>{busy ? "Saving…" : "Save profile"}</button>
            <button className="quiet" type="button" disabled={busy} onClick={() => { setEditing(null); setMessage(""); }}>Cancel</button>
          </form>
        )}
        {message && <p role="status">{message}</p>}
      </div>
    </section>
  );
}
