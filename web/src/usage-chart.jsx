import React from "react";
import { splitUsageSegments } from "./usage-client.js";

const fmt = (n) => n.toLocaleString();
export const USAGE_DIMENSIONS = [
  ["cpu_used_millis", "CPU used", "millicores"],
  ["ram_used_mib", "Memory used", "MiB"],
  ["disk_used_mib", "Disk used", "MiB"],
];
export function formatSampleAge(now, sampledAt) {
  const seconds = Math.max(0, Math.round((now.getTime() - Date.parse(sampledAt)) / 1000));
  if (seconds < 90) return `${seconds} seconds ago`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 90) return `${minutes} minutes ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 48) return `${hours} hours ago`;
  return `${Math.round(hours / 24)} days ago`;
}
// UsageChart draws measured samples as one polyline per contiguous monitoring
// segment. A sampling gap breaks the line instead of bridging it, and single
// sample points render as dots; absence is handled by the panel, never as a
// zero-filled series here.
export function UsageChart({ samples, dimension = "cpu_used_millis", label = "CPU used", unit = "millicores", now = new Date() }) {
  if (!Array.isArray(samples) || samples.length === 0) return null;
  const width = 640,
    height = 170,
    padX = 46,
    padTop = 12,
    padBottom = 22;
  const ordered = samples.slice().sort((a, b) => Date.parse(a.sampled_at) - Date.parse(b.sampled_at));
  const start = Date.parse(ordered[0].sampled_at),
    end = Date.parse(ordered[ordered.length - 1].sampled_at);
  const span = Math.max(1, end - start);
  const max = Math.max(1, ...ordered.map((sample) => sample[dimension]));
  const x = (sample) => padX + ((Date.parse(sample.sampled_at) - start) / span) * (width - padX - 10);
  const y = (value) => padTop + (1 - value / max) * (height - padTop - padBottom);
  const segments = splitUsageSegments(ordered);
  const gridSteps = [0, 0.5, 1];
  return (
    <svg viewBox={`0 0 ${width} ${height}`} role="img" aria-label={`${label} history chart`}>
      {gridSteps.map((step) => (
        <g key={step}>
          <line className="grid-line" x1={padX} x2={width - 10} y1={y(max * step)} y2={y(max * step)} />
          <text x={4} y={y(max * step) + 3}>
            {fmt(Math.round(max * step))}
          </text>
        </g>
      ))}
      {segments.map((segment, index) => (
        <polyline
          key={index}
          className="history-line"
          data-segment={index}
          points={segment.map((sample) => `${x(sample).toFixed(1)},${y(sample[dimension]).toFixed(1)}`).join(" ")}
        />
      ))}
      {ordered.map((sample) => (
        <circle key={sample.sampled_at} cx={x(sample)} cy={y(sample[dimension])} r="2.4" />
      ))}
      <text x={padX} y={height - 6}>
        {new Date(start).toLocaleString()}
      </text>
      <text x={width - 10} y={height - 6} textAnchor="end">
        {new Date(end).toLocaleString()} · {unit}
      </text>
    </svg>
  );
}
