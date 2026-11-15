import React, {useState} from "react";
import {GarmInventoryChanges} from "./garm-inventory-changes.jsx";
import {selectInventoryPage, inventoryProviders} from "./garm-inventory-search.js";

// Controls affect the display only, never upstream scope or permissions.
export function GarmInventoryBrowser({rows, kind, children}) {
 const [query, setQuery] = useState(""), [provider, setProvider] = useState(""), [page, setPage] = useState(0);
 const view = selectInventoryPage(rows, {query, provider, page});
 return <div>
  <GarmInventoryChanges rows={rows} kind={kind}/>
  <label>Search {kind} inventory<input type="search" maxLength={256} value={query} onChange={event=>{setQuery(event.target.value);setPage(0);}} placeholder="Name, ID, owner or tag" /></label>
  <label>Filter {kind} provider<select value={provider} onChange={event=>{setProvider(event.target.value);setPage(0);}}><option value="">All observed providers</option>{inventoryProviders(rows,provider).map(value=><option key={value} value={value}>{value}</option>)}</select></label>
  <p role="status">Showing {view.rows.length} of {view.matched} matching {kind}; {view.total} observed in provider scope. Display filters do not change discovery scope or permissions.</p>
  {view.rows.map(children)}
  {view.total > 0 && view.matched === 0 && <p>No observed {kind} match these display filters. This does not mean none exist.</p>}
  {view.pages > 1 && <nav aria-label={`${kind} inventory pages`}><button type="button" disabled={view.page===0} onClick={()=>setPage(view.page-1)}>Previous {kind} page</button><span>Page {view.page+1} of {view.pages}</span><button type="button" disabled={view.page+1===view.pages} onClick={()=>setPage(view.page+1)}>Next {kind} page</button></nav>}
 </div>;
}
