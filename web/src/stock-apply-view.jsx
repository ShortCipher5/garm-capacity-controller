import { stockApplyMessages } from "./stock-apply-view.js";

// Read-only operator surface for the durable stock-apply controller state.
// Every command renders disabled with its server-supplied reason; nothing
// here changes Garm settings or implies a production permission grant.
export function StockApplyControllerView({ view }) {
  if (!view) return null;
  const messages = stockApplyMessages(view);
  return <section className="card stock-apply" aria-label="Stock settings apply controller state">
    <h3>Stock settings apply (controller state)</h3>
    <p><strong>{messages.state}</strong></p>
    {messages.reason && messages.reason !== "view_unavailable" && <p>Reason: <code>{messages.reason}</code></p>}
    <p>{messages.charged}</p>
    {messages.drift && <p className="drain-warning" role="alert">{messages.drift}</p>}
    {view.operation && <p>Recorded operation <code>{view.operation.operation_id}</code>: {view.operation.direction} pool <code>{view.operation.pool_id}</code> maximum from {view.operation.observed} to {view.operation.suggested} (durable state: <code>{view.operation.state}</code>).</p>}
    {messages.refusals.length > 0 && <ul>{messages.refusals.map((line, index) => <li key={index}>{line}</li>)}</ul>}
    <h4>Operator commands</h4>
    <ul>
      {view.actions.map(action => <li key={action.action}>
        <button type="button" disabled title={action.reason || "disabled"}>{action.action.replaceAll("_", " ")}</button>
        <span> disabled{action.reason ? `: ${action.reason.replaceAll("_", " ")}` : ""}</span>
      </li>)}
    </ul>
    <p>Nothing on this panel changes Garm settings, fences another writer, or grants production permission. Maintenance pause and resume are controller-local controls; an interrupted or unknown operation stays blocked and charged until an independently authoritative outcome is proven.</p>
  </section>;
}
