import React from "react";

const fmt = (n) => n.toLocaleString();
export const dimensions = [
  ["cpu_millis", "CPU allocation", "millicores"],
  ["ram_mib", "Memory allocation", "MiB"],
  ["runners", "Runner allocation", "runners"],
  ["disk_mib", "Disk allocation", "MiB"],
];
export function allocationText(allocation, key) {
  const used=allocation.allocated[key], budget=allocation.budget[key];
  if (!Number.isSafeInteger(used) || !Number.isSafeInteger(budget)) return "Unavailable";
  return key === "disk_mib" && budget === 0 ? `${fmt(used)} / no ceiling` : `${fmt(used)} / ${fmt(budget)}`;
}
export function Usage({ allocation, metric = false }) {
  return dimensions.map(([key, label, unit]) => {
    const used = allocation.allocated[key],
      budget = allocation.budget[key];
    if (!Number.isSafeInteger(used) || !Number.isSafeInteger(budget)) return <div className={metric ? "card metric" : "usage"} key={key}><span>{label}</span><p>Allocation or budget unavailable; not assumed zero.</p></div>;
    if (key === "disk_mib" && budget === 0) return <div className={metric ? "card metric" : "usage"} key={key}><span>{label}</span><p>{fmt(used)} MiB charged · No disk ceiling configured</p><p>Zero disables the disk limit; it does not mean disk is free or available.</p></div>;
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
