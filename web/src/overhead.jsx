import React from 'react';
import {overheadSummary} from './overhead.js';
export function Overhead({value}){
 return <section aria-label="Retained non-pool overhead" className="card"><h3>Retained non-pool overhead</h3><p>{overheadSummary(value)}</p>
 {value&&<details><summary>Accounting details</summary><p>These conservative ceilings are already included in global and host charges, not attributed to pools or running-runner counts. They can overlap guest charges. Do not add them again or interpret them as measured usage or free capacity. Absence or lower usage does not release this retained debt.</p><div className="table-wrap"><table><thead><tr><th>Retained charge</th><th>CPU (millicores)</th><th>RAM (MiB)</th><th>Disk (MiB)</th><th>Runner slots</th></tr></thead><tbody>{[['Native domains',value.domain],['Shared artifact roots',value.artifact]].map(([name,r])=><tr key={name}><td>{name}</td><td>{r.cpu_millis}</td><td>{r.ram_mib}</td><td>{r.disk_mib}</td><td>{r.runners}</td></tr>)}</tbody></table></div><p>This is a global subtotal from this snapshot, not per-host or per-guest attribution and not permission to release resources.</p></details>}
 </section>;
}
