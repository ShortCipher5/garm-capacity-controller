import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import { createMockPool } from '../../test/factories.js';

// This file never statically imports the components under test: the feature
// flag is evaluated when flags.ts is first loaded, so each test dynamically
// imports after (optionally) stubbing the env, in a fresh module registry.
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

vi.mock('$lib/components/JsonEditor.svelte', () => import('$lib/test/EmptyComponent.svelte'));

function jsonResponse(status: number, body: unknown, etag?: string): Response {
	const headers: Record<string, string> = { 'Content-Type': 'application/json' };
	if (etag) headers['ETag'] = etag;
	return new Response(JSON.stringify(body), { status, headers });
}

describe('UpdatePoolModal experiment gate', () => {
	afterEach(() => {
		vi.unstubAllEnvs();
		vi.unstubAllGlobals();
	});

	const pool = createMockPool({ id: 'pool-9', repo_id: 'repo-1' });

	it('keeps the stock modal untouched when the experiment is disabled', async () => {
		const fetchMock = vi.fn();
		vi.stubGlobal('fetch', fetchMock);

		const { default: UpdatePoolModal } = await import('./UpdatePoolModal.svelte');
		render(UpdatePoolModal, { props: { pool } });

		// stock form is present
		expect(screen.getByText('Update Pool pool-9')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Update Pool' })).toBeInTheDocument();
		// no experimental editor surfaced
		expect(screen.queryByText(/Pool Settings/)).not.toBeInTheDocument();
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it('replaces the modal content with the experimental editor only when enabled', async () => {
		vi.stubEnv('VITE_GARM_EXPERIMENTAL_POOL_SETTINGS', 'true');
		const fetchMock = vi.fn().mockResolvedValue(
			jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"')
		);
		vi.stubGlobal('fetch', fetchMock);

		const { default: UpdatePoolModal } = await import('./UpdatePoolModal.svelte');
		render(UpdatePoolModal, { props: { pool } });

		await waitFor(() =>
			expect(screen.getByText('Update Pool Settings pool-9 (experimental)')).toBeInTheDocument()
		);
		expect(screen.queryByText('Update Pool pool-9')).not.toBeInTheDocument();
	});
});
