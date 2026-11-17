import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@testing-library/svelte';
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

// In two-tab tests two editors render into one document with duplicate
// element IDs, which breaks label[for] resolution in jsdom; scope by ID.
function priorityInputIn(container: HTMLElement): HTMLInputElement {
	const el = container.querySelector('#expPriority');
	if (!(el instanceof HTMLInputElement)) throw new Error('priority input not found');
	return el;
}

const PENDING_KEY = 'garm.expPoolSettings.pending.pool-9';
const LOCK_KEY = 'garm.expPoolSettings.lock.pool-9';
const OTHER_PENDING_KEY = 'garm.expPoolSettings.pending.pool-other';

// MemoryStorage models the shared localStorage two tabs would see in a real
// browser: jsdom's localStorage is per test file (and per window), so the
// two-tab tests below inject ONE shared instance into both editors.
class MemoryStorage implements Storage {
	private data = new Map<string, string>();
	get length() {
		return this.data.size;
	}
	clear(): void {
		this.data.clear();
	}
	getItem(key: string): string | null {
		return this.data.has(key) ? (this.data.get(key) as string) : null;
	}
	key(index: number): string | null {
		return Array.from(this.data.keys())[index] ?? null;
	}
	removeItem(key: string): void {
		this.data.delete(key);
	}
	setItem(key: string, value: string): void {
		this.data.set(key, String(value));
	}
}

describe('ExperimentalPoolSettingsEditor', () => {
	beforeEach(() => {
		localStorage.clear();
	});
	afterEach(() => {
		localStorage.clear();
		vi.unstubAllEnvs();
		vi.unstubAllGlobals();
	});

	async function renderEditor(opts: { storage?: Storage } = {}) {
		const component = render(ExperimentalPoolSettingsEditor, {
			props: {
				pool: createMockPool({ id: 'pool-9', repo_id: 'repo-1' }),
				...(opts.storage ? { storage: opts.storage } : {})
			}
		});
		// Wait on THIS component's container: bound queries default to
		// document.body, which two-tab tests render into twice. The scoped
		// ID query also sidesteps jsdom's document-wide label[for]
		// resolution breaking on duplicate IDs between the two editors.
		await waitFor(() => expect(priorityInputIn(component.container)).toBeInTheDocument());
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
		// recovery, and the multi-tab lock is held for this tab's identity
		const journaled = localStorage.getItem(PENDING_KEY);
		expect(journaled).toContain('"payload"');
		const lock = JSON.parse(localStorage.getItem(LOCK_KEY) ?? 'null') as {
			tabId?: string;
			acquiredAt?: string;
		} | null;
		expect(lock?.tabId).toBe(JSON.parse(journaled ?? 'null').tabId);
		expect(typeof lock?.acquiredAt).toBe('string');
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
				tabId: 'tab-from-before-restart',
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
				tabId: 'tab-full-draft',
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
			JSON.stringify({ tabId: 'tab-other-tab', acquiredAt: new Date().toISOString() })
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
		expect(localStorage.getItem(LOCK_KEY)).toContain('tab-other-tab');
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

	it('two tabs sharing one storage: interleaved claims yield exactly one owner and a blocked second tab', async () => {
		const shared = new MemoryStorage();
		// Log every lock-key write: a read-then-write claim that could
		// interleave across tabs would show TWO sets (double claim); the
		// component's claim is one synchronous read-check-write block, so
		// exactly one set may ever land.
		const lockSets: string[] = [];
		const origSet = shared.setItem.bind(shared);
		shared.setItem = (key: string, value: string) => {
			if (key === LOCK_KEY) lockSets.push(value);
			return origSet(key, value);
		};

		let resolvePatch: ((r: Response) => void) | null = null;
		let patchCount = 0;
		vi.stubGlobal(
			'fetch',
			vi.fn(async (url: unknown, init?: RequestInit) => {
				if ((init?.method ?? 'GET') === 'PATCH') {
					patchCount += 1;
					return new Promise<Response>((resolve) => {
						resolvePatch = resolve;
					});
				}
				return jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"');
			})
		);

		const a = await renderEditor({ storage: shared });
		const b = await renderEditor({ storage: shared });
		const qa = within(a.container);
		const qb = within(b.container);

		await fireEvent.input(priorityInputIn(a.container), { target: { value: '30' } });
		await fireEvent.input(priorityInputIn(b.container), { target: { value: '31' } });

		// Fire both saves back to back: maximal interleaving opportunity. The
		// claim inside each click handler is synchronous, so tab A's
		// read-check-write completes before tab B's read observes anything.
		const clickA = fireEvent.click(qa.getByRole('button', { name: 'Save changes' }));
		const clickB = fireEvent.click(qb.getByRole('button', { name: 'Save changes' }));
		await Promise.all([clickA, clickB]);

		// Exactly one owner: one lock write total, owned by tab A (the first
		// claimant); tab A journaled its pending request under the same tab
		// identity.
		expect(lockSets).toHaveLength(1);
		const ownerLock = JSON.parse(shared.getItem(LOCK_KEY) ?? 'null') as { tabId?: string };
		const journaled = JSON.parse(shared.getItem(PENDING_KEY) ?? 'null') as { tabId?: string };
		expect(ownerLock.tabId).toBe(journaled.tabId);
		expect(patchCount).toBe(1);

		// Tab B was refused with the blocked state: another tab holds the
		// unresolved request, nothing was dispatched or journaled for B, and
		// no silent takeover was offered.
		await waitFor(() =>
			expect(
				qb.getByText(/Another tab has an unresolved write for this pool; resolve it there first/)
			).toBeInTheDocument()
		);
		expect(qb.queryByRole('button', { name: 'Take over lock' })).toBeNull();
		expect(patchCount).toBe(1);

		// Let tab A's write finish: it applies and releases the lock.
		resolvePatch!(jsonResponse(200, { priority: 30, max_runners: 5, min_idle_runners: 1 }, '"43:1"'));
		await waitFor(() => expect(qa.getByText('Settings applied. Draft cleared.')).toBeInTheDocument());
		expect(shared.getItem(LOCK_KEY)).toBeNull();
	});

	it('a tab that lost ownership never releases the new owner\u2019s lock on outcome', async () => {
		const shared = new MemoryStorage();
		let resolvePatch: ((r: Response) => void) | null = null;
		vi.stubGlobal(
			'fetch',
			vi.fn(async (url: unknown, init?: RequestInit) => {
				if ((init?.method ?? 'GET') === 'PATCH') {
					return new Promise<Response>((resolve) => {
						resolvePatch = resolve;
					});
				}
				return jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"');
			})
		);

		const a = await renderEditor({ storage: shared });
		const b = await renderEditor({ storage: shared });
		const qa = within(a.container);
		const qb = within(b.container);

		// Tab A claims the lock; its PATCH stays in flight.
		await fireEvent.input(priorityInputIn(a.container), { target: { value: '30' } });
		await fireEvent.click(qa.getByRole('button', { name: 'Save changes' }));
		const aLock = JSON.parse(shared.getItem(LOCK_KEY) ?? 'null') as { tabId: string; acquiredAt: string };
		expect(aLock.tabId).toBeTruthy();

		// Age A's lock past the TTL (models a dead tab) and let tab B take it
		// over explicitly while A's write is still in flight.
		shared.setItem(
			LOCK_KEY,
			JSON.stringify({ ...aLock, acquiredAt: new Date(Date.now() - 10 * 60 * 1000).toISOString() })
		);
		await fireEvent.input(priorityInputIn(b.container), { target: { value: '31' } });
		await fireEvent.click(qb.getByRole('button', { name: 'Save changes' }));
		const takeOver = await qb.findByRole('button', { name: 'Take over lock' });
		await fireEvent.click(takeOver);
		const bLock = JSON.parse(shared.getItem(LOCK_KEY) ?? 'null') as { tabId: string; acquiredAt: string };
		expect(bLock.tabId).not.toBe(aLock.tabId);

		// The storage event reaches the former owner (it fires in the other
		// document in production); A blocks editing with the explanatory state.
		window.dispatchEvent(
			new StorageEvent('storage', { key: LOCK_KEY, newValue: shared.getItem(LOCK_KEY) })
		);
		await waitFor(() => expect(qa.getByText(/lost the multi-tab write lock/)).toBeInTheDocument());
		// A's button still shows its in-flight "Saving..." label, hence the
		// regex name; it must be disabled either way.
		expect(
			(qa.getByRole('button', { name: /Save changes|Saving/ }) as HTMLButtonElement).disabled
		).toBe(true);
		expect(priorityInputIn(a.container).disabled).toBe(true);

		// A's in-flight write now returns definitively. A is no longer the
		// owner, so its release attempt must leave B's lock intact.
		resolvePatch!(jsonResponse(200, { priority: 30, max_runners: 5, min_idle_runners: 1 }, '"43:1"'));
		await waitFor(() => expect(qa.getByText('Settings applied. Draft cleared.')).toBeInTheDocument());
		const after = JSON.parse(shared.getItem(LOCK_KEY) ?? 'null') as { tabId?: string } | null;
		expect(after?.tabId).toBe(bLock.tabId);
	});

	it('stale-lock takeover lets another tab write, and the takeover cannot orphan the other tab silently', async () => {
		const shared = new MemoryStorage();
		let patchCount = 0;
		vi.stubGlobal(
			'fetch',
			vi.fn(async (url: unknown, init?: RequestInit) => {
				if ((init?.method ?? 'GET') === 'PATCH') {
					patchCount += 1;
					// Every dispatched write stays UNKNOWN.
					throw new TypeError('connection dropped after dispatch');
				}
				return jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"');
			})
		);

		const a = await renderEditor({ storage: shared });
		const b = await renderEditor({ storage: shared });
		const qa = within(a.container);
		const qb = within(b.container);

		// Tab A saves and ends up in the UNKNOWN hold, keeping the lock.
		await fireEvent.input(priorityInputIn(a.container), { target: { value: '30' } });
		await fireEvent.click(qa.getByRole('button', { name: 'Save changes' }));
		await waitFor(() => expect(qa.getByText(/UNKNOWN:/)).toBeInTheDocument());
		const aLock = JSON.parse(shared.getItem(LOCK_KEY) ?? 'null') as { tabId: string; acquiredAt: string };

		// A fresh foreign lock is refused outright — no takeover offered.
		await fireEvent.input(priorityInputIn(b.container), { target: { value: '31' } });
		await fireEvent.click(qb.getByRole('button', { name: 'Save changes' }));
		await waitFor(() =>
			expect(
				qb.getByText(/Another tab has an unresolved write for this pool; resolve it there first/)
			).toBeInTheDocument()
		);
		expect(qb.queryByRole('button', { name: 'Take over lock' })).toBeNull();

		// Age the lock past the TTL: the refusal now explains the staleness
		// and offers the explicit takeover.
		shared.setItem(
			LOCK_KEY,
			JSON.stringify({ ...aLock, acquiredAt: new Date(Date.now() - 10 * 60 * 1000).toISOString() })
		);
		await fireEvent.click(qb.getByRole('button', { name: 'Save changes' }));
		await waitFor(() => expect(qb.getByText(/its lock is stale/)).toBeInTheDocument());
		const takeOver = qb.getByRole('button', { name: 'Take over lock' });
		await fireEvent.click(takeOver);

		// The takeover is real: B owns the lock and can dispatch its write.
		const bLock = JSON.parse(shared.getItem(LOCK_KEY) ?? 'null') as { tabId?: string };
		expect(bLock.tabId).not.toBe(aLock.tabId);
		await fireEvent.click(qb.getByRole('button', { name: 'Save changes' }));
		await waitFor(() => expect(qb.getByText(/UNKNOWN:/)).toBeInTheDocument());
		expect(patchCount).toBe(2);
	});

	it.each([
		['unparseable JSON', '{not-json'],
		['wrong shape', JSON.stringify({ requestId: 'req-x', payload: { priority: 1 } })]
	])('malformed pending journal (%s) fails closed: persistent error, Save disabled, nothing discarded', async (_name, raw) => {
		const shared = new MemoryStorage();
		shared.setItem(PENDING_KEY, raw);
		vi.stubGlobal(
			'fetch',
			vi.fn(async () => jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"'))
		);

		const comp = await renderEditor({ storage: shared });

		await waitFor(() => expect(comp.getByText(/Recovery data unreadable/)).toBeInTheDocument());
		expect((comp.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(
			true
		);
		// Fail closed: the corrupt bytes are still there, nothing was silently
		// discarded, and no UNKNOWN hold was invented from corrupt evidence.
		expect(shared.getItem(PENDING_KEY)).toBe(raw);
		expect(comp.queryByText(/UNKNOWN:/)).not.toBeInTheDocument();
	});

	it('malformed lock record fails closed with a persistent error state', async () => {
		const shared = new MemoryStorage();
		shared.setItem(LOCK_KEY, '{oops');
		vi.stubGlobal(
			'fetch',
			vi.fn(async () => jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"'))
		);

		const comp = await renderEditor({ storage: shared });

		await waitFor(() => expect(comp.getByText(/Recovery data unreadable/)).toBeInTheDocument());
		expect((comp.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(
			true
		);
		expect(shared.getItem(LOCK_KEY)).toBe('{oops');
	});

	it('"Mark reconciled" stays disabled until a fresh GET completes after the conflict', async () => {
		const ifMatchHeaders: Array<string | null> = [];
		let getCount = 0;
		vi.stubGlobal('fetch', async (url: unknown, init?: RequestInit) => {
			const method = init?.method ?? 'GET';
			const headers = (init?.headers as Record<string, string>) ?? {};
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
			if (getCount === 2) {
				return jsonResponse(200, { priority: 11, max_runners: 5, min_idle_runners: 1 }, '"99:9"');
			}
			return jsonResponse(200, { priority: 12, max_runners: 5, min_idle_runners: 1 }, '"99:10"');
		});

		const comp = await renderEditor();

		await fireEvent.input(priorityInput(), { target: { value: '77' } });
		await fireEvent.click(comp.getByRole('button', { name: 'Save changes' }));
		await waitFor(() => expect(comp.getByText(/Conflict \(412\)/)).toBeInTheDocument());

		// Immediately after the conflict there has been no fresh GET: the
		// deliberate click is refused and Save stays latched shut.
		const mark = comp.getByRole('button', { name: 'Mark reconciled' }) as HTMLButtonElement;
		expect(mark.disabled).toBe(true);
		await fireEvent.click(mark);
		expect((comp.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(
			true
		);

		// A fresh GET after the conflict arms reconcile; the displayed values
		// are that read's.
		await fireEvent.click(comp.getByRole('button', { name: 'Re-read settings' }));
		await waitFor(() => expect(comp.getByText('"99:9"')).toBeInTheDocument());
		expect((comp.getByRole('button', { name: 'Mark reconciled' }) as HTMLButtonElement).disabled).toBe(
			false
		);

		// Settings change again: the next GET re-arms reconcile against the
		// newest values, so the operator always asserts a fresh read.
		await fireEvent.click(comp.getByRole('button', { name: 'Re-read settings' }));
		await waitFor(() => expect(comp.getByText('"99:10"')).toBeInTheDocument());
		expect((comp.getByRole('button', { name: 'Mark reconciled' }) as HTMLButtonElement).disabled).toBe(
			false
		);

		// Reconcile remains one deliberate click; afterwards Save is
		// preconditioned on the newest ETag.
		await fireEvent.click(comp.getByRole('button', { name: 'Mark reconciled' }));
		expect((comp.getByRole('button', { name: 'Save changes' }) as HTMLButtonElement).disabled).toBe(
			false
		);
		await fireEvent.click(comp.getByRole('button', { name: 'Save changes' }));
		await waitFor(() => expect(comp.getByText('Settings applied. Draft cleared.')).toBeInTheDocument());
		expect(ifMatchHeaders).toEqual(['"42:7"', '"99:10"']);
	});
});
