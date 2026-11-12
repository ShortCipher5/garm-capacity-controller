import React, { useState } from "react";
import { auditFilterOptions, filterAuditEvents } from "./audit-events.js";

export function AuditEvents({ events }) {
  const [kind, setKind] = useState("");
  const [host, setHost] = useState("");
  const kinds = auditFilterOptions(events, "kind");
  const hosts = auditFilterOptions(events, "host");
  const matches = filterAuditEvents(events, kind, host);
  return <section id="events">
    <div className="section-heading"><h2>Audit events</h2><span className="muted">Ledger records, newest first</span></div>
    <div className="card decisions">
      <div className="audit-filters">
        <label>Event kind<select value={kind} onChange={(e) => setKind(e.target.value)}>
          <option value="">All kinds</option>
          {kind && !kinds.includes(kind) && <option value={kind}>{kind} (not in snapshot)</option>}
          {kinds.map((value) => <option key={value} value={value}>{value}</option>)}
        </select></label>
        <label>Event host<select value={host} onChange={(e) => setHost(e.target.value)}>
          <option value="">All hosts (including unassigned)</option>
          {host && !hosts.includes(host) && <option value={host}>{host} (not in snapshot)</option>}
          {hosts.map((value) => <option key={value} value={value}>{value}</option>)}
        </select></label>
        <button className="quiet" disabled={!kind && !host} onClick={() => { setKind(""); setHost(""); }}>Clear filters</button>
      </div>
      <p role="status">Showing {matches.length} of {events.length} events in this snapshot</p>
      {matches.map((event) => <article className="event" key={event.sequence}>
        <span className="pill">#{event.sequence}</span>
        <div><strong>{event.kind}</strong><time dateTime={event.at}>{new Date(event.at).toLocaleString()}</time>
          {event.host && <p>Host: {event.host}</p>}
          {event.request_key && <p>{event.kind === "admin_policy_change" ? "Policy audit: " : event.kind.startsWith("onboarding_probe_") ? "Onboarding audit: " : "Request: "}{event.request_key}</p>}
        </div>
      </article>)}
      {!matches.length && <p className="empty">{events.length ? "No audit events match these filters." : "No audit events recorded."}</p>}
    </div>
  </section>;
}
