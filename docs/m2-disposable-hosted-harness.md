# M2 disposable harness: read-only survey first, no safety claim

This slice adds only a **read-only GitHub-hosted metadata survey** and a concrete
next-harness design. It does NOT execute M2 kernel isolation tests or a privileged
broker, open a host/daemon socket, create a container/VM/namespace, install software,
change policy, stop work, activate a runner or release capacity. The M1 journal
remains [unanchored non-executing facts](supervisor-audit-m1.md).

## Official hosted route and its limits

GitHub documents standard `ubuntu-24.04` public-repository jobs as fresh x64 VMs,
not the single-CPU `ubuntu-slim` container. Nested virtualization is technically
possible but not officially supported. Therefore do not promise KVM/VM testing from
a runner label or enable it through device permissions/security-policy changes.

The checked [Ubuntu24 software report](https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2404-Readme.md)
(image 20261101.360.1, blob 707d839e521e51c4e9b7dabd4511890be83b0fc2)
lists Docker 28.0.4, Podman 4.9.3, Buildah 1.33.7 and cached Go versions. It does NOT
guarantee an accessible daemon, delegated native cgroup, usable QEMU/KVM or a
preapproved guest/container image. Weekly images change; capture the actual job's
image/tool/kernel observations rather than infer qualification from that report.

Sources: [hosted concepts](https://docs.github.com/en/actions/concepts/runners/github-hosted-runners),
[hosted reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners),
[normal container jobs](https://docs.github.com/en/actions/how-tos/write-workflows/choose-where-workflows-run/run-jobs-in-a-container).
The last confirms ordinary container execution is a supported workflow mechanism;
it is NOT a credential/privilege isolation proof for our proposed supervisor.

## Exactly what the new workflow observes

`m2-read-only-hosted-capability-survey` uses only standard `ubuntu-24.04`, checkout
with credentials not persisted, `contents:read`, no secrets and no dynamic runner,
endpoint, command, device or image inputs. It reads:

- exact installed CLI version output using fixed `--version`/`go version` probes;
- OS/kernel, current effective-capability/non-privilege metadata and two read-only
  user-namespace/AppArmor sysctl values;
- cgroup v2/controller-file presence and caller write-permission observations;
- `/dev/kvm` type/permission metadata, WITHOUT opening it or issuing an ioctl.

It does NOT use Docker/Podman info, daemon connections, host sockets, sudo,
rootless namespace initialization, runtime resources, image pulls, network probes,
KVM access, mount/clone/pidfd operations or configuration changes. Probe children
receive a fresh private HOME and closed environment without tokens, runtime
endpoints, loader or proxy configuration. Automatic Go toolchain downloads are
disabled. Only those short-lived metadata children are bounded/terminated on
time/output overrun, never a service, runner or job.

Fresh output refuses existing files/directories/symlinks (including dangling).
The sole uploaded artifact and result-log line contain a bounded whitelist JSON, not home/configuration,
raw subprocess errors, full environment, process paths or host inventories.
Artifact acknowledgment follows successful creation of this invocation's JSON.

Every result permanently says `m2_kernel_tests_executed:false`,
`isolation_qualified:false`, `execution_authority:none`. Cgroup write permission is
NOT verified delegation; CLI presence is NOT daemon access; KVM permission is NOT a
VM boot. The hosted guard prevents accidental use on a different machine, not
proof of host isolation. A green workflow means **survey completed only**.
No production code accepts the output as enrollment, proof or permission.

## Implementable next subset, separately reviewed and authorized

An ordinary restricted-container fixture can test SOME necessary UID/IPC/mount
properties without privileged mode or host security changes, provided an approved
DISPOSABLE runtime control endpoint is available. That access is NOT provided or
probed by this read-only workflow and must not be inferred from its output.

A concrete future fixture would:

1. Use an already-installed supported Go/Docker toolchain. Build a tiny static
   reviewed Go helper into a local `FROM scratch` image: no registry base image,
   external binary, package installation or unpinned image pull is necessary.
   This image build/container execution is NOT performed by the present workflow.
2. Run only bounded fake broker/control/job roles with all capabilities dropped,
   no-new-privileges, read-only roots, no network/host namespaces/devices, strict
   CPU/memory/PID limits and default seccomp/AppArmor retained. Verify actual
   runtime configuration and kernel status before any adversarial fixture.
3. Share at most newly created test-owned scratch state, never the Docker socket,
   user credentials/control home, GARM/Incus endpoints or arbitrary host binds.
   Use distinct UIDs and fake decoy credentials only. Test actual kernel peer
   credentials and refusal of job-origin, stale-generation, path-traversal and
   forged state requests. Do not trust a client-claimed UID or JSON identity.
4. Observe native process-birth/pidfd/cgroup identity where already readable, and
   demonstrate that a queued accepted runtime request can invalidate an earlier
   empty observation. A cgroup-empty read still supplies NO creation frontier.
   Missing native permissions must stop the test, not cause policy relaxation.
5. Keep artifacts as observations/counterexamples only. Clean up only exact IDs
   recorded as created by this test plus verified ownership labels. Never prune,
   delete by broad name pattern or kill an unverified resource. A lost create reply
   is unknown fixture ownership, not permission to destroy another workload.

This subset does NOT test a host-privileged broker, atomically place children into
a delegated native cgroup, prevent arbitrary host-side clients, establish a global
no-restart fence, enroll an image or supply a rollback anchor. It is not full M2.
A runtime socket must NEVER be exposed to the untrusted test payload. If the scope
continues to prohibit even trusted harness access to a disposable runtime control
endpoint, this container subset remains unavailable.

## Exact prerequisites for full M2, with no workaround

Full broker/native-frontier tests need either:

- a separately approved disposable Linux environment with actual cgroup v2
  delegation/privileges, clone-into-cgroup and pidfd support, trusted process/UID
  separation and a controlled runtime; OR
- an approved userspace VM runtime plus pinned verified guest kernel/rootfs and
  sufficient resources. KVM can be optional only if a supported emulator/profile
  already exists. Neither QEMU, a guest image nor nested virtualization is assumed
  available here. No install or device/security permission change is authorized.

Required actual cases are in [the threat model](one-use-supervisor-threat-model.md):
all pre/main/post/cleanup paths, copied clients/credential replay, no Docker/host
fallback, protected hook scratch, UID/mount/IPC/cgroup escape, creation-at-birth,
queued pre-seal runtime work, crash/restore and held generation. An ephemeral CI VM
cannot by itself establish the independent cross-restore production anchor or the
actual installed-runner/GitHub service contract.

The current local task has no Docker/VM runtime on PATH, zero effective/bounding
capabilities and no writable cgroup. Existing bwrap/unshare presence does not fix
those gaps and is not permission to execute privileged tests. Nothing was weakened
or installed. Until an actual appropriate environment is approved and tested,
report the missing capability and keep M2/activation/retirement blocked.
