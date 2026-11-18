# Approved runtime asset slots

This is local configuration registration, not provisioning, SSH, readiness,
physical admission, or activation. Stock GARM remains unmodified.

Deployment may configure `native_assets.slots` beside legacy `targets`. Each
slot contains `bundle_directory`, `helper_path`, `manifest_sha256`, and
`helper_sha256`, all protected deployment inputs with the same verified file
rules as the existing uploader. Configure `native_assets.registry_directory`
as a dedicated existing operator-owned 0700 directory. Static targets plus slots
must total 1–8 and every asset manifest ID must be unique across both. The
existing separate asset staging token authenticates both workflows; general
read, management and preparation tokens cannot register asset plans.

Each slot is an **exact preapproved, one-use manifest ID**, not a reusable image
recipe. Its actual files are opened and verified at startup. The approved owner
registration template must contain the matching `runtime_assets` reference.
After the owner bundle is assembled and its reviewed plan is registered, a GET
of `/admin/api/v1/native-asset-plans` offers only slots whose ID and manifest
digest exactly match that real completed assembly. No browser file paths,
commands, credential references or invented assembly reports are accepted.

POST requires `slot`, `target_id`, `preparation_id`, `review_digest` and
`confirm_asset_plan_registration: true`. It reserves the slot and target locally,
creates a protected transfer journal, and persists completion. This never calls
SSH or resolves its signer. The new recipe appears in the existing asset staging
workflow, which still needs its own exact upload approval. A lost registration
response must be resolved by reloading status, not retrying the POST.

The complete review binds the frozen catalog, completed assembly, saved choice,
current owner plan, exact SSH tuple, asset ID, file manifest and transfer recipe.
Every later staging action rechecks the actual current owner assembly; switching
the owner plan cannot silently retain the old asset association.

On restart only exact completed registrations are reopened. Partial or unknown
reservations consume the slot permanently; there is no reset, deletion, retry,
ID rewrite, or implicit repair. Preserve the directory. The catalog is frozen
once a slot is consumed: renaming/removing/changing slots is rejected against
its history, rather than freeing capacity. Operator-approved new manifests and
catalog configuration remain necessary for new IDs. This removes per-target
SSH recipe edits/restarts for preapproved slots, not artifact preparation itself.

Registration and uploaded-asset receipts are never physical capacity evidence,
launch authority, or permission to start the owner. The prior runtime dispatch
binding restrictions and disabled activation paths are unchanged.
