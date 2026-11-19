# Protected guest graceful shutdown (dormant)

The two protected PID1 entrypoints now retain SIGTERM, SIGINT and SIGHUP without
forwarding them to jobs. SIGPWR records a pending halt request. The process may
return successfully only after all descendants have been reaped, the exact
single supported eth0 interface has been observed down, and the terminal root
journal record has been durably written. A request arriving before that point
waits; it does not cancel a running job. Failures leave PID1 alive and keep the
signal shield installed. No forced-stop fallback is supplied.

This matches the default LXC halt signal. Pinned Incus v6.0.4 delegates graceful
shutdown to liblxc and waits for its stop hook:
https://github.com/lxc/incus/blob/v6.0.4/internal/server/instance/drivers/driver_lxc.go#L2731-L2833
Pinned LXC v6.0.4 implements its halt default in lxccontainer.c:
https://github.com/lxc/lxc/blob/v6.0.4/src/lxc/lxccontainer.c
An image/domain with a different halt signal is unsupported until explicitly
qualified; timeout is not permission to force-stop or release capacity.

Tests exercise concurrent gate transitions and actual SIGPWR delivery to a
**disposable ordinary subprocess**, not a running native guest. They do not
prove PID namespace containment, Incus shutdown, safe physical retirement,
storage absence or GitHub job completion. The existing unqualified journal
format and capacity-release restrictions are unchanged. Actual native owner
control and physical completion qualification remain required.
