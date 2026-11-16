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
	let unknownHeld = false;
	let errorMessage = '';

	// Client-side durable journal of a dispatched-but-unresolved write. The
	// exact request id, payload, full draft and precondition are persisted
	// before dispatch and cleared on any definitive outcome, so a browser
	// restart recovers the UNKNOWN state instead of silently dropping it.
	// The journal is kept per pool so concurrent editors of different pools
	// can never overwrite or clear each other's pending request. The full
	// draft is journaled alongside the patch so recovery restores every
	// field the operator saw, not just the changed ones.
	interface PendingRequest {
		requestId: string;
		payload: Partial<PoolSettings>;
		draft: PoolSettings;
		etag: string;
		poolID: string;
		at: string;
	}
	let pendingRequest: PendingRequest | null = null;

	// The request id currently holding this tab's multi-tab lock, if any.
	// Tracked so the beforeunload handler (and only the owner) can release
	// the lock without touching a lock another tab holds.
	let heldLockRequestId: string | null = null;

	function pendingStorageKey(): string {
		return `garm.expPoolSettings.pending.${pool.id}`;
	}

	function lockStorageKey(): string {
		return `garm.expPoolSettings.lock.${pool.id}`;
	}

	// persistPending is fail-closed: it reports whether the journal actually
	// reached localStorage so save() can abort BEFORE dispatching when the
	// pending record would otherwise be lost (and the UNKNOWN hold with it).
	function persistPending(rec: PendingRequest): boolean {
		try {
			localStorage.setItem(pendingStorageKey(), JSON.stringify(rec));
			return true;
		} catch {
			return false;
		}
	}

	function clearPending() {
		pendingRequest = null;
		try {
			localStorage.removeItem(pendingStorageKey());
		} catch {
			// ignore
		}
	}

	// acquireLock is the multi-tab guard around a write to this pool. An
	// existing lock with a DIFFERENT request id means another tab has an
	// unresolved write here; the save is refused. Re-entering with the SAME
	// request id (a deliberate resolve after UNKNOWN) refreshes the lock
	// instead of deadlocking against it.
	function acquireLock(requestId: string): boolean {
		try {
			const raw = localStorage.getItem(lockStorageKey());
			if (raw) {
				const existing = JSON.parse(raw) as { requestId?: string } | null;
				if (existing && existing.requestId && existing.requestId !== requestId) {
					return false;
				}
			}
			localStorage.setItem(lockStorageKey(), JSON.stringify({ requestId, at: new Date().toISOString() }));
			heldLockRequestId = requestId;
			return true;
		} catch {
			return false;
		}
	}

	// releaseLock removes the lock only when it still belongs to the given
	// request, so an outcome here can never clear another tab's lock.
	function releaseLock(requestId: string) {
		try {
			const raw = localStorage.getItem(lockStorageKey());
			if (raw) {
				const existing = JSON.parse(raw) as { requestId?: string } | null;
				if (!existing || !existing.requestId || existing.requestId === requestId) {
					localStorage.removeItem(lockStorageKey());
				}
			}
		} catch {
			// ignore
		}
		if (heldLockRequestId === requestId) {
			heldLockRequestId = null;
		}
	}

	// releaseHeldLock is the best-effort beforeunload cleanup: it drops the
	// lock only if this tab owns one. The pending journal survives either
	// way, so recovery after a restart is unaffected.
	function releaseHeldLock() {
		if (heldLockRequestId) {
			releaseLock(heldLockRequestId);
		}
	}

	function restorePending(): PendingRequest | null {
		try {
			const raw = localStorage.getItem(pendingStorageKey());
			if (!raw) return null;
			const rec = JSON.parse(raw) as PendingRequest;
			if (!rec || !rec.requestId || !rec.payload || rec.poolID !== pool.id) return null;
			if (
				!rec.draft ||
				typeof rec.draft.priority !== 'number' ||
				typeof rec.draft.max_runners !== 'number' ||
				typeof rec.draft.min_idle_runners !== 'number'
			) {
				return null;
			}
			return rec;
		} catch {
			return null;
		}
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
		if (!ref || !etag || !serverValues) return;
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

		// Persistent request identity. The multi-tab lock is taken first so
		// a refused save never clobbers another tab's journaled request for
		// this pool, then the pending record (request id, payload AND full
		// draft) is journaled before dispatch so an UNKNOWN outcome survives
		// a browser restart.
		const requestId = crypto.randomUUID();
		if (!acquireLock(requestId)) {
			errorMessage =
				'Another tab has an unresolved write for this pool; resolve it there first. Your change was NOT sent.';
			return;
		}
		saving = true;
		applied = false;
		conflictEtag = null;
		errorMessage = '';
		const rec: PendingRequest = {
			requestId,
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
			releaseLock(requestId);
			saving = false;
			errorMessage =
				'Your change was NOT sent: the pending-request journal could not be saved to browser storage. Fix the storage problem (e.g. free quota) and try again.';
			return;
		}
		try {
			await dispatchPatch(requestId, patch, etag);
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
				releaseLock(requestId);
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
			releaseLock(requestId);
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
				releaseLock(requestId);
				unknownHeld = false;
				// Keep the draft exactly as the operator typed it, and adopt
				// the fresh ETag so a deliberate re-save is preconditioned on
				// it (a manual re-read still precedes a truly reconciled save).
				conflictEtag = err.etag;
				conflictLatched = true;
				if (err.etag) {
					etag = err.etag;
				}
			} else if (err instanceof PoolSettingsError && err.kind === 'rejected') {
				clearPending();
				releaseLock(requestId);
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
	// It never submits new content. It re-enters under the journaled request
	// id, so a lock still held from the original dispatch does not
	// self-deadlock; only a lock owned by a different request (another tab)
	// refuses the resolve.
	async function resolve() {
		if (!ref || !pendingRequest || saving || refreshing) return;
		if (!acquireLock(pendingRequest.requestId)) {
			errorMessage =
				'Another tab has an unresolved write for this pool; resolve it there first. Your change was NOT sent.';
			return;
		}
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
		// Deliberate operator action: only this clears the conflict latch.
		conflictLatched = false;
	}

	onMount(() => {
		const rec = restorePending();
		if (rec) {
			// Recover an unresolved write from before the restart: re-enter
			// the UNKNOWN hold and restore the operator's full journaled
			// draft (every field, not only the patched ones).
			pendingRequest = rec;
			unknownHeld = true;
			draft = { ...rec.draft };
		}
		load(false);
		window.addEventListener('beforeunload', releaseHeldLock);
		return () => {
			window.removeEventListener('beforeunload', releaseHeldLock);
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
							disabled={saving || unknownHeld}
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
							disabled={saving || unknownHeld}
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
							disabled={saving || unknownHeld}
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
						class="px-4 py-2 border border-transparent rounded-md shadow-sm text-sm font-medium text-white bg-yellow-600 hover:bg-yellow-700 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-yellow-500 cursor-pointer"
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
					disabled={saving || refreshing || !etag || unknownHeld || conflictLatched}
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
