import React, { useEffect, useState } from "react";
import { runnerDiskHealthRows } from "./runner-disk-health.js";

export function RunnerDiskHealth({ readings }) {
  const [, setClock] = useState(0);
  const now = Date.now();
  const rows = runnerDiskHealthRows(readings, now);
  const nextExpiry = Math.min(Infinity, ...rows.filter(row => row.state === "current").map(row => row.expires));
  useEffect(() => {
    if (!Number.isFinite(nextExpiry)) return;
    let timer;
    const tick = () => {
      const remaining = nextExpiry - Date.now();
      if (remaining > 0) timer = setTimeout(tick, Math.min(remaining, 300000));
      else setClock(generation => generation + 1);
    };
    tick();
    return () => clearTimeout(timer);
  }, [nextExpiry]);

  return <div className="runner-disk-health-panel" aria-label="Runner disk health">
    <h3>Runner disk health</h3>
    {rows.length === 0 ?
      <p className="muted">Unknown: no verified runner filesystem reading. Host root and configured runner sizes are not substitutes.</p> :
      <ul>{rows.map(row => <li key={row.pool}>
        <strong>Storage pool {row.pool}:</strong>{" "}
        {row.state === "stale" ? "Stale: current space and inode availability are unknown." :
          <>{row.availableBytes.toLocaleString()} of {row.totalBytes.toLocaleString()} bytes available · {row.availableInodes.toLocaleString()} of {row.totalInodes.toLocaleString()} inodes available</>}
        {" "}<span className="muted">(observed {new Date(row.observed).toLocaleString()})</span>
      </li>)}</ul>}
    <p className="muted">These are filesystem readings, not per-runner quotas or a capacity guarantee.</p>
  </div>;
}
