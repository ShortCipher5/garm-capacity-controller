import { describe, it, expect, vi, afterEach } from 'vitest';

describe('experimental feature flags', () => {
	afterEach(() => {
		vi.unstubAllEnvs();
		vi.resetModules();
	});

	it('defaults to OFF when the variable is unset', async () => {
		const flags = await import('./flags.js');
		expect(flags.experimentalPoolSettingsEnabled).toBe(false);
	});

	it('opts in via VITE_GARM_EXPERIMENTAL_POOL_SETTINGS', async () => {
		vi.stubEnv('VITE_GARM_EXPERIMENTAL_POOL_SETTINGS', '1');
		vi.resetModules();
		const flags = await import('./flags.js');
		expect(flags.experimentalPoolSettingsEnabled).toBe(true);
	});

	it('does not opt in for arbitrary values', async () => {
		vi.stubEnv('VITE_GARM_EXPERIMENTAL_POOL_SETTINGS', 'yes');
		vi.resetModules();
		const flags = await import('./flags.js');
		expect(flags.experimentalPoolSettingsEnabled).toBe(false);
	});
});
