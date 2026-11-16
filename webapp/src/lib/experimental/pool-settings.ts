// Experimental client for the provisional pool settings interface.
//
//   GET   /api/v1/offline/entities/{organization|repository|enterprise}/{entityID}/pools/{poolID}/settings
//   PATCH same path
//
// PATCH sends a strict JSON body over exactly {priority, max_runners,
// min_idle_runners} (at least one field) and carries the strong quoted ETag
// from GET in an If-Match header.
//
// Outcome semantics (never an auto-retry, never an unconditional write):
//   - 200:             applied; the response carries the new ETag.
//   - 412:             PoolSettingsError kind 'conflict'. A 412 proves our
//                      write had no effect; the fresh ETag from the response
//                      is on the error so a draft can be rebased after a
//                      manual re-read.
//   - 400/403/422/428: PoolSettingsError kind 'rejected' (definitive,
//                      non-ambiguous).
//   - anything else, or a lost/ambiguous response after dispatch:
//                      PoolSettingsError kind 'unknown'. The caller must hold,
//                      surface, and let the operator re-read; the request is
//                      never retried here.
//
// This module only ever issues the single request it was asked to issue.

import { garmApi } from '$lib/api/client.js';

export type PoolSettingsEntityType = 'repository' | 'organization' | 'enterprise';

export interface PoolSettingsRef {
	entityType: PoolSettingsEntityType;
	entityID: string;
	poolID: string;
}

export interface PoolSettings {
	priority: number;
	max_runners: number;
	min_idle_runners: number;
}

export type PoolSettingsPatch = Partial<PoolSettings>;

export type PoolSettingsErrorKind = 'conflict' | 'rejected' | 'unknown';

export class PoolSettingsError extends Error {
	constructor(
		readonly kind: PoolSettingsErrorKind,
		readonly status: number,
		readonly etag: string | null,
		message: string
	) {
		super(message);
		this.name = 'PoolSettingsError';
	}
}

export interface PoolSettingsResult {
	settings: PoolSettings;
	etag: string | null;
}

const SETTINGS_FIELDS: Array<keyof PoolSettings> = ['priority', 'max_runners', 'min_idle_runners'];

export function poolSettingsPath(ref: PoolSettingsRef): string {
	if (!ref.entityID) {
		throw new Error('entity ID must not be empty');
	}
	if (!ref.poolID) {
		throw new Error('pool ID must not be empty');
	}
	return `/api/v1/offline/entities/${ref.entityType}/${encodeURIComponent(ref.entityID)}/pools/${encodeURIComponent(ref.poolID)}/settings`;
}

function messageFromBody(data: unknown, fallback: string): string {
	if (data && typeof data === 'object') {
		const detail = (data as { detail?: string; error?: string }).detail;
		const err = (data as { error?: string }).error;
		if (detail && err) return `${err}. ${detail}`;
		if (detail) return detail;
		if (err) return err;
	}
	return fallback;
}

function asSettings(data: unknown): PoolSettings | null {
	if (
		data &&
		typeof data === 'object' &&
		typeof (data as PoolSettings).priority === 'number' &&
		typeof (data as PoolSettings).max_runners === 'number' &&
		typeof (data as PoolSettings).min_idle_runners === 'number'
	) {
		return data as PoolSettings;
	}
	return null;
}

function validatePatch(patch: PoolSettingsPatch): void {
	if (!patch || typeof patch !== 'object' || Object.keys(patch).length === 0) {
		throw new PoolSettingsError('rejected', 0, null, 'patch must set at least one of priority, max_runners, min_idle_runners');
	}
	for (const key of Object.keys(patch)) {
		if (!SETTINGS_FIELDS.includes(key as keyof PoolSettings)) {
			throw new PoolSettingsError(
				'rejected',
				0,
				null,
				`patch contains unsupported field ${JSON.stringify(key)} (allowed: priority, max_runners, min_idle_runners)`
			);
		}
		const value = (patch as Record<string, unknown>)[key];
		if (typeof value !== 'number' || !Number.isInteger(value) || value < 0) {
			throw new PoolSettingsError('rejected', 0, null, `patch field ${JSON.stringify(key)} must be a non-negative integer`);
		}
	}
}

async function requestPoolSettings(
	method: 'GET' | 'PATCH',
	ref: PoolSettingsRef,
	opts: { ifMatch?: string; patch?: PoolSettingsPatch } = {}
): Promise<PoolSettingsResult> {
	const headers: Record<string, string> = { Accept: 'application/json' };
	const token = garmApi.getToken();
	if (token) {
		headers['Authorization'] = `Bearer ${token}`;
	}
	let body: string | undefined;
	if (opts.patch !== undefined) {
		if (!opts.ifMatch) {
			throw new PoolSettingsError('rejected', 0, null, 'refusing to patch without a strong ETag: unconditional writes are never sent');
		}
		validatePatch(opts.patch);
		headers['Content-Type'] = 'application/json';
		// Strong quoted ETag, exactly as obtained from GET.
		headers['If-Match'] = opts.ifMatch;
		body = JSON.stringify(opts.patch);
	}

	let resp: Response;
	try {
		resp = await fetch(poolSettingsPath(ref), { method, headers, body });
	} catch (err) {
		// The request may have been dispatched; the response was lost. Hold as
		// UNKNOWN. Never retry from here.
		throw new PoolSettingsError(
			'unknown',
			0,
			null,
			`request dispatched but the response was lost or ambiguous: ${err instanceof Error ? err.message : String(err)}`
		);
	}

	let etag: string | null = null;
	try {
		etag = resp.headers.get('ETag');
	} catch {
		etag = null;
	}

	let data: unknown = null;
	try {
		const text = await resp.text();
		data = text ? JSON.parse(text) : null;
	} catch {
		data = null;
	}

	switch (resp.status) {
		case 200: {
			const settings = asSettings(data);
			if (!settings) {
				throw new PoolSettingsError(
					'unknown',
					resp.status,
					etag,
					'response was 200 but the body is not a readable settings document; outcome of the write is unknown'
				);
			}
			return { settings, etag };
		}
		case 412:
			// 412 proves non-effect. Surface the fresh ETag so a preserved
			// draft can be rebased after a manual re-read.
			throw new PoolSettingsError(
				'conflict',
				resp.status,
				etag,
				messageFromBody(data, 'settings changed since this ETag was issued; your changes were NOT applied')
			);
		case 400:
		case 403:
		case 422:
		case 428:
			throw new PoolSettingsError(
				'rejected',
				resp.status,
				etag,
				messageFromBody(data, `request rejected with status ${resp.status}`)
			);
		default:
			throw new PoolSettingsError(
				'unknown',
				resp.status,
				etag,
				messageFromBody(data, `unexpected status ${resp.status} after dispatch; outcome of the write is unknown`)
			);
	}
}

export function getPoolSettings(ref: PoolSettingsRef): Promise<PoolSettingsResult> {
	return requestPoolSettings('GET', ref);
}

export function patchPoolSettings(
	ref: PoolSettingsRef,
	ifMatch: string,
	patch: PoolSettingsPatch
): Promise<PoolSettingsResult> {
	return requestPoolSettings('PATCH', ref, { ifMatch, patch });
}
