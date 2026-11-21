import React from "react";

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
// Only recorded points are shown. The wire does not establish coverage between
// observations, so no line implies uninterrupted monitoring or source continuity.
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
      {ordered.map((sample, index) => (
        <circle key={`${sample.source}:${sample.sampled_at}:${index}`} cx={x(sample)} cy={y(sample[dimension])} r="2.4" />
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
