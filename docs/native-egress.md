# Explicit isolated native egress

This is construction code, not an enabled runtime or a qualified firewall deployment.
It must be integrated with the actual paused capsule and reviewed with the host's
network manager/firewall policy before a supported host can be enabled.

## Modes

* `public-internet-v4`: usable general CI egress to public IPv4 TCP and UDP endpoints,
  including GitHub, package/image registries, HTTPS, Git SSH and job-selected public
  services. This is deliberately **not** a domain allowlist or an exfiltration filter.
* `allowlist-v4`: explicit destination CIDR, protocol and port rules, plus explicit
  public DNS resolvers. CDN/GitHub addresses change; operators must maintain this
  stricter policy. A GitHub-only allowlist does not cover arbitrary CI jobs.

Both modes deny host-local input/output, private/reserved/link-local/metadata IPv4,
all IPv6 and arbitrary non-TCP/UDP outbound protocols. Every public control-plane or
admin endpoint must appear in deployment-owned `control_plane_cidrs`. There is no
private-network override, proxy requirement or silent unrestricted fallback.
Jobs needing private registries, IPv6, inbound listeners, ICMP or other protocols
are outside this supported profile and must be reported as blocked, not working.

Use an explicitly reserved private `/30` transit and disjoint `/24` runner subnet.
No guessed host addresses are defaults. DNS has one to three approved public IPv4
resolvers. The runtime image's pinned resolver file must match that choice for
native daemon downloads; runner DHCP advertises those same resolvers directly.
Version-4 native policy binds the egress digest and protected runner image together.
It creates exactly one shared capsule-local bridge with explicit IPv4 DHCP, NAT,
routing and firewall. No raw dnsmasq option disables its AppArmor profile.

## Host prerequisites and limits

The host already has IPv4 forwarding enabled and an external route. This code never
changes global sysctls, flushes firewall rules or enables network services. Existing
host policy can still block egress. No flowtables/offload are supported. A second exact owned nft bridge-family table drops all bridge input/output/forward
traffic on the owned veth, independently of br_netfilter. Both tables install in
one transaction before veth creation. Any master assignment also fails actual
readback. Network managers should leave this prefix unmanaged for availability;
an accidental bridge assignment is denied rather than bypassing routed policy.
Unsupported nft bridge hooks are a hard refusal, never an inet-only fallback.

The root-owned network registry must be shared by every capsule on the host. It
permanently reserves transit subnets before mutation, including failed births.
Creation atomically attaches the veth peer using the actual held netns descriptor,
installs a unique owned nft table before bringing interfaces up, and verifies exact
firewall plus paired veth indices. It does not adopt or delete preexisting state.
Any timeout, lost response, partial operation or changed readback remains unknown.
Recheck uses real descriptors and pinned tools; failure latches across copied handles.
A trusted host administrator can change kernel policy; this is not protection from
host root. No tests execute real nft, network namespaces, veth creation or host routes.
Actual iproute2/nft JSON compatibility, packet paths and restart behavior require
supported-host qualification before enabling runtime authority.

Only connection-tracked related IPv4 ICMP errors (unreachable, time-exceeded and
parameter-problem) may return from host/private routers to the capsule. This is
needed for path-MTU discovery; it does not allow new host/private connections,
arbitrary outbound ICMP probes, or untracked ICMP from the external network.
