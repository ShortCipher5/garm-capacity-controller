# Stock GARM zero grant and transfer boundaries

## Source distinction

Pinned stock GARM `154638445c3949c1958b01812f69d9a1e4d82684` rejects zero
maximum on pool **creation**, but its **update** path accepts explicit zero when
minimum idle is zero:

- [CreatePoolParams.Validate](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/params/requests.go#L227-L238)
  rejects max_runners=0 on create.
- [UpdatePoolParams](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/params/requests.go#L163-L170)
  uses pointers, preserving an explicit zero.
- [UpdatePoolByID](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pools.go#L74-L110)
  enforces min_idle<=max, without rejecting zero max.
- [Database update map](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/database/sql/util.go#L607-L617)
  writes the explicit zero; it is not lost as a zero-value omitted struct field.
- [Pool creation gates](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pool/pool.go#L1326-L1362)
  reject count>=max before AddRunner; min_idle=0 skips warm creation.

Thus zero-max rejection in our legacy ManagementClient was an additional local
restriction, not a stock UPDATE limitation. This change permits max=0 only when
the existing minimum idle is already0, preserving all other guards. A request
lowering a positive minimum idle to0 still fails. The experimental native
conditional path and its restrictions are unchanged.

## What the update does

For an existing pool whose min_idle is already0, keep enabled=true and update
max_runners to0 through the existing durable settings manager. This stops future
starts once the stock pool manager observes the change. It does not cancel an
already accepted AddRunner, in-flight provider call, runner registration or job.
It does not remove existing runners or release any allocation.

Do not use disabled=true as drain: pinned GARM's
[scale-down path](https://github.com/cloudbase/garm/blob/154638445c3949c1958b01812f69d9a1e4d82684/runner/pool/pool.go#L1225-L1234)
skips disabled pools. With enabled=true/min_idle=0, ordinary stock lifecycle can
continue. That lifecycle still requires independently safe provider retirement
before the capacity controller can reuse physical charges.

New pools can be created disabled with positive maximum and min_idle0, then
updated to max0 before enabling. This is a proposed staged sequence, not a new
onboarding path or an assertion that cached/in-flight work is fenced.

## Real transfer design

1. Persist a donor-grant reduction and stop admitting fresh donor work at the
   authoritative provider gate; retain all accepted/in-flight/unknown charges.
2. Journal the exact stock max reduction and write via the existing manager.
   Lost/ambiguous outcomes remain blocked across restart; matching GET alone
   cannot prove the original operation finished. Stock GET/fingerprint/PUT is
   not remote compare-and-swap; exclusive settings ownership remains necessary.
3. Do not infer freed capacity from reduced maxima or absent GARM rows. Reassign
   only unused authority-confirmed grants or authoritatively retired capacity.
4. Persist the recipient grant before increasing its stock maximum. Atomic
   provider admission remains the hard backstop against delayed/cache-stale starts.

Those grant/physical-authority/retirement mechanisms are not added by this PR.
No route-guard or denied dispatch material is reproduced. Write-enable flags,
credential configuration, live deployment and UI enablement are unchanged.

Tests use synthetic TLS stock-shaped responses and the real existing local
settings journal, including ambiguous response, restart and matching-read hold.
No live or installed stock GARM qualification is claimed.

## Local dispatch boundary and concurrency

The stock update handler performs a read followed by an update, and the SQL
transaction does not compare a caller-supplied settings revision. A client
fingerprint is therefore not remote CAS, even when local operations are
serialized. The existing durable settings manager remains the single intent
journal; no second pool-settings writer is introduced.

Queued settings writes now honor context cancellation while awaiting the local
management lock. A failed pre-PUT GET or cancelled queue entry returns a typed
`ErrManagementNotDispatched`; the live manager can durably record that exact
attempt as rejected. This classification is minted only before calling the PUT
transport. Every possibly-sent PUT, failed post-read, or crash before a durable
outcome record remains unknown/pending. A later matching GET cannot resolve it.

This does not supply exclusive settings ownership, bypass the provider/native
runtime guard, lower the positive-idle safety restriction, authorize maximum
increases, or activate a deployment. Automated pool target writes still require
an enforceable single-writer authority path. Native domain enrollment and a
fresh capacity ledger alone are not that authority.
