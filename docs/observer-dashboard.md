# Observation dashboard

Use the gateway's separate `-observer-config` mode for an observation-only
dashboard on an existing stock GARM deployment. This is not the managed
capacity-gateway configuration. The observer refuses all other flags, does not
open an admission database, and never connects to a GARM writer, provider API,
or instance/operation route on Incus. Existing GARM owns runner lifecycle.

The listener is HTTPS on loopback and serves the dashboard at `/admin/`.
The browser sends an owner-only dashboard bearer token with the fixed
`GET /admin/api/v1/observer` request. It does not persist the token to local
storage or cookies. Use separate access control on any external route and do not
proxy the stock reader, Incus API, or any existing management paths directly.

Configuration is owner-only JSON. Required fields:

- `listen`, a loopback address and port; `server_tls`, standard certificate,
  key and CA file paths; `token_file`, a private file containing a randomly
  generated 32-byte token encoded as 64 lowercase hexadecimal characters.
- `stock` fields `endpoint`, `controller`, `server_key_pin`, `client_tls`,
  `providers`, and `repositories`. The endpoint is the existing loopback
  authenticated private stock reader. The provider names and repository IDs
  must match the approved reader scope. Do not supply the human GARM password
  or any GARM API write credential.
- Optional `metrics`, keyed by a dashboard host ID, with `endpoint`, `project`,
  `server_key_pin`, and `client_tls` for each host. Use a separately generated
  Incus metrics-only client certificate restricted to the intended project.
  Never substitute an existing GARM provisioning certificate. The client
  requires normal CA and hostname verification plus a pinned native TLS key.
  If the endpoint is an IP and its native certificate identifies a DNS name,
  set `server_name` to the exact enrolled DNS SAN. This does not change where
  the connection dials, allow insecure TLS, or skip the SPKI pin.
  No new relay or wider TLS class authorization is part of this mode.

The GARM feed lists observed pool supply and queue state, not missed webhooks.
The Incus feed shows user/system CPU time and current memory of observed running
Linux containers. Missing observations are unavailable, not zero. These
samples do not establish host totals, reservations, operation state, job-attempt
identity, or free capacity, and they never feed admission or scaling.

Before rollout, confirm the exact qualified Incus version and credential
behavior for each host, and check the existing stock GARM route and service
still work. Validate the private dashboard's allowed route and anonymous
denials before opening any external route. Rollback removes the observer route,
stops only its service and revokes only its dedicated client certificates.
