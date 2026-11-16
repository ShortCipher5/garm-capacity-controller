// Opt-in, build-time feature flags for experimental (provisional) interfaces.
//
// Everything in this module defaults to OFF. Ordinary builds keep the stock
// UI behavior; nothing here is enabled implicitly. Follow the existing Vite
// convention (same as VITE_GARM_API_URL): set the variable at build time to
// opt in.

const env = import.meta.env as unknown as Record<string, string | undefined>;

// Dynamic check, read at call time (useful for tests and late configuration).
export function isPoolSettingsExperimentEnabled(): boolean {
	return env.VITE_GARM_EXPERIMENTAL_POOL_SETTINGS === 'true' || env.VITE_GARM_EXPERIMENTAL_POOL_SETTINGS === '1';
}

// Enables the conditional pool settings editor in UpdatePoolModal. The editor
// speaks the provisional interface:
//
//   GET/PATCH /api/v1/offline/entities/{organization|repository|enterprise}/
//             {entityID}/pools/{poolID}/settings
//
// PATCH carries a strong quoted ETag in If-Match and the three coupled fields
// (priority, max_runners, min_idle_runners) are edited with conflict-safe
// semantics instead of the stock unconditional pool update.
export const experimentalPoolSettingsEnabled: boolean =
	env.VITE_GARM_EXPERIMENTAL_POOL_SETTINGS === 'true' ||
	env.VITE_GARM_EXPERIMENTAL_POOL_SETTINGS === '1';
