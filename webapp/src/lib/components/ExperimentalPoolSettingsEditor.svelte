<script lang="ts">
	import { createEventDispatcher, onMount } from 'svelte';
	import type { Pool } from '$lib/api/generated/api.js';
	import {
		getPoolSettings,
		patchPoolSettings,
		PoolSettingsError,
		type PoolSettings,
		type PoolSettingsRef
	} from '$lib/experimental/pool-settings.js';

	export let pool: Pool;

	// Injectable storage accessor. Production uses the tab-scoped
	// window.localStorage; tests inject a shared in-memory Storage so two
	// tabs can be modelled inside one jsdom document.
	export let storage: Storage | null = null;

	function store(): Storage {
		return storage ?? localStorage;
	}

	const dispatch = createEventDispatcher<{
		close: void;
	}>();

	let loading = true;
	let refreshing = false;
	let saving = false;
	let loadError = '';

	// Server-observed state and its strong ETag.
	let serverValues: PoolSettings | null = null;
	let etag: string | null = null;

	// The operator's draft. Preserved across conflicts and UNKNOWN holds.
	let draft: PoolSettings = { priority: 0, max_runners: 0, min_idle_runners: 0 };

	// Outcome states.
	let applied = false;
	let conflictEtag: string | null = null;
	let conflictLatched = false;
	// reconcileFreshRead arms only when a GET completes AFTER the conflict,
	// so "Mark reconciled" always asserts the operator reviewed values from
	// a fresh read. Any later successful read re-arms it against the newest
	// values; a new conflict disarms it again.
	let reconcileFreshRead = false;
	let unknownHeld = false;
	let errorMessage = '';

	// Client-side durable journal of a dispatched-but-unresolved write. The
	// exact request id, payload, full draft and precondition are persisted
	// before dispatch and cleared on any definitive outcome, so a browser
	// restart recovers the UNKNOWN state instead of silently dropping it.
	// The journal is kept per pool so concurrent editors of different pools
	// can never overwrite or clear each other's pending request. The full
	// draft is journaled alongside the patch so recovery restores every
	// field the operator saw, not just the changed ones. The journaled
	// tabId is the lock identity this tab re-enters under after a restart.
	interface PendingRequest {
		requestId: string;
		tabId: string;
		payload: Partial<PoolSettings>;
		draft: PoolSettings;
		etag: string;
		poolID: string;
		at: string;
	}
	let pendingRequest: PendingRequest | null = null;

	// Multi-tab write lock. The stored value is JSON {tabId, acquiredAt}; a
	// tab may claim only a free lock or refresh its own, and may release
	// only a lock it owns. claimLock reads and writes with NO await between
	// the two steps: storage access is synchronous and JS runs to
	// completion, so against other tabs the read-check-write is one atomic
	// step — a second tab's claim cannot interleave and double-claim.
	interface PoolSettingsLock {
		tabId: string;
		acquiredAt: string;
	}

	// A lock older than this is stale: another tab may then take it over,
	// but only through the operator's explicit "Take over lock" action.
	const LOCK_STALE_MS = 5 * 60 * 1000;

	// tabId identifies THIS tab/editor instance for lock ownership. On
	// journal recovery the editor adopts the journaled tabId so a deliberate
	// resolve after a restart re-enters the lock held before the restart
	// instead of deadlocking against it.
	let tabId: string = crypto.randomUUID();

	// This tab currently owns the multi-tab lock.
	let heldLock = false;
	// Another tab's lock is stale: the operator may explicitly take it over.
	let lockTakeoverOffered = false;
	// This tab lost ownership (stale takeover in another tab): editing is
	// blocked with an explanatory state.
	let lockLost = false;
	// Latched: recovery data (pending journal or lock record) is unreadable.
	// Save stays disabled and nothing is discarded until the operator fixes
	// storage; a successful load does NOT clear this.
	let recoveryError = '';

	function pendingStorageKey(): string {
		return `garm.expPoolSettings.pending.${pool.id}`;
	}

	function lockStorageKey(): string {
		return `garm.expPoolSettings.lock.${pool.id}`;
	}

	// persistPending is fail-closed: it reports whether the journal actually
	// reached storage so save() can abort BEFORE dispatching when the
	// pending record would otherwise be lost (and the UNKNOWN hold with it).
	function persistPending(rec: PendingRequest): boolean {
		try {
			store().setItem(pendingStorageKey(), JSON.stringify(rec));
			return true;
		} catch {
			return false;
		}
	}

	function clearPending() {
		pendingRequest = null;
		try {
			store().removeItem(pendingStorageKey());
		} catch {
			// ignore
		}
	}

	// parseLock reads one lock record. It throws on unparseable JSON and
	// returns null on a wrong-shaped record; readLock maps both to 'corrupt'
	// so callers can fail closed instead of misreading the lock as free.
	function parseLock(raw: string): PoolSettingsLock | null {
		const parsed = JSON.parse(raw) as unknown;
		if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null;
		const rec = parsed as Record<string, unknown>;
		if (typeof rec.tabId !== 'string' || rec.tabId === '') return null;
		if (typeof rec.acquiredAt !== 'string' || rec.acquiredAt === '') return null;
		return { tabId: rec.tabId, acquiredAt: rec.acquiredAt };
	}

	function readLock(): PoolSettingsLock | null | 'corrupt' {
		const raw = store().getItem(lockStorageKey());
		if (!raw) return null;
		try {
			return parseLock(raw) ?? 'corrupt';
		} catch {
			return 'corrupt';
		}
	}

	function lockIsStale(lock: PoolSettingsLock): boolean {
		const acquiredAt = Date.parse(lock.acquiredAt);
		if (Number.isNaN(acquiredAt)) return true;
		return Date.now() - acquiredAt > LOCK_STALE_MS;
	}

	// claimLock is the multi-tab guard around a write to this pool. A lock
	// held by another tab refuses the claim ('blocked'); a lock held by a
	// likely-dead tab (acquiredAt past LOCK_STALE_MS) reports 'stale' so the
	// operator can explicitly take it over. Re-entering under this tab's own
	// tabId (a deliberate resolve after UNKNOWN or restart) refreshes the
	// lock instead of deadlocking against it.
	function claimLock(): 'claimed' | 'blocked' | 'stale' | 'corrupt' | 'error' {
		let existing: PoolSettingsLock | null | 'corrupt';
		try {
			existing = readLock();
		} catch {
			return 'error';
		}
		if (existing === 'corrupt') return 'corrupt';
		if (existing && existing.tabId !== tabId) {
			return lockIsStale(existing) ? 'stale' : 'blocked';
		}
		try {
			store().setItem(lockStorageKey(), JSON.stringify({ tabId, acquiredAt: new Date().toISOString() }));
		} catch {
			return 'error';
		}
		heldLock = true;
		return 'claimed';
	}

	// releaseLock removes the lock only when it still belongs to this tab,
	// so an outcome here can never clear a lock another tab took over. A
	// corrupt record is left untouched: nothing is silently discarded.
	function releaseLock() {
		try {
			const existing = readLock();
			if (existing && existing !== 'corrupt' && existing.tabId === tabId) {
				store().removeItem(lockStorageKey());
			}
		} catch {
			// ignore
		}
		heldLock = false;
	}

	// releaseHeldLock is the best-effort beforeunload cleanup: it drops the
	// lock only if this tab owns one. The pending journal survives either
	// way, so recovery after a restart is unaffected.
	function releaseHeldLock() {
		if (heldLock) {
			releaseLock();
		}
	}

	// takeOverLock is the explicit operator action that claims a STALE lock
	// held by another tab. It re-validates staleness immediately before
	// writing so a live lock can never be stolen. The tab that held the lock
	// is not orphaned silently: it learns about the loss through the
	// storage event and blocks editing with an explanatory state.
	function takeOverLock() {
		lockTakeoverOffered = false;
		let existing: PoolSettingsLock | null | 'corrupt';
		try {
			existing = readLock();
		} catch {
			return;
		}
		if (existing === 'corrupt') {
			recoveryError =
				'Recovery data unreadable: the multi-tab lock record for this pool is corrupt. Your change was NOT sent. Nothing has been deleted; clear this pool\u2019s experimental site data to recover.';
			return;
		}
		if (!existing || existing.tabId === tabId || !lockIsStale(existing)) {
			return;
		}
		try {
			store().setItem(lockStorageKey(), JSON.stringify({ tabId, acquiredAt: new Date().toISOString() }));
		} catch {
			return;
		}
		heldLock = true;
	}

	// onLockStorageEvent lets a tab detect that its lock was removed or
	// taken over while it still believed it owned it (stale takeover by
	// another tab, or a storage wipe). It blocks editing with an explanatory
	// state instead of letting two tabs write concurrently. Storage events
	// fire only in OTHER documents, so a tab's own writes never trigger this.
	function onLockStorageEvent(ev: StorageEvent) {
		if (ev.key !== null && ev.key !== lockStorageKey()) return;
		if (!heldLock) return;
		let next: PoolSettingsLock | null | 'corrupt' = null;
		if (ev.newValue) {
			try {
				next = parseLock(ev.newValue) ?? 'corrupt';
			} catch {
				next = 'corrupt';
			}
		}
		if (next === 'corrupt' || !next || next.tabId !== tabId) {
			heldLock = false;
			lockLost = true;
		}
	}

	// refuseClaim routes a failed lock claim: a live foreign lock blocks the
	// write with an explanation; a stale one offers the explicit takeover; a
	// corrupt record latches the persistent recovery error (fail closed).
	function refuseClaim(claim: 'blocked' | 'stale' | 'corrupt' | 'error') {
		lockTakeoverOffered = claim === 'stale';
		if (claim === 'corrupt') {
			recoveryError =
				'Recovery data unreadable: the multi-tab lock record for this pool is corrupt. Your change was NOT sent. Nothing has been deleted; clear this pool\u2019s experimental site data to recover.';
			return;
		}
		if (claim === 'error') {
			errorMessage = 'Browser storage rejected the lock write; your change was NOT sent.';
			return;
		}
		errorMessage =
			claim === 'stale'
				? 'Another tab holds an unresolved write for this pool, but its lock is stale (older than 5 minutes), so that tab is likely gone. Your change was NOT sent. Use "Take over lock" only if that tab is gone, then save again.'
				: 'Another tab has an unresolved write for this pool; resolve it there first. Your change was NOT sent.';
	}

	// isPendingRequest validates a journaled record strictly. Recovery must
	// fail closed: a record under this pool's key that does not match the
	// schema is corrupt evidence, not "nothing pending".
	function isPendingRequest(rec: unknown): rec is PendingRequest {
		if (!rec || typeof rec !== 'object' || Array.isArray(rec)) return false;
		const r = rec as Record<string, unknown>;
		if (typeof r.requestId !== 'string' || r.requestId === '') return false;
		if (typeof r.tabId !== 'string' || r.tabId === '') return false;
		if (typeof r.etag !== 'string' || r.etag === '') return false;
		if (typeof r.poolID !== 'string' || r.poolID !== pool.id) return false;
		if (!r.payload || typeof r.payload !== 'object' || Array.isArray(r.payload)) return false;
		const d = r.draft;
		if (!d || typeof d !== 'object') return false;
		const draft = d as Record<string, unknown>;
		return (
			typeof draft.priority === 'number' &&
			typeof draft.max_runners === 'number' &&
			typeof draft.min_idle_runners === 'number'
		);
	}

	// restorePending fails closed. An absent key means "nothing pending"
	// (null), but a present-yet-unreadable record — unparseable JSON or a
	// wrong-shaped document — THROWS, so the caller surfaces a persistent
	// error state instead of silently discarding recovery data.
	function restorePending(): PendingRequest | null {
		const raw = store().getItem(pendingStorageKey());
		if (!raw) return null;
		const rec = JSON.parse(raw) as unknown;
		if (!isPendingRequest(rec)) {
			throw new Error('malformed pending record');
		}
		return rec;
	}

	$: ref = deriveRef(pool);

	function deriveRef(p: Pool): PoolSettingsRef | null {
		if (!p.id) return null;
		if (p.repo_id) return { entityType: 'repository', entityID: p.repo_id, poolID: p.id };
		if (p.org_id) return { entityType: 'organization', entityID: p.org_id, poolID: p.id };
		if (p.enterprise_id) return { entityType: 'enterprise', entityID: p.enterprise_id, poolID: p.id };
		return null;
	}

	async function load(preserveDraft: boolean) {
		if (!ref) return;
		if (preserveDraft) {
			refreshing = true;
		} else {
			loading = true;
		}
		loadError = '';
		try {
			const res = await getPoolSettings(ref);
			serverValues = res.settings;
			etag = res.etag;
			if (!preserveDraft && !pendingRequest) {
				// First load seeds the draft from the server document —
				// unless an unresolved request is being recovered, in which
				// case the journaled draft wins.
				draft = { ...res.settings };
			}
			applied = false;
			conflictEtag = null;
			// A successful GET after the conflict satisfies the reconcile
			// precondition: the displayed values are now this fresh read's.
			if (conflictLatched) {
				reconcileFreshRead = true;
			}
			// unknownHeld is intentionally NOT cleared here: a refresh does
			// not resolve an unresolved write. It clears only through
			// resolve() after a definitive outcome. conflictLatched is also
			// NOT cleared: only the operator's "Mark reconciled" does.
			errorMessage = '';
		} catch (err) {
			loadError = describeError(err);
		} finally {
			loading = false;
			refreshing = false;
		}
	}

	function describeError(err: unknown): string {
		if (err instanceof PoolSettingsError) {
			return err.message;
		}
		return err instanceof Error ? err.message : String(err);
	}

	async function save() {
		if (!ref || !etag || !serverValues || !pool.id) return;
		if (lockLost) {
			errorMessage = 'This tab lost the multi-tab write lock; close this editor and use the tab that took over.';
			return;
		}
		if (recoveryError) {
			// Fail closed: recovery data is unreadable, so saving stays
			// blocked until the operator fixes storage.
			return;
		}
		if (unknownHeld || pendingRequest) {
			// An unresolved write blocks further writes until its outcome
			// is resolved through the durable receipt.
			errorMessage = 'An earlier save is still unresolved. Use "Resolve outcome" before saving again.';
			return;
		}
		if (conflictLatched) {
			// A recorded conflict blocks saving until the operator has
			// deliberately reconciled against the read-only current values.
			errorMessage =
				'Saving is blocked until the recorded conflict is reconciled. Review the read-only current values, then press "Mark reconciled".';
			return;
		}
		const patch: Partial<PoolSettings> = {};
		if (draft.priority !== serverValues.priority) patch.priority = draft.priority;
		if (draft.max_runners !== serverValues.max_runners) patch.max_runners = draft.max_runners;
		if (draft.min_idle_runners !== serverValues.min_idle_runners) patch.min_idle_runners = draft.min_idle_runners;
		if (Object.keys(patch).length === 0) {
			errorMessage = 'No changes to apply.';
			return;
		}

		// The multi-tab lock is claimed first so a refused save never
		// clobbers another tab's journaled request for this pool, then the
		// pending record (request id, tab identity, payload AND full draft)
		// is journaled before dispatch so an UNKNOWN outcome survives a
		// browser restart. The claim is a synchronous read-then-write: no
		// other tab can interleave inside it.
		const claim = claimLock();
		if (claim !== 'claimed') {
			refuseClaim(claim);
			return;
		}
		lockTakeoverOffered = false;
		saving = true;
		applied = false;
		conflictEtag = null;
		errorMessage = '';
		const rec: PendingRequest = {
			requestId: crypto.randomUUID(),
			tabId,
			payload: patch,
			draft: { ...draft },
			etag,
			poolID: pool.id,
			at: new Date().toISOString()
		};
		pendingRequest = rec;
		if (!persistPending(rec)) {
			// Fail closed: nothing was dispatched, the in-memory hold is
			// dropped and the lock is released; the operator must fix
			// storage and try again.
			pendingRequest = null;
			releaseLock();
			saving = false;
			errorMessage =
				'Your change was NOT sent: the pending-request journal could not be saved to browser storage. Fix the storage problem (e.g. free quota) and try again.';
			return;
		}
		try {
			await dispatchPatch(rec.requestId, patch, etag);
		} finally {
			saving = false;
		}
	}

	// dispatchPatch issues exactly one PATCH and routes every outcome class.
	// Shared by save() and by the deliberate resolve of an unresolved
	// request; it never retries on its own. The multi-tab lock is released
	// on every definitive outcome; UNKNOWN keeps it (recovery re-enters
	// under the same request id).
	async function dispatchPatch(requestId: string, patch: Partial<PoolSettings>, precondition: string) {
		if (!ref) return;
		try {
			const res = await patchPoolSettings(ref, precondition, patch, requestId);
			if (res.replayed) {
				// The server answered from its durable receipt: this exact
				// request already applied earlier. The outcome is now known;
				// adopt the recorded ETag and re-read deliberately.
				clearPending();
				releaseLock();
				unknownHeld = false;
				etag = res.etag;
				await load(true);
				applied = true;
				return;
			}
			if (!res.settings) {
				throw new Error('applied response carried no settings document');
			}
			clearPending();
			releaseLock();
			unknownHeld = false;
			// Applied: the server document is the new baseline and the draft is
			// cleared (it equals the applied document).
			serverValues = res.settings;
			etag = res.etag;
			draft = { ...res.settings };
			applied = true;
		} catch (err) {
			if (err instanceof PoolSettingsError && err.kind === 'conflict') {
				// 412 proves non-effect. For a resolve() re-presentation it
				// additionally proves the original unresolved request did not
				// commit (a committed request would have left a durable
				// receipt and been answered by replay). Definitive either way.
				clearPending();
				releaseLock();
				unknownHeld = false;
				// Keep the draft exactly as the operator typed it, and adopt
				// the fresh ETag so a deliberate re-save is preconditioned on
				// it (a manual re-read still precedes a truly reconciled save).
				conflictEtag = err.etag;
				conflictLatched = true;
				// Reconcile requires a fresh successful GET from here on.
				reconcileFreshRead = false;
				if (err.etag) {
					etag = err.etag;
				}
			} else if (err instanceof PoolSettingsError && err.kind === 'rejected') {
				clearPending();
				releaseLock();
				unknownHeld = false;
				errorMessage = describeError(err);
			} else if (err instanceof PoolSettingsError && err.kind === 'unknown') {
				// Lost or ambiguous response after dispatch: keep the journal
				// and the hold (and the lock). Never auto-retry.
				unknownHeld = true;
			} else {
				errorMessage = describeError(err);
			}
		}
	}

	// resolve deliberately re-presents the journaled request so the
	// server's durable receipt turns the UNKNOWN into a definitive outcome.
	// It never submits new content. It re-enters under the journaled tab
	// identity, so a lock still held from the original dispatch does not
	// self-deadlock; only a lock owned by a different tab refuses the
	// resolve.
	async function resolve() {
		if (!ref || !pendingRequest || saving || refreshing) return;
		const claim = claimLock();
		if (claim !== 'claimed') {
			refuseClaim(claim);
			return;
		}
		lockTakeoverOffered = false;
		saving = true;
		applied = false;
		conflictEtag = null;
		errorMessage = '';
		try {
			await dispatchPatch(pendingRequest.requestId, pendingRequest.payload, pendingRequest.etag);
		} finally {
			saving = false;
		}
	}

	// refresh re-reads the settings document. It never auto-submits a patch.
	async function refresh() {
		await load(true);
	}

	function markReconciled() {
		// Deliberate operator action: only this clears the conflict latch,
		// and only once a fresh GET after the conflict has completed — the
		// button is disabled until then.
		if (!reconcileFreshRead) return;
		conflictLatched = false;
	}

	onMount(() => {
		try {
			const rec = restorePending();
			if (rec) {
				// Recover an unresolved write from before the restart: re-enter
				// the UNKNOWN hold, restore the operator's full journaled
				// draft (every field, not only the patched ones) and adopt
				// the journaled tab identity so resolving re-enters the lock
				// this tab held before the restart.
				pendingRequest = rec;
				unknownHeld = true;
				draft = { ...rec.draft };
				tabId = rec.tabId;
			}
		} catch {
			// Fail closed: a malformed pending record is corrupt recovery
			// evidence, not "nothing pending". Nothing is discarded; Save
			// stays disabled until the operator fixes storage.
			recoveryError =
				'Recovery data unreadable: the pending-request journal for this pool is corrupt or has the wrong shape. Saving is disabled. Nothing has been deleted; clear this pool\u2019s experimental site data to recover.';
		}
		if (!recoveryError) {
			// Same fail-closed treatment for a corrupt lock record.
			try {
				if (readLock() === 'corrupt') {
					recoveryError =
						'Recovery data unreadable: the multi-tab lock record for this pool is corrupt. Saving is disabled. Nothing has been deleted; clear this pool\u2019s experimental site data to recover.';
				}
			} catch {
				// Treat an unreadable key as absent; claim time fails closed.
			}
		}
		load(false);
		window.addEventListener('beforeunload', releaseHeldLock);
		window.addEventListener('storage', onLockStorageEvent);
		return () => {
			window.removeEventListener('beforeunload', releaseHeldLock);
			window.removeEventListener('storage', onLockStorageEvent);
		};
	});
</script>

<div class="max-w-6xl w-full max-h-[90vh] overflow-y-auto">
	<div class="px-6 py-4 border-b border-gray-200 dark:border-gray-700">
		<h2 class="text-xl font-semibold text-gray-900 dark:text-white">
			Update Pool Settings {pool.id} (experimental)
		</h2>
	</div>

	<div class="p-6 space-y-6">
		{#if loading}
			<div class="flex items-center text-sm text-gray-600 dark:text-gray-400">
				<div class="animate-spin rounded-full h-4 w-4 border-b-2 border-blue-600 mr-2"></div>
				Loading pool settings...
			</div>
		{:else if loadError}
			<div class="rounded-md bg-red-50 dark:bg-red-900 p-4">
				<p class="text-sm font-medium text-red-800 dark:text-red-200">{loadError}</p>
			</div>
			<div class="flex justify-end">
				<button
					type="button"
					on:click={() => load(false)}
					class="px-4 py-2 border border-gray-300 dark:border-gray-600 rounded-md shadow-sm text-sm font-medium text-gray-700 dark:text-gray-300 bg-white dark:bg-gray-700 hover:bg-gray-50 dark:hover:bg-gray-600 cursor-pointer"
				>
					Retry
				</button>
			</div>
		{:else if !ref}
			<div class="rounded-md bg-yellow-50 dark:bg-yellow-900 p-4">
				<p class="text-sm font-medium text-yellow-800 dark:text-yellow-200">
					This pool is not attached to a repository, organization or enterprise; the experimental settings editor cannot address it.
				</p>
			</div>
		{:else}
			{#if applied}
				<div class="rounded-md bg-green-50 dark:bg-green-900 p-4" role="status">
					<p class="text-sm font-medium text-green-800 dark:text-green-200">Settings applied. Draft cleared.</p>
					{#if etag}
						<p class="text-sm text-green-800 dark:text-green-200 mt-1">New ETag: {etag}</p>
					{/if}
				</div>
			{/if}

			{#if conflictEtag}
				<div class="rounded-md bg-yellow-50 dark:bg-yellow-900 p-4" role="alert">
					<p class="text-sm font-medium text-yellow-800 dark:text-yellow-200">
						Conflict (412): the settings changed elsewhere. Your edits below are preserved and were not applied.
					</p>
					<p class="text-sm text-yellow-800 dark:text-yellow-200 mt-1">
						Fresh ETag: {conflictEtag}. Review the read-only current values below, reconcile your draft, then press "Mark reconciled" to re-enable saving.
					</p>
				</div>
			{/if}

			{#if unknownHeld}
				<div class="rounded-md bg-orange-50 dark:bg-orange-900 p-4" role="alert">
					<p class="text-sm font-medium text-orange-800 dark:text-orange-200">
						UNKNOWN: the outcome of a save could not be determined. The request was dispatched, but the response was lost or ambiguous.
					</p>
					<p class="text-sm text-orange-800 dark:text-orange-200 mt-1">
						Your edits below are held and have not been confirmed applied. Saving is blocked until the outcome is resolved: use "Resolve outcome" to re-present the exact journaled request (id {pendingRequest?.requestId ?? 'unknown'}) and read the server's durable receipt. A re-read alone does not clear this state.
					</p>
				</div>
			{/if}

			{#if errorMessage}
				<div class="rounded-md bg-red-50 dark:bg-red-900 p-4" role="alert">
					<p class="text-sm font-medium text-red-800 dark:text-red-200">{errorMessage}</p>
				</div>
			{/if}

			{#if recoveryError}
				<div class="rounded-md bg-red-50 dark:bg-red-900 p-4" role="alert">
					<p class="text-sm font-medium text-red-800 dark:text-red-200">{recoveryError}</p>
				</div>
			{/if}

			{#if lockLost}
				<div class="rounded-md bg-red-50 dark:bg-red-900 p-4" role="alert">
					<p class="text-sm font-medium text-red-800 dark:text-red-200">
						This tab lost the multi-tab write lock to another tab (stale-lock takeover). Editing is blocked here so two tabs can never write concurrently. Close this editor, or reload it after the other tab has finished.
					</p>
				</div>
			{/if}

			{#if lockTakeoverOffered}
				<div class="rounded-md bg-yellow-50 dark:bg-yellow-900 p-4" role="alert">
					<p class="text-sm font-medium text-yellow-800 dark:text-yellow-200">
						The multi-tab lock for this pool is held by another tab but is stale (older than 5 minutes), so that tab is likely gone. Taking over blocks that tab from editing. Take over only if you are sure the other tab is gone.
					</p>
					<div class="mt-3">
						<button
							type="button"
							on:click={takeOverLock}
							class="px-4 py-2 border border-transparent rounded-md shadow-sm text-sm font-medium text-white bg-yellow-600 hover:bg-yellow-700 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-yellow-500 cursor-pointer"
						>
							Take over lock
						</button>
					</div>
				</div>
			{/if}

			<!-- Current server document (read-only reconcile aid) -->
			{#if serverValues}
				<div class="bg-gray-50 dark:bg-gray-900 p-4 rounded-lg">
					<h3 class="text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">
						Current server values (read-only)
						{#if refreshing}
							<span class="text-xs text-gray-500 dark:text-gray-400">(refreshing…)</span>
						{/if}
					</h3>
					<div class="grid grid-cols-1 md:grid-cols-4 gap-4 text-sm">
						<div class="flex">
							<span class="text-gray-500 dark:text-gray-400 w-28 flex-shrink-0">Priority:</span>
							<span class="text-gray-900 dark:text-white">{serverValues.priority}</span>
						</div>
						<div class="flex">
							<span class="text-gray-500 dark:text-gray-400 w-28 flex-shrink-0">Max runners:</span>
							<span class="text-gray-900 dark:text-white">{serverValues.max_runners}</span>
						</div>
						<div class="flex">
							<span class="text-gray-500 dark:text-gray-400 w-28 flex-shrink-0">Min idle:</span>
							<span class="text-gray-900 dark:text-white">{serverValues.min_idle_runners}</span>
						</div>
						<div class="flex">
							<span class="text-gray-500 dark:text-gray-400 w-28 flex-shrink-0">ETag:</span>
							<span class="text-gray-900 dark:text-white">{etag ?? 'not provided by server'}</span>
						</div>
					</div>
				</div>
			{/if}

			<!-- Draft editor -->
			<div class="space-y-4">
				<h3 class="text-lg font-medium text-gray-900 dark:text-white border-b border-gray-200 dark:border-gray-700 pb-2">
					Your edits (draft)
				</h3>
				<div class="grid grid-cols-1 md:grid-cols-3 gap-4">
					<div>
						<label for="expMinIdleRunners" class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">
							Min Idle Runners
						</label>
						<input
							id="expMinIdleRunners"
							type="number"
							bind:value={draft.min_idle_runners}
							on:change={(e) => (draft.min_idle_runners = (e.currentTarget as HTMLInputElement).valueAsNumber || 0)}
							min="0"
							disabled={saving || unknownHeld || lockLost}
							class="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500 bg-white dark:bg-gray-700 text-gray-900 dark:text-white disabled:opacity-50"
						/>
					</div>
					<div>
						<label for="expMaxRunners" class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">
							Max Runners
						</label>
						<input
							id="expMaxRunners"
							type="number"
							bind:value={draft.max_runners}
							on:change={(e) => (draft.max_runners = (e.currentTarget as HTMLInputElement).valueAsNumber || 0)}
							min="1"
							disabled={saving || unknownHeld || lockLost}
							class="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500 bg-white dark:bg-gray-700 text-gray-900 dark:text-white disabled:opacity-50"
						/>
					</div>
					<div>
						<label for="expPriority" class="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-2">
							Priority
						</label>
						<input
							id="expPriority"
							type="number"
							bind:value={draft.priority}
							on:change={(e) => (draft.priority = (e.currentTarget as HTMLInputElement).valueAsNumber || 0)}
							min="1"
							disabled={saving || unknownHeld || lockLost}
							class="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-md shadow-sm focus:outline-none focus:ring-blue-500 focus:border-blue-500 bg-white dark:bg-gray-700 text-gray-900 dark:text-white disabled:opacity-50"
						/>
					</div>
				</div>
				<p class="text-xs text-gray-500 dark:text-gray-400">
					Saves are conditional: the server only applies them if the settings still match the listed ETag. Conflicting or ambiguous saves never lose your edits.
				</p>
			</div>

			<!-- Action Buttons -->
			<div class="flex justify-end space-x-3 pt-6 border-t border-gray-200 dark:border-gray-700">
				<button
					type="button"
					on:click={() => dispatch('close')}
					class="px-4 py-2 border border-gray-300 dark:border-gray-600 rounded-md shadow-sm text-sm font-medium text-gray-700 dark:text-gray-300 bg-white dark:bg-gray-700 hover:bg-gray-50 dark:hover:bg-gray-600 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-blue-500 cursor-pointer"
				>
					Cancel
				</button>
				<button
					type="button"
					on:click={refresh}
					disabled={saving || refreshing}
					class="px-4 py-2 border border-gray-300 dark:border-gray-600 rounded-md shadow-sm text-sm font-medium text-gray-700 dark:text-gray-300 bg-white dark:bg-gray-700 hover:bg-gray-50 dark:hover:bg-gray-600 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-blue-500 disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
				>
					{#if refreshing}
						Refreshing...
					{:else}
						Re-read settings
					{/if}
				</button>
				{#if conflictLatched}
					<button
						type="button"
						on:click={markReconciled}
						disabled={!reconcileFreshRead || saving || refreshing}
						class="px-4 py-2 border border-transparent rounded-md shadow-sm text-sm font-medium text-white bg-yellow-600 hover:bg-yellow-700 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-yellow-500 disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
					>
						Mark reconciled
					</button>
				{/if}
				{#if unknownHeld}
					<button
						type="button"
						on:click={resolve}
						disabled={saving || refreshing || !pendingRequest}
						class="px-4 py-2 border border-transparent rounded-md shadow-sm text-sm font-medium text-white bg-orange-600 hover:bg-orange-700 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-orange-500 disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
					>
						Resolve outcome
					</button>
				{/if}
				<button
					type="button"
					on:click={save}
					disabled={saving || refreshing || !etag || unknownHeld || conflictLatched || !!recoveryError || lockLost}
					class="px-4 py-2 border border-transparent rounded-md shadow-sm text-sm font-medium text-white bg-blue-600 hover:bg-blue-700 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-blue-500 disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
				>
					{#if saving}
						<div class="flex items-center">
							<div class="animate-spin rounded-full h-4 w-4 border-b-2 border-white mr-2"></div>
							Saving...
						</div>
					{:else}
						Save changes
					{/if}
				</button>
			</div>
		{/if}
	</div>
</div>
