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
//   - 200:             applied, but only ever treated as confirmed after
//                      strict validation: a strong, well-formed "epoch:seq"
//                      ETag header plus a body matching exactly one of the
//                      two documented shapes (the settings tuple or the
//                      replay outcome envelope). A malformed success is
//                      UNKNOWN, never confirmed. When confirmed, the
//                      response carries the new ETag and the settings
//                      tuple. If the same request identity and payload are
//                      presented again the server answers from its durable
//                      request receipt instead: the result has settings
//                      null, the resulting ETag, and replayed=true.
//   - 412:             PoolSettingsError kind 'conflict'. A 412 proves our
//                      write had no effect; the fresh ETag from the response
//                      is on the error so a draft can be rebased after a
//                      manual re-read.
//   - 409:             PoolSettingsError kind 'rejected'. The request
//                      identity was already used with different content;
//                      nothing was written. Definitive, never UNKNOWN.
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

// PoolSettingsPatchResult is the PATCH outcome. When the server answers from
// a durable request receipt instead of re-applying (same request identity and
// payload presented again), settings is null and etag is the resulting ETag
// recorded by the original request; the caller re-reads deliberately if it
// needs the document.
export interface PoolSettingsPatchResult {
	settings: PoolSettings | null;
	etag: string | null;
	replayed: boolean;
}

const SETTINGS_FIELDS: Array<keyof PoolSettings> = ['priority', 'max_runners', 'min_idle_runners'];

// Bounds mirror the provisional server contract: priority 0..1000000,
// max_runners 1..1000000, min_idle_runners 0..1000000.
const SETTINGS_BOUNDS: Record<keyof PoolSettings, { min: number; max: number }> = {
	priority: { min: 0, max: 1_000_000 },
	max_runners: { min: 1, max: 1_000_000 },
	min_idle_runners: { min: 0, max: 1_000_000 }
};

function hasOwnKey(data: object, key: string): boolean {
	return Object.prototype.hasOwnProperty.call(data, key);
}

// isStrongETag reports whether value is a strong, quoted "<epoch>:<seq>"
// revision tag exactly as the experimental API mints it (the server formats
// it from two unsigned decimal integers). Both components must parse as safe
// integers, the epoch must be positive and the sequence zero or positive;
// anything else — missing, weak, unquoted, non-numeric, epoch 0 — can never
// confirm an outcome, because the caller stores this value as its next
// precondition.
function isStrongETag(value: unknown): value is string {
	if (typeof value !== 'string') {
		return false;
	}
	const match = /^"(\d+):(\d+)"$/.exec(value);
	if (!match) {
		return false;
	}
	const epoch = Number(match[1]);
	const seq = Number(match[2]);
	return Number.isSafeInteger(epoch) && epoch > 0 && Number.isSafeInteger(seq) && seq >= 0;
}

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

// asSettings reads a settings tuple for GET and applied-200 responses. It
// fails closed: the server marshals exactly the three settings fields, so a
// document carrying an unknown field is malformed evidence; a value outside
// the bounds above, a non-integer, a negative, or min_idle_runners above
// max_runners means the server document is corrupt evidence and reads as
// null (UNKNOWN on PATCH, the existing 'unknown' throw from getPoolSettings
// on GET). A document that also carries a replay outcome marker is
// ambiguous, not a readable settings document.
function asSettings(data: unknown): PoolSettings | null {
	if (!data || typeof data !== 'object') {
		return null;
	}
	if (hasOwnKey(data, 'outcome')) {
		return null;
	}
	const doc = data as Record<string, unknown>;
	for (const key of Object.keys(doc)) {
		if (!SETTINGS_FIELDS.includes(key as keyof PoolSettings)) {
			return null;
		}
	}
	const priority = doc.priority;
	const maxRunners = doc.max_runners;
	const minIdleRunners = doc.min_idle_runners;
	if (
		typeof priority !== 'number' ||
		!Number.isInteger(priority) ||
		priority < SETTINGS_BOUNDS.priority.min ||
		priority > SETTINGS_BOUNDS.priority.max ||
		typeof maxRunners !== 'number' ||
		!Number.isInteger(maxRunners) ||
		maxRunners < SETTINGS_BOUNDS.max_runners.min ||
		maxRunners > SETTINGS_BOUNDS.max_runners.max ||
		typeof minIdleRunners !== 'number' ||
		!Number.isInteger(minIdleRunners) ||
		minIdleRunners < SETTINGS_BOUNDS.min_idle_runners.min ||
		minIdleRunners > SETTINGS_BOUNDS.min_idle_runners.max ||
		minIdleRunners > maxRunners
	) {
		return null;
	}
	return { priority, max_runners: maxRunners, min_idle_runners: minIdleRunners };
}

// asReplay recognises the wire document a PATCH answered from a stored
// request receipt carries on HTTP 200 instead of the settings tuple: the
// server does not store the original settings body. Only applied outcomes
// arrive with status 200 (rejected replays re-use their original status),
// and the envelope marshals exactly {outcome, resulting_etag}: an unknown
// outcome, an envelope that also carries settings fields or any other extra
// key, or a resulting_etag that is not a strong revision tag is an ambiguous
// or malformed document, not an applied outcome.
function asReplay(data: unknown): { resultingETag: string } | null {
	if (!data || typeof data !== 'object') {
		return null;
	}
	const doc = data as Record<string, unknown>;
	for (const key of Object.keys(doc)) {
		if (key !== 'outcome' && key !== 'resulting_etag') {
			return null;
		}
	}
	if (doc.outcome !== 'applied_changed' && doc.outcome !== 'applied_noop') {
		return null;
	}
	if (typeof doc.resulting_etag !== 'string' || !isStrongETag(doc.resulting_etag)) {
		return null;
	}
	return { resultingETag: doc.resulting_etag };
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
		const bounds = SETTINGS_BOUNDS[key as keyof PoolSettings];
		if (typeof value !== 'number' || !Number.isInteger(value) || value < bounds.min || value > bounds.max) {
			throw new PoolSettingsError(
				'rejected',
				0,
				null,
				`patch field ${JSON.stringify(key)} must be an integer between ${bounds.min} and ${bounds.max}`
			);
		}
	}
	// Same-patch runner bounds must satisfy the server contract's invariant
	// before dispatch; min must never exceed max.
	const { max_runners: maxRunners, min_idle_runners: minIdleRunners } = patch;
	if (maxRunners !== undefined && minIdleRunners !== undefined && minIdleRunners > maxRunners) {
		throw new PoolSettingsError('rejected', 0, null, 'patch field "min_idle_runners" must not exceed "max_runners"');
	}
}

async function requestPoolSettings(
	method: 'GET' | 'PATCH',
	ref: PoolSettingsRef,
	opts: { ifMatch?: string; patch?: PoolSettingsPatch; requestId?: string } = {}
): Promise<PoolSettingsPatchResult> {
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
		if (opts.requestId) {
			// Persistent request identity: lets the server answer a
			// re-presented request from its durable receipt instead of
			// re-applying it.
			headers['Idempotency-Key'] = opts.requestId;
		}
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
			// A 2xx confirms nothing without a strong, well-formed revision
			// tag: the caller stores this value as its next precondition and
			// clears its pending record, so a missing or malformed ETag must
			// read as UNKNOWN, never as a confirmed apply.
			if (!isStrongETag(etag)) {
				throw new PoolSettingsError(
					'unknown',
					resp.status,
					etag,
					etag === null
						? 'response was 200 but carried no strong ETag; outcome of the write is unknown'
						: 'response was 200 but carried a malformed ETag; outcome of the write is unknown'
				);
			}
			const settings = asSettings(data);
			if (settings) {
				return { settings, etag, replayed: false };
			}
			// A PATCH answered from a stored request receipt returns an
			// outcome envelope instead of the settings tuple. That outcome
			// is durable and known; it must not surface as UNKNOWN.
			const replay = asReplay(data);
			if (replay) {
				// The server answers an applied replay with the ETag header
				// and the envelope resulting_etag set to the same recorded
				// tag; a mismatch is a malformed success, not a confirmed
				// replay.
				if (replay.resultingETag !== etag) {
					throw new PoolSettingsError(
						'unknown',
						resp.status,
						etag,
						'replay envelope resulting_etag does not match the response ETag; outcome of the write is unknown'
					);
				}
				return { settings: null, etag: replay.resultingETag, replayed: true };
			}
			throw new PoolSettingsError(
				'unknown',
				resp.status,
				etag,
				'response was 200 but the body is neither a readable settings document nor a readable replay outcome; outcome of the write is unknown'
			);
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
		case 409:
			// Request identity reused with different content: definitive
			// refusal, nothing was written. Never an UNKNOWN hold.
			throw new PoolSettingsError(
				'rejected',
				resp.status,
				etag,
				messageFromBody(data, 'request identity was already used with different content; this request was refused and nothing was written')
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

export async function getPoolSettings(ref: PoolSettingsRef): Promise<PoolSettingsResult> {
	const res = await requestPoolSettings('GET', ref);
	if (!res.settings) {
		throw new PoolSettingsError('unknown', 200, res.etag, 'GET returned no readable settings document');
	}
	return { settings: res.settings, etag: res.etag };
}

export function patchPoolSettings(
	ref: PoolSettingsRef,
	ifMatch: string,
	patch: PoolSettingsPatch,
	requestId?: string
): Promise<PoolSettingsPatchResult> {
	return requestPoolSettings('PATCH', ref, { ifMatch, patch, requestId });
}
