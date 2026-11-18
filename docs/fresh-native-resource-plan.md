# Fresh native resource planning, not activation

`Policy.PlanFreshResources` is a pure compiler for reviewed storage/network
request bodies. It performs no I/O and supplies no proof of domain ownership,
dataset birth, quota enforcement, isolation, successful creation, or release.
Nothing calls it from the live controller. Its digest binds policy, domain,
quota-parent name and ordered fixed requests; returned request slices are copies.

The supported storage source is a direct child of the host quota parent, not the
parent or reserved `runtime` child. Distinct Incus pools cannot share a source,
and one Incus pool cannot select different sources through different recipes.
Existing empty datasets must never be adopted on this basis. A future executor
must receive a genuine fresh child birth/GUID and recheck it, alongside the
constructed namespace/process identity and immutable parent quota.

Pinned Incus 6.0.4 may create a missing source dataset or accept an empty existing
one, and its creation rollback may delete that source. See
[the pinned driver](https://github.com/lxc/incus/blob/v6.0.4/internal/server/storage/drivers/driver_zfs.go).
This is why a source string or empty listing is not sufficient authorization.
No device path, loop pool, wipe request, arbitrary configuration or quota change
is emitted by this compiler.

The bridge request is deliberately isolated L2 with no IP, DHCP/DNS, NAT or
external interface. It is NOT a usable runner Internet connection. A future
networking contract must construct and verify controlled egress inside the
isolated domain, rather than attaching this bridge to host networking.
The settings follow the
[pinned bridge implementation](https://github.com/lxc/incus/blob/v6.0.4/internal/server/network/driver_bridge.go).

Remaining execution work: consume actual constructed namespace and fresh dataset
handles; durably record first attempt before mutation; reject any existing
resource/conflict; validate exact synchronous response and readback; quarantine
partial or lost responses without automatic retry/adoption/cleanup. Until that
exists, the existing control activation refusal must remain unchanged.
