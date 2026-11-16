import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/svelte';
import ExperimentalPoolSettingsEditor from './ExperimentalPoolSettingsEditor.svelte';
import { createMockPool } from '../../test/factories.js';

// Mock the generated API client: the editor speaks the provisional interface
// through fetch, but reads its bearer token from garmApi.
vi.mock('$lib/api/client.js', () => ({
	garmApi: {
		setToken: vi.fn(),
		getToken: vi.fn(() => 'tok-123'),
		listTemplates: vi.fn().mockResolvedValue([]),
		updateRepository: vi.fn(),
		updateOrganization: vi.fn(),
		updateEnterprise: vi.fn()
	}
}));

function jsonResponse(status: number, body: unknown, etag?: string): Response {
	const headers: Record<string, string> = { 'Content-Type': 'application/json' };
	if (etag) headers['ETag'] = etag;
	return new Response(JSON.stringify(body), { status, headers });
}

function priorityInput(): HTMLInputElement {
	return screen.getByLabelText('Priority') as HTMLInputElement;
}

const PENDING_KEY = 'garm.expPoolSettings.pending.pool-9';
const LOCK_KEY = 'garm.expPoolSettings.lock.pool-9';
const OTHER_PENDING_KEY = 'garm.expPoolSettings.pending.pool-other';

describe('ExperimentalPoolSettingsEditor', () => {
	beforeEach(() => {
		localStorage.clear();
	});
	afterEach(() => {
		localStorage.clear();
		vi.unstubAllEnvs();
		vi.unstubAllGlobals();
	});

	async function renderEditor() {
		const component = render(ExperimentalPoolSettingsEditor, {
			props: { pool: createMockPool({ id: 'pool-9', repo_id: 'repo-1' }) }
		});
		await waitFor(() => expect(priorityInput()).toBeInTheDocument());
		return component;
	}

	it('loads settings, keeps the ETag and seeds the draft', async () => {
		const fetchMock = vi.fn().mockResolvedValue(
			jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"')
		);
		vi.stubGlobal('fetch', fetchMock);

		await renderEditor();

		expect(priorityInput().value).toBe('10');
		expect((screen.getByLabelText('Max Runners') as HTMLInputElement).value).toBe('5');
		expect(screen.getByText('"42:7"')).toBeInTheDocument();
	});

	it('412 latches Save shut until the operator presses "Mark reconciled"', async () => {
		const seenAuth: Array<string | null> = [];
		const ifMatchHeaders: Array<string | null> = [];
		let getCount = 0;
		vi.stubGlobal('fetch', async (url: unknown, init?: RequestInit) => {
			const method = init?.method ?? 'GET';
			const headers = (init?.headers as Record<string, string>) ?? {};
			seenAuth.push(headers['Authorization'] ?? null);
			if (method === 'PATCH') {
				const ifMatch = headers['If-Match'] ?? null;
				ifMatchHeaders.push(ifMatch);
				if (ifMatch === '"42:7"') {
					return jsonResponse(412, { detail: 'stale' }, '"99:1"');
				}
				return jsonResponse(200, { priority: 77, max_runners: 5, min_idle_runners: 1 }, '"99:2"');
			}
			getCount += 1;
			if (getCount === 1) {
				return jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"');
			}
			return jsonResponse(200, { priority: 11, max_runners: 5, min_idle_runners: 1 }, '"99:9"');
		});

		await renderEditor();

		await fireEvent.input(priorityInput(), { target: { value: '77' } });
		await fireEvent.click(await screen.findByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(screen.getByText(/Conflict \(412\)/)).toBeInTheDocument());
		// draft preserved in the inputs
		expect(priorityInput().value).toBe('77');
		// fresh ETag recorded
		expect(screen.getByText(/Fresh ETag: "99:1"/)).toBeInTheDocument();

		// Save is latched shut and stays latched across a fresh successful
		// load; only "Mark reconciled" re-enables it. (The button returns to
		// the "Save changes" name a tick after the conflict banner.)
		const saveButton = (await screen.findByRole('button', { name: 'Save changes' })) as HTMLButtonElement;
		await waitFor(() => expect(saveButton.disabled).toBe(true));
		expect(screen.getByRole('button', { name: 'Mark reconciled' })).toBeInTheDocument();
		expect(screen.getByText(/press "Mark reconciled"/)).toBeInTheDocument();

		await fireEvent.click(screen.getByRole('button', { name: 'Re-read settings' }));
		await waitFor(() => expect(screen.getByText('"99:9"')).toBeInTheDocument());
		expect((screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(true);
		expect(screen.getByRole('button', { name: 'Mark reconciled' })).toBeInTheDocument();

		// operator reconciles, then saves again: fresh ETag goes out, auth
		// token intact, applied banner shows.
		await fireEvent.click(screen.getByRole('button', { name: 'Mark reconciled' }));
		expect((screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(false);
		await fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
		await waitFor(() => expect(screen.getByText('Settings applied. Draft cleared.')).toBeInTheDocument());

		expect(ifMatchHeaders).toEqual(['"42:7"', '"99:9"']);
		expect(seenAuth).toEqual(['Bearer tok-123', 'Bearer tok-123', 'Bearer tok-123', 'Bearer tok-123']);
	});

	it('200 clears the draft', async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(
				jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"')
			)
			.mockResolvedValueOnce(
				jsonResponse(200, { priority: 30, max_runners: 6, min_idle_runners: 2 }, '"43:1"')
			);
		vi.stubGlobal('fetch', fetchMock);

		await renderEditor();
		await fireEvent.input(priorityInput(), { target: { value: '30' } });
		await fireEvent.click(await screen.findByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(screen.getByText('Settings applied. Draft cleared.')).toBeInTheDocument());
		// inputs now show the server-confirmed values; no unsent draft remains
		expect(priorityInput().value).toBe('30');
		expect((screen.getByLabelText('Max Runners') as HTMLInputElement).value).toBe('6');
		expect(screen.getByText('"43:1"')).toBeInTheDocument();
	});

	it('UNKNOWN hold: blocks saving, survives re-read, resolves via the durable receipt', async () => {
		localStorage.clear();
		const methods: string[] = [];
		const fetchMock = vi
			.fn()
			.mockImplementationOnce(async () =>
				jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"')
			)
			.mockImplementationOnce(async () => {
				throw new TypeError('connection dropped after dispatch');
			})
			.mockImplementationOnce(async () =>
				jsonResponse(200, { priority: 11, max_runners: 5, min_idle_runners: 1 }, '"50:1"')
			)
			.mockImplementationOnce(async () =>
				jsonResponse(200, { priority: 44, max_runners: 5, min_idle_runners: 1 }, '"50:2"')
			);
		vi.stubGlobal('fetch', async (url: unknown, init?: RequestInit) => {
			methods.push(init?.method ?? 'GET');
			return fetchMock(url, init);
		});

		await renderEditor();
		await fireEvent.input(priorityInput(), { target: { value: '44' } });
		await fireEvent.click(await screen.findByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(screen.getByText(/UNKNOWN:/)).toBeInTheDocument());
		// draft held exactly as the operator left it
		expect(priorityInput().value).toBe('44');
		// exactly one PATCH was dispatched; nothing auto-retried
		expect(methods.filter((m) => m === 'PATCH')).toHaveLength(1);
		// the pending request is journaled under this pool's key for restart
		// recovery, and the multi-tab lock is held for its request id
		const journaled = localStorage.getItem(PENDING_KEY);
		expect(journaled).toContain('"payload"');
		const lock = JSON.parse(localStorage.getItem(LOCK_KEY) ?? 'null') as { requestId?: string } | null;
		expect(lock?.requestId).toBe(JSON.parse(journaled ?? 'null').requestId);
		// saving is blocked until the outcome is resolved
		await waitFor(() =>
			expect((screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(true)
		);

		// a manual re-read does NOT clear the UNKNOWN state
		await fireEvent.click(screen.getByRole('button', { name: 'Re-read settings' }));
		await waitFor(() => expect(screen.getByText('"50:1"')).toBeInTheDocument());
		expect(priorityInput().value).toBe('44');
		expect(methods.filter((m) => m === 'PATCH')).toHaveLength(1);
		expect(screen.getByText(/UNKNOWN:/)).toBeInTheDocument();

		// deliberate resolution re-presents the journaled request under the
		// same request id (no self-deadlock on the lock); the server answers
		// definitively, the hold clears and the lock is released
		await fireEvent.click(screen.getByRole('button', { name: 'Resolve outcome' }));
		await waitFor(() => expect(screen.getByText('Settings applied. Draft cleared.')).toBeInTheDocument());
		expect(screen.queryByText(/UNKNOWN:/)).not.toBeInTheDocument();
		expect(methods.filter((m) => m === 'PATCH')).toHaveLength(2);
		expect(localStorage.getItem(PENDING_KEY)).toBeNull();
		expect(localStorage.getItem(LOCK_KEY)).toBeNull();
	});

	it('recovers an unresolved request across restart from the journal', async () => {
		localStorage.clear();
		localStorage.setItem(
			PENDING_KEY,
			JSON.stringify({
				requestId: 'req-from-before-restart',
				payload: { priority: 44 },
				draft: { priority: 44, max_runners: 5, min_idle_runners: 1 },
				etag: '"42:7"',
				poolID: 'pool-9',
				at: new Date().toISOString()
			})
		);
		const fetchMock = vi.fn().mockResolvedValue(
			jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"60:1"')
		);
		vi.stubGlobal('fetch', fetchMock);

		await renderEditor();

		// The UNKNOWN hold re-enters immediately, the journaled draft is
		// restored and saving stays blocked even though the re-read succeeds.
		await waitFor(() => expect(screen.getByText(/UNKNOWN:/)).toBeInTheDocument());
		expect(priorityInput().value).toBe('44');
		expect((screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(true);
		expect(screen.getByRole('button', { name: 'Resolve outcome' })).toBeInTheDocument();
	});

	it('restores the complete journaled draft on recovery, not just the patch', async () => {
		localStorage.setItem(
			PENDING_KEY,
			JSON.stringify({
				requestId: 'req-full-draft',
				payload: { priority: 44 },
				draft: { priority: 44, max_runners: 9, min_idle_runners: 3 },
				etag: '"42:7"',
				poolID: 'pool-9',
				at: new Date().toISOString()
			})
		);
		const fetchMock = vi.fn().mockResolvedValue(
			jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"60:1"')
		);
		vi.stubGlobal('fetch', fetchMock);

		await renderEditor();

		// Every field comes back from the journaled draft — not the default
		// zeros and not the just-loaded server document.
		await waitFor(() => expect(screen.getByText(/UNKNOWN:/)).toBeInTheDocument());
		expect(priorityInput().value).toBe('44');
		expect((screen.getByLabelText('Max Runners') as HTMLInputElement).value).toBe('9');
		expect((screen.getByLabelText('Min Idle Runners') as HTMLInputElement).value).toBe('3');
	});

	it('isolates pending journals per pool and ignores the legacy shared key', async () => {
		const otherRecord = {
			requestId: 'req-other-pool',
			payload: { priority: 99 },
			draft: { priority: 99, max_runners: 7, min_idle_runners: 2 },
			etag: '"70:1"',
			poolID: 'pool-other',
			at: new Date().toISOString()
		};
		const otherRaw = JSON.stringify(otherRecord);
		localStorage.setItem(OTHER_PENDING_KEY, otherRaw);
		// Legacy shared-key record for THIS pool: must be ignored, not migrated.
		const legacyRaw = JSON.stringify({
			requestId: 'req-legacy-shared-key',
			payload: { priority: 55 },
			draft: { priority: 55, max_runners: 4, min_idle_runners: 1 },
			etag: '"1:1"',
			poolID: 'pool-9',
			at: new Date().toISOString()
		});
		localStorage.setItem('garm.expPoolSettings.pending', legacyRaw);

		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(
				jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"')
			)
			.mockResolvedValueOnce(
				jsonResponse(200, { priority: 30, max_runners: 6, min_idle_runners: 2 }, '"43:1"')
			);
		vi.stubGlobal('fetch', fetchMock);

		await renderEditor();

		// Neither the other pool's journal nor the legacy shared key trigger
		// an UNKNOWN hold, and saving is not blocked by another pool's write.
		expect(screen.queryByText(/UNKNOWN:/)).not.toBeInTheDocument();
		expect((screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(false);

		await fireEvent.input(priorityInput(), { target: { value: '30' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(screen.getByText('Settings applied. Draft cleared.')).toBeInTheDocument());
		// The other pool's journal is byte-for-byte untouched, and the legacy
		// shared key was left alone (no auto-migration).
		expect(localStorage.getItem(OTHER_PENDING_KEY)).toBe(otherRaw);
		expect(localStorage.getItem('garm.expPoolSettings.pending')).toBe(legacyRaw);
		// This pool's journal and lock were cleared by the definitive outcome.
		expect(localStorage.getItem(PENDING_KEY)).toBeNull();
		expect(localStorage.getItem(LOCK_KEY)).toBeNull();
	});

	it('refuses to save while another tab holds the per-pool lock', async () => {
		localStorage.setItem(
			LOCK_KEY,
			JSON.stringify({ requestId: 'req-other-tab', at: new Date().toISOString() })
		);
		const fetchMock = vi.fn().mockResolvedValue(
			jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"')
		);
		vi.stubGlobal('fetch', fetchMock);

		await renderEditor();
		await fireEvent.input(priorityInput(), { target: { value: '30' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));

		await waitFor(() =>
			expect(
				screen.getByText(/Another tab has an unresolved write for this pool; resolve it there first/)
			).toBeInTheDocument()
		);
		// The PATCH was never dispatched and nothing was journaled or held.
		expect(fetchMock).toHaveBeenCalledTimes(1);
		expect(localStorage.getItem(PENDING_KEY)).toBeNull();
		expect(screen.queryByText(/UNKNOWN:/)).not.toBeInTheDocument();
		expect(screen.queryByText('Settings applied. Draft cleared.')).not.toBeInTheDocument();
		// The other tab's lock is untouched.
		expect(localStorage.getItem(LOCK_KEY)).toContain('req-other-tab');
	});

	it('aborts the save before dispatch when the pending journal cannot be written', async () => {
		const fetchMock = vi.fn().mockResolvedValue(
			jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"')
		);
		vi.stubGlobal('fetch', fetchMock);
		const originalSetItem = Storage.prototype.setItem;
		const setItemSpy = vi
			.spyOn(Storage.prototype, 'setItem')
			.mockImplementation(function (this: Storage, key: string, value: string) {
				if (key.startsWith('garm.expPoolSettings.pending.')) {
					throw new Error('quota exceeded');
				}
				return originalSetItem.call(this, key, value);
			});
		try {
			await renderEditor();
			await fireEvent.input(priorityInput(), { target: { value: '30' } });
			await fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));

			await waitFor(() =>
				expect(
					screen.getByText(/Your change was NOT sent: the pending-request journal could not be saved/)
				).toBeInTheDocument()
			);
			// The PATCH was never dispatched: fetch saw only the initial GET.
			expect(fetchMock).toHaveBeenCalledTimes(1);
			// No applied state and no UNKNOWN hold was produced.
			expect(screen.queryByText('Settings applied. Draft cleared.')).not.toBeInTheDocument();
			expect(screen.queryByText(/UNKNOWN:/)).not.toBeInTheDocument();
			// Nothing was journaled and the lock was released again; Save is
			// usable once storage is fixed.
			expect(localStorage.getItem(PENDING_KEY)).toBeNull();
			expect(localStorage.getItem(LOCK_KEY)).toBeNull();
			expect((screen.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(false);
		} finally {
			setItemSpy.mockRestore();
		}
	});
});
