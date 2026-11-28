# Install and configure Incus during Add Server

Status: draft implementation plan. This document is not an implemented or activated feature.

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
