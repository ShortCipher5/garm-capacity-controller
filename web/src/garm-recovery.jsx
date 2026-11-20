import React, { useState } from "react";
import { writerRecoveryEvidence, writerRecoveryEvidenceGuidance, writerRecoveryReport, writerRecoveryState } from "./garm-recovery.js";

export function GarmRecoveryPanel({ snapshot, conflict, unavailable, uncertain }) {
  const [message, setMessage] = useState("");
  const state = writerRecoveryState({ snapshot, conflict, unavailable, uncertain });
  if (!state) return null;
  const evidence = writerRecoveryEvidenceGuidance(writerRecoveryEvidence({ snapshot, unavailable, uncertain }));
  function download() {
    let url;
    try {
      const report = writerRecoveryReport({ snapshot, conflict, unavailable, uncertain });
      url = URL.createObjectURL(new Blob([JSON.stringify(report, null, 2)], { type: "application/json" }));
      const anchor = document.createElement("a"); anchor.href = url; anchor.download = "garm-writer-recovery.json";
      document.body.appendChild(anchor); anchor.click(); anchor.remove();
      setMessage("Diagnostic download requested. Keep the authoritative journal separately; this summary is not a backup or permission to retry.");
    } catch { setMessage("Diagnostic download unavailable. Preserve the request ID and authoritative journal; do not retry or clear an unknown write."); }
    finally { if (url) setTimeout(() => URL.revokeObjectURL(url), 1000); }
  }
  return <aside className="card" aria-label="Garm writer recovery">
    <h3>Restore safe settings ownership</h3>
    {state === "external-change" ? <p>Another settings change was observed. Your draft has not been silently reapplied. Compare the changed fields above, then discard and refresh before reviewing a new change.</p> : state === "unknown-write" ? <p>A write may still be in flight or may have completed without a reply. Do not resend it with a new request ID, delete the journal, or assume matching settings settles the outcome.</p> : <p>The latest settings check failed. Previously displayed values are not current evidence. Check the configured connection and management supervisor status, then use Refresh Garm settings.</p>}
    {evidence && <p>{evidence}</p>}
    <ol>
      <li>Stop additional settings edits in other Garm UI, CLI and automation sessions while investigating. Do not stop running jobs or delete runners.</li>
      <li>Preserve the authoritative settings journal and the pending request ID. Download the diagnostic summary below for the operator investigating the writer.</li>
      <li>For an externally coordinated installation, re-establish one settings writer before editing. For a managed installation, a revoked topology or identity boundary must be revalidated by its supervisor; a page refresh does not restore that authority.</li>
      <li>{snapshot?.pending?.resolution_available === true ? "Use the existing exact receipt-resolution action only for the recorded request. If it remains uncertain, keep it blocked." : "If the stock write has an unknown outcome, investigate the original writer and preserve the block. Current settings alone cannot safely resolve it."}</li>
    </ol>
    <button type="button" className="quiet" onClick={download}>Download writer diagnostic summary</button>
    <p>No reset, force-write, credential change or automatic retry is performed here.</p>
    {message && <p role="status">{message}</p>}
  </aside>;
}
