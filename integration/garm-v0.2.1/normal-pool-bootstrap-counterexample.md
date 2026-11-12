# Normal-pool bootstrap gap and its explicit resolution

The earlier disposable counterexample reproduced a real integration gap:
normal-pool Garm helpers inserted/preserved `runner_install_template`, while the
gateway rejected every nonempty ExtraSpecs value except `{}`. A hand-built
bootstrap with `{}` did not prove that the actual source path worked.

That expected-gap diagnostic is replaced by the explicit configured
`gateway-owned-default-cloudinit/v1` source contract. Custom templates/options are
rejected before pool writes/admission/JIT; explicit capability-gated public
`template_id:0` means SQL NULL. Only this mode skips Garm wrapper injection. The
gateway's arbitrary-script rejection is unchanged. See
[source contract](source-preparation.md) and
[actual integration scope](source-gateway-qualification.md).

This is an offline source extension and fixture qualification, not stock Garm
compatibility, permission to change a live pool, or production safety proof.
