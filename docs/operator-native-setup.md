# Native setup in the existing Servers screen

Two collapsed panels connect to real authenticated handlers. They do not add a
page or accept arbitrary endpoints, policy/proof JSON, host keys or owner TLS
credentials. Loading requires the ordinary read session. Each write consumes and
clears a separately entered management token and a fresh server-side recipe
revision. A missing/unconfigured API exposes no executable reviewed action.

Native enrollment uses GET /admin/api/v1/native-enrollment and sends only recipe,
recipe_revision and expected_sequence. Fixed initial versus existing-binding
mode is shown, never selected or silently switched by the browser. Initial mode
requires whole-ledger unbound/uncharged/not-ready coverage; existing mode requires
exact stored host bindings. Responses must echo the reviewed request. Partial,
failed, lost or cancelled responses invalidate the review and require another
GET. Configuration is never labelled readiness or execution authority.

Collection uses GET/POST /admin/api/v1/overhead-refresh. POST sends a new random
32-hex request ID with only recipe/revision. Accepted asynchronous work is
monitored by read-only status polls, never by resending POST. Durable complete,
partial, failed and unknown receipts remain historical, not current capacity
or health. A failed receipt reports no known successful sequences, not proof of
no effects. Partial receipts may omit uncertain later effects. Missing/stale
status removes executable choices.

New explicit collection requests are possible only after a successful status
read reports can_start and the operator reselects/reapproves a recipe. They use
a new request ID; old unknown operations are not replayed. Closing/cancelling
the browser view stops monitoring, not accepted server work or recorded charges.
Terminal receipts trigger a read-only overview refresh, not a release or enable.

Dependencies: real enrollment and overhead-refresh recipe handlers. Frontend
mocks exercise response/protocol/error handling; actual owner TLS/SQLite
qualification belongs to the backend tests and is not claimed by those mocks.
