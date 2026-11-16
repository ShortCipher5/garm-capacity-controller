import { describe, it, expect, vi, afterEach } from 'vitest';
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

describe('ExperimentalPoolSettingsEditor', () => {
	afterEach(() => {
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

	it('412 preserves the draft, records the fresh ETag and keeps the token', async () => {
		const seenAuth: Array<string | null> = [];
		const ifMatchHeaders: Array<string | null> = [];
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(
				jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"')
			)
			.mockResolvedValueOnce(jsonResponse(412, { detail: 'stale' }, '"99:1"'))
			.mockResolvedValueOnce(
				jsonResponse(200, { priority: 77, max_runners: 5, min_idle_runners: 1 }, '"99:2"')
			);
		vi.stubGlobal('fetch', async (url: unknown, init?: RequestInit) => {
			seenAuth.push(((init?.headers as Record<string, string>) ?? {})['Authorization'] ?? null);
			if (init?.method === 'PATCH') {
				ifMatchHeaders.push((init.headers as Record<string, string>)['If-Match'] ?? null);
			}
			return fetchMock(url, init);
		});

		await renderEditor();

		await fireEvent.input(priorityInput(), { target: { value: '77' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(screen.getByText(/Conflict \(412\)/)).toBeInTheDocument());
		// draft preserved in the inputs
		expect(priorityInput().value).toBe('77');
		// fresh ETag recorded
		expect(screen.getByText(/Fresh ETag: "99:1"/)).toBeInTheDocument();

		// operator saves again: fresh ETag goes out, auth token intact
		await fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
		await waitFor(() => expect(screen.getByText('Settings applied. Draft cleared.')).toBeInTheDocument());

		expect(ifMatchHeaders).toEqual(['"42:7"', '"99:1"']);
		expect(seenAuth).toEqual(['Bearer tok-123', 'Bearer tok-123', 'Bearer tok-123']);
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
		await fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(screen.getByText('Settings applied. Draft cleared.')).toBeInTheDocument());
		// inputs now show the server-confirmed values; no unsent draft remains
		expect(priorityInput().value).toBe('30');
		expect((screen.getByLabelText('Max Runners') as HTMLInputElement).value).toBe('6');
		expect(screen.getByText('"43:1"')).toBeInTheDocument();
	});

	it('UNKNOWN hold: no retry, manual re-read never auto-submits', async () => {
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
			);
		vi.stubGlobal('fetch', async (url: unknown, init?: RequestInit) => {
			methods.push(init?.method ?? 'GET');
			return fetchMock(url, init);
		});

		await renderEditor();
		await fireEvent.input(priorityInput(), { target: { value: '44' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(screen.getByText(/UNKNOWN:/)).toBeInTheDocument());
		// draft held exactly as the operator left it
		expect(priorityInput().value).toBe('44');
		// exactly one PATCH was dispatched; nothing auto-retried
		expect(methods.filter((m) => m === 'PATCH')).toHaveLength(1);

		// manual re-read reconciles, preserves the draft, never auto-submits
		await fireEvent.click(screen.getByRole('button', { name: 'Re-read settings' }));
		await waitFor(() => expect(screen.getByText('"50:1"')).toBeInTheDocument());
		expect(priorityInput().value).toBe('44');
		expect(methods.filter((m) => m === 'PATCH')).toHaveLength(1);
		expect(screen.queryByText(/UNKNOWN:/)).not.toBeInTheDocument();
	});
});

