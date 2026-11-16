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
	let unknownHeld = false;
	let errorMessage = '';

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
			if (!preserveDraft) {
				// First load seeds the draft from the server document.
				draft = { ...res.settings };
			}
			applied = false;
			conflictEtag = null;
			unknownHeld = false;
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
		const patch: Partial<PoolSettings> = {};
		if (draft.priority !== serverValues.priority) patch.priority = draft.priority;
		if (draft.max_runners !== serverValues.max_runners) patch.max_runners = draft.max_runners;
		if (draft.min_idle_runners !== serverValues.min_idle_runners) patch.min_idle_runners = draft.min_idle_runners;
		if (Object.keys(patch).length === 0) {
			errorMessage = 'No changes to apply.';
			return;
		}

		saving = true;
		applied = false;
		conflictEtag = null;
		unknownHeld = false;
		errorMessage = '';
		try {
			const res = await patchPoolSettings(ref, etag, patch);
			// Applied: the server document is the new baseline and the draft is
			// cleared (it equals the applied document).
			serverValues = res.settings;
			etag = res.etag;
			draft = { ...res.settings };
			applied = true;
		} catch (err) {
			if (err instanceof PoolSettingsError && err.kind === 'conflict') {
				// 412 proves non-effect. Keep the draft exactly as the operator
				// typed it, and adopt the fresh ETag so a deliberate re-save is
				// preconditioned on it (a manual re-read still precedes a truly
				// reconciled save).
				conflictEtag = err.etag;
				if (err.etag) {
					etag = err.etag;
				}
			} else if (err instanceof PoolSettingsError && err.kind === 'unknown') {
				// Lost or ambiguous response after dispatch: hold in UNKNOWN.
				// Never auto-retry; the operator re-reads manually.
				unknownHeld = true;
			} else {
				errorMessage = describeError(err);
			}
		} finally {
			saving = false;
		}
	}

	// refresh re-reads the settings document. It never auto-submits a patch.
	async function refresh() {
		await load(true);
	}

	onMount(() => {
		load(false);
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
						Fresh ETag: {conflictEtag}. Re-read the current settings, reconcile, then save again.
					</p>
				</div>
			{/if}

			{#if unknownHeld}
				<div class="rounded-md bg-orange-50 dark:bg-orange-900 p-4" role="alert">
					<p class="text-sm font-medium text-orange-800 dark:text-orange-200">
						UNKNOWN: the outcome of the last save could not be determined. The request was dispatched, but the response was lost or ambiguous.
					</p>
					<p class="text-sm text-orange-800 dark:text-orange-200 mt-1">
						Your edits below are held and have not been confirmed applied. Do not auto-retry: re-read the current settings, reconcile, and only then save again.
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
							disabled={saving}
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
							disabled={saving}
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
							disabled={saving}
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
				<button
					type="button"
					on:click={save}
					disabled={saving || refreshing || !etag}
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
