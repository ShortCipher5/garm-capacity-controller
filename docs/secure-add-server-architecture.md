# Secure Add Server: implemented trust component and remaining lifecycle

## Product direction

Keep stock GARM. Prefer supported Incus HTTPS/mTLS for ongoing discovery and
provisioning. SSH is a bounded bootstrap/configuration channel, not a permanent
privileged agent and not a job relay. A local helper is justified only for a
specific visibility or enforcement gap that Incus cannot address. It must not
receive a general root shell or an `incus-admin` equivalent merely to collect
hardware totals.

These are architecture requirements, not claims that guided SSH onboarding is
implemented. The current runtime accepts only operator-configured profiles and
private credential files. It does not accept a browser IP address or SSH secret.

## Implemented in this change

An onboarding profile can specify `server_key_sha256`: exactly 64 lowercase hex
characters representing SHA-256 of the daemon TLS leaf certificate's DER
SubjectPublicKeyInfo. This is a **public-key fingerprint**, not Incus's full
certificate fingerprint. Obtain it from an independently trusted operator/host
console or authenticated configuration channel, not from a failed TLS connection.
Never populate it by silently accepting the first server that responds.

The operator-supplied pin adds a check to normal CA-chain and hostname
verification; it does not replace either. It is installed before constructing
any onboarding transport and checked on TLS connections before application HTTP
reads. A mismatch is an unavailable observation, never new trust or healthy
capacity. Private credential-file permissions, bounded calls and redirect refusal
remain unchanged. Offline configuration validation and runtime use the same
loader. Errors do not echo pins, paths or certificate material.

Existing profiles without a pin retain existing verified CA/hostname mTLS
behavior for compatibility. Absence of a pin must not be presented as an
independently confirmed server identity. A future guided onboarding flow should
require explicit expected-key approval before enabling a connection.

Pin changes are explicit reviewed configuration changes requiring new transport
construction/restart, not in-place mutable callbacks. Rotating a certificate with
the same public key preserves its SPKI pin, but still requires normal certificate
validity. A new key requires separate review; there is no automatic key-rotation
acceptance. Removing a profile and restarting stops its future configured
probes, but **does not revoke its certificate at Incus**. Remote revocation and
in-flight work cancellation remain separate lifecycle implementation.

## Required guided flow (not yet implemented)

1. **Draft connection intent:** authenticated operator supplies address, port,
   protocol, intended host/project and desired scope. Store nonsecret metadata
   only. Resolve/validate against a deployment-managed egress policy; check the
   exact resolved destination at dial time to prevent DNS rebinding. Do not allow
   browser-selected local paths, proxy commands, arbitrary schemes or redirects.
2. **Verify server identity:** for SSH use an independently approved known-host
   key/CA with strict matching. Do not offer `StrictHostKeyChecking=no`, blind
   accept-new, agent forwarding, port forwarding or proxy command execution. For
   Incus use verified HTTPS plus the explicit server-key pin implemented here.
   Any changed key returns to review; never continue with a warning alone.
3. **Acquire scoped credentials:** use a separately authorized credential
   reference/service, not browser local storage or database plaintext. Separate
   short-lived bootstrap authority from long-lived operational credentials.
   Define encryption/key ownership, auditing, expiry, rotation and remote
   revocation before implementing persistence. Do not imply the existing
   owner-only file loader provides a complete credential-management service.
4. **Read-only preflight:** collect bounded CPU/RAM/disk/project/workload and
   permission observations. Surface inaccessible scope as unknown. Reuse the
   versioned proposal and durable review pipeline in #91/#93. Authenticated
   access is not proof of read-only authority or whole-host capacity ownership.
5. **Review changes:** present a versioned immutable plan showing packages,
   service identity, listener/network changes, certificate scope, directories,
   permissions and cleanup/rollback. Installation, trust grants and system
   changes require explicit approval of that concrete plan, not merely "probe".
6. **Execute bounded bootstrap:** an isolated worker runs fixed typed actions,
   with cancellation, deadlines, idempotent operation journal, output limits and
   secret redaction. No shell interpolation from browser inputs. A timeout is
   unknown outcome; reconcile before retrying. No generic remote-shell endpoint.
7. **Persist connection and revoke:** atomically commit the reviewed nonsecret
   connection revision and credential reference, reject stale revisions, close
   old transport pools, and invalidate old discovery reviews. Revocation must
   stop new work, reconcile unknown work, and remove remote trust when possible;
   show pending/failed remote removal rather than claiming revocation succeeded.
8. **Separate activation:** bind the authenticated daemon/project to physical
   authority, obtain complete inventory and configure the admission boundary.
   Disabled enrollment, discovery totals and transport trust alone cannot enable
   safe creation. Existing allocations remain charged until authoritative proof.

## Why no automatic agent installation here

The repository has no dynamic credential vault integration, approved-bootstrap
operation journal, SSH execution isolation, dynamic profile revocation, or
host-wide permission qualification. Installing a general agent would bypass
these missing boundaries rather than implement them. The concrete blocker is
missing lifecycle code and security integration, not a need to access a live
server just to write the code. This patch deliberately makes one existing
transport safer and documents the remaining implementable stages.

## Primary references checked 2026-11-18

- [Incus authentication](https://linuxcontainers.org/incus/docs/main/authentication/):
  supports TLS client authentication and trust management.
- [Incus authorization](https://linuxcontainers.org/incus/docs/main/authorization/):
  project restrictions are not the same as read-only authority. Authorization
  driver selection can change restricted-certificate behavior; verify the actual
  deployed version and configuration rather than infer permissions from a label.
- [Incus project isolation](https://linuxcontainers.org/incus/docs/main/explanation/projects/):
  broad `incus-admin` access is effectively host-administrator authority.
- [OpenSSH client configuration](https://man.openbsd.org/ssh_config): strict
  host-key matching, forwarding and command settings must be deliberately scoped.

The controller's existing compatibility target and trust checks still apply;
current documentation is not evidence that a specific target supports every
newer authorization feature.
