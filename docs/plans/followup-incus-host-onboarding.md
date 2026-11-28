# Install and configure Incus during Add Server

Status: partial implementation on this draft branch; not activated or live-qualified. See implementation and remaining scope below.

## Scope

Reuse reviewed SSH preparation and host controls (#105, #214, #411). Add Server is not complete if it merely reports a missing prerequisite. An operator can enroll a supported Linux machine with no Incus install and make it eligible for placement from the onboarding flow.

## Requirements

- Detect supported OS, package channel, Incus installation, daemon state, initialized storage/network and connector reachability before making changes.
- Show the exact supported installation/configuration changes for review, bind them to pinned SSH host and approved privilege, and apply version-aware fixed recipes. Prefer validated existing storage/network, and support an appropriate default without requiring ZFS. No arbitrary shell input or blind package upgrades.
- Where already configured, preserve working storage, bridges, projects, profiles and workloads. For fresh/uninitialized Incus, explicitly review storage/network/project setup and narrowly scoped TLS trust. Verify the exact daemon identity, version, capacity and route before placement; leave unverified setup disabled.
- Persist step state, verify before replay after a lost response, present recovery steps and avoid deleting existing data or retrying an uncertain action blindly.

## Acceptance

Disposable clean host, installed-but-uninitialized host, already-configured host with existing resources, unsupported distro, host key mismatch, constrained permissions and interruption/retry. No live-server or credential fixture.

## Estimate and release boundary

5–8 engineer-days for an initial supported Linux recipe and UI integration, versus 3–5 for preparing preinstalled hosts. More OS/package channels are separate. Engineering estimate excludes rollout and access lead time. Build against disposable fixtures; no live infrastructure, GitHub installation, or production mutations are needed for this PR. Keep stock GARM and existing queue/runner ownership. Mark any unimplemented or unsupported path plainly; do not mistake a fixture or plan for active capability.

## Implementation in this branch

The Add Server panel now offers a privileged, explicitly enabled **Install or configure Incus** operation. Private gateway config uses `admin.incus_host_setup` with `token_file`, `admin_credential_files` (alias → owner-only key file) and `targets` (target ID → `{admin_credential_alias, target:{address, port, admin_user, host_key, admin_key_fingerprint}}`). The separate `--allow-incus-host-setup` flag is required. The browser cannot choose an arbitrary IP, credential path, command, package or admin grant. Configure this on the private TLS admin listener; do not expose the setup token to public clients.

Load targets, select one, then inspect using the distinct setup token. A missing host must provide Debian 13/systemd, Python 3 and python3-apt, a current signed distribution cache, the independently pinned SSH key and a deployment-approved noninteractive sudo administrative credential. Incus 6.x/7.x are supported for observation. For a missing package the review names the exact native Debian candidate; a preconfigured signed `trixie-backports` release is eligible when APT selects it (no repositories, keys or pins are changed); automatic installation refuses to upgrade existing packages or add package sources. Installing Incus also installs dependencies. An installed but pristine daemon uses upstream local `incus admin init --minimal`; this can create storage, a default bridge and profile devices. Existing configured storage is not converted or erased. Incomplete/ambiguous state, unsupported OS and permission failures never qualify a host.

Apply consumes one five-minute target/identity/recipe/observed-state approval, journals unknown before contacting SSH, and verifies the daemon over its local Unix socket afterwards. A lost HTTP reply can be checked without rerunning: reload targets, read last attempt, then inspect. A surviving root-only remote unknown intent under `/var/lib/garm-capacity-incus-onboarding` blocks more changes. Inspect the host and reconcile that journal privately after a genuinely interrupted multi-step installation; never remove data, reset the daemon, or retry blindly. The gateway does not keep the SSH administrator credential on the target.

Local storage verification is **not** remote connector qualification: this operation never adds a public listener or trusts a controller certificate by itself, never selects an arbitrary existing project and never enables placement. Optionally give a target an `onboarding_profile` naming an existing static `incus_onboarding.profiles` entry with project, trusted client identity, and an explicit server-key pin. The inspect step verifies the configured route's mTLS response and matches its certificate and version to the independently authenticated SSH observation; local setup, connector unavailable/mismatch, same-daemon connectivity, and placement eligibility are distinct states. Use the separately authorized Incus discovery and disabled-host enrollment flow. Automatic scoped TLS/project enrollment on a pristine host, end-to-end runner-network/metrics qualification and linking that evidence to placement activation require integration work and disposable host qualification before this plan is complete.

For continuous host headroom metrics, the separate inventory role still uses the pinned forced-command inventory shell; install or upgrade its reviewed artifact through the existing restricted inventory workflow. Never repurpose the administrator key as a polling credential.

Supported upstream references: [Debian packages](https://linuxcontainers.org/incus/docs/main/installing/), [minimal initialization and its defaults](https://linuxcontainers.org/incus/docs/main/howto/initialize/).
