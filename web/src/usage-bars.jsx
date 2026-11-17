import React from "react";

const fmt = (n) => n.toLocaleString();
export const dimensions = [
  ["cpu_millis", "CPU allocation", "millicores"],
  ["ram_mib", "Memory allocation", "MiB"],
  ["runners", "Runner allocation", "runners"],
];
export function Usage({ allocation, metric = false }) {
  return dimensions.map(([key, label, unit]) => {
    const used = allocation.allocated[key],
      budget = allocation.budget[key];
    return metric ? (
      <article className="card metric" key={key}>
        <span>{label}</span>
        <h2>
          {fmt(used)}{" "}
          <small>
            / {fmt(budget)} {unit}
          </small>
        </h2>
        <progress
          aria-label={label}
          max={Math.max(budget, 1)}
          value={Math.min(used, Math.max(budget, 1))}
        />
        <p>
          {used > budget
            ? "Over declared budget"
            : `${fmt(budget - used)} ${unit} unallocated`}{" "}
          · Not a guarantee of availability
        </p>
      </article>
    ) : (
      <div className="usage" key={key}>
        <div>
          <span>{label}</span>
          <strong>
            {fmt(used)} / {fmt(budget)} <small>{unit}</small>
            {used > budget ? " · over budget" : ""}
          </strong>
        </div>
        <progress
          aria-label={label}
          max={Math.max(budget, 1)}
          value={Math.min(used, Math.max(budget, 1))}
        />
      </div>
    );
  });
}
