# Authenticated empty stock GARM inventory smoke

Run only on a standard disposable GitHub-hosted Ubuntu 24.04 worker:

```sh
python3 -B -m unittest discover -s deploy/runtime/stock-inventory
timeout --kill-after=15s 360s bash deploy/runtime/stock-inventory/smoke.sh
```

This is a separate non-root, loopback-only, networkless stock v0.2.1 daemon
with a new disposable database. It does not modify the existing uninitialized
joined preboot specimen. A probe using only the generated public CA must
observe that initial blank state before it uses stock's normal bootstrap and
login APIs. It sets only loopback controller URLs as required by stock, and
does not create repositories, pools, providers, instances or jobs. The actual
stock API must deny anonymous GETs and return canonical empty arrays for
authenticated instance, pool and provider inventory GETs. Fixture account
credentials and the bearer are generated locally and never printed.

Stock requires an admin bearer for these API routes. This test does not
qualify a read-only stock credential, gateway integration, enrolled pool
inventory, GitHub assignment, reusable capacity or any production topology.
The admin bearer exists only in the disposable probe process; it is not
handed to the controller or the existing preboot fixture.
