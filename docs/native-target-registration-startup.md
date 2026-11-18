# Dynamic native target startup

`--allow-native-target-registration` opts into the scoped configuration workflow:
registration, authenticated discovery and saved selections, local bundle assembly,
explicit first-plan registration and separately approved disabled preparation. It
requires `--allow-ssh-inventory` and a private `admin.native_registration` config.
It is mutually exclusive with the static native planning/preparation/assembly
flags and their static configuration blocks. Conflicting modes refuse before
private inputs are opened. Without the opt-in, the registration route is disabled.

The new configuration contains:

- `catalog`: deployment-owned `nativebundle.RegistrationCatalog`, including
  explicit template/profile references, unique private owner identity slots, and
  native administrator CIDR/user/port/public credential fingerprint scopes.
- `registration_directory`: existing protected 0700 permanent registration journal.
- `selection_directory`: existing protected 0700 saved-selection directory.
- `planning_token_file` and `preparation_token_file`: distinct protected token files,
  also distinct from other configured management/inventory/rotation privileges.
- `admin_credential_files`: exact administrator-alias to protected SSH key mapping.
  Actual public fingerprints must match each catalog scope. Restricted inventory
  and helper credentials cannot be reused. Parsed signers are retained so later
  file replacement cannot change a previously reviewed action's identity.

This configuration does not list fake baseline bundles. The handler starts with
real empty dynamic planning/assembly state, restores only verified durable
registration records, and exposes pending configurations separately from real
preparation Plans. The startup fixture exercises the actual configured admin
handler and restart with an empty inventory and no fabricated Plan; it does not
contact a remote host or claim a TLS handshake/host qualification test.

All existing owner activation/runtime guards remain closed. No SSH request occurs
at startup or local registration/assembly. Each later discovery, first-plan
registration and preparation action retains its separate explicit review. Owner
TLS issuance and storage/image/network profiles remain deployment responsibilities;
this workflow does not infer a ZFS root from ordinary OS disk inventory or issue
untrusted credentials. Root/journal rollback is not a supported recovery method.
