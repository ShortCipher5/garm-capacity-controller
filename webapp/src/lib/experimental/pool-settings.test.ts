import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import {
	getPoolSettings,
	patchPoolSettings,
	PoolSettingsError,
	poolSettingsPath,
	type PoolSettingsPatch,
	type PoolSettingsRef
} from './pool-settings.js';
import { garmApi } from '$lib/api/client.js';

const ref: PoolSettingsRef = { entityType: 'repository', entityID: 'repo-1', poolID: 'pool-9' };
const settingsURL = '/api/v1/offline/entities/repository/repo-1/pools/pool-9/settings';

function jsonResponse(status: number, body: unknown, etag?: string): Response {
	const headers: Record<string, string> = { 'Content-Type': 'application/json' };
	if (etag) headers['ETag'] = etag;
	return new Response(JSON.stringify(body), { status, headers });
}

describe('pool-settings client', () => {
	beforeEach(() => {
		garmApi.setToken('tok-123');
	});

	afterEach(() => {
		vi.unstubAllGlobals();
		garmApi.setToken('');
	});

	it('builds the provisional settings path per entity kind', () => {
		expect(poolSettingsPath(ref)).toBe(settingsURL);
		expect(poolSettingsPath({ entityType: 'organization', entityID: 'o1', poolID: 'p1' })).toBe(
			'/api/v1/offline/entities/organization/o1/pools/p1/settings'
		);
		expect(poolSettingsPath({ entityType: 'enterprise', entityID: 'e1', poolID: 'p1' })).toBe(
			'/api/v1/offline/entities/enterprise/e1/pools/p1/settings'
		);
	});

	it('GET keeps the exact ETag and carries the bearer token', async () => {
		const fetchMock = vi.fn().mockResolvedValue(
			jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"')
		);
		vi.stubGlobal('fetch', fetchMock);

		const result = await getPoolSettings(ref);

		expect(result.settings).toEqual({ priority: 10, max_runners: 5, min_idle_runners: 1 });
		expect(result.etag).toBe('"42:7"');

		const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
		expect(url).toBe(settingsURL);
		expect((init.headers as Record<string, string>)['Authorization']).toBe('Bearer tok-123');
	});

	it('PATCH sends If-Match verbatim with a strict body', async () => {
		const fetchMock = vi.fn().mockResolvedValue(
			jsonResponse(200, { priority: 20, max_runners: 8, min_idle_runners: 2 }, '"43:1"')
		);
		vi.stubGlobal('fetch', fetchMock);

		const result = await patchPoolSettings(ref, '"42:7"', { priority: 20, max_runners: 8 });

		expect(result.etag).toBe('"43:1"');
		const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
		expect(init.method).toBe('PATCH');
		expect((init.headers as Record<string, string>)['If-Match']).toBe('"42:7"');
		expect(JSON.parse(init.body as string)).toEqual({ priority: 20, max_runners: 8 });
	});

	it('412 surfaces the fresh ETag, leaves the caller draft untouched and keeps the token', async () => {
		const seenAuth: Array<string | null> = [];
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(
				jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 1 }, '"42:7"')
			)
			.mockResolvedValueOnce(jsonResponse(412, { detail: 'stale' }, '"99:1"'))
			.mockResolvedValueOnce(
				jsonResponse(200, { priority: 21, max_runners: 5, min_idle_runners: 1 }, '"99:2"')
			);
		vi.stubGlobal('fetch', async (url: unknown, init?: RequestInit) => {
			seenAuth.push(((init?.headers as Record<string, string>) ?? {})['Authorization'] ?? null);
			return fetchMock(url, init);
		});

		const getResult = await getPoolSettings(ref);
		const draft = { priority: 21 };
		const draftBefore = { ...draft };

		let conflict: PoolSettingsError | undefined;
		try {
			await patchPoolSettings(ref, getResult.etag!, { ...draft });
		} catch (err) {
			conflict = err as PoolSettingsError;
		}
		expect(conflict).toBeInstanceOf(PoolSettingsError);
		expect(conflict!.kind).toBe('conflict');
		expect(conflict!.etag).toBe('"99:1"');
		// the operator's draft object is untouched by the conflict
		expect(draft).toEqual(draftBefore);

		// operator reviews, saves again against the fresh ETag
		const retried = await patchPoolSettings(ref, conflict!.etag!, { ...draft });
		expect(retried.etag).toBe('"99:2"');
		const lastInit = fetchMock.mock.calls[2][1] as RequestInit;
		expect((lastInit.headers as Record<string, string>)['If-Match']).toBe('"99:1"');

		// the auth token was preserved on every request, including after 412
		expect(seenAuth).toEqual(['Bearer tok-123', 'Bearer tok-123', 'Bearer tok-123']);
	});

	it('holds UNKNOWN with no retry when the response is lost', async () => {
		const fetchMock = vi.fn().mockRejectedValue(new TypeError('network down'));
		vi.stubGlobal('fetch', fetchMock);

		let err: PoolSettingsError | undefined;
		try {
			await patchPoolSettings(ref, '"42:7"', { priority: 3 });
		} catch (e) {
			err = e as PoolSettingsError;
		}
		expect(err).toBeInstanceOf(PoolSettingsError);
		expect(err!.kind).toBe('unknown');
		expect(err!.message).toContain('lost or ambiguous');
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	it('treats 503 after dispatch as UNKNOWN and does not retry', async () => {
		const fetchMock = vi.fn().mockResolvedValue(jsonResponse(503, {}));
		vi.stubGlobal('fetch', fetchMock);

		let err: PoolSettingsError | undefined;
		try {
			await patchPoolSettings(ref, '"42:7"', { priority: 3 });
		} catch (e) {
			err = e as PoolSettingsError;
		}
		expect(err!.kind).toBe('unknown');
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	it('classifies 400/403/422/428 as definitive rejections', async () => {
		for (const status of [400, 403, 422, 428]) {
			const fetchMock = vi.fn().mockResolvedValue(jsonResponse(status, { detail: 'no' }));
			vi.stubGlobal('fetch', fetchMock);

			let err: PoolSettingsError | undefined;
			try {
				await patchPoolSettings(ref, '"42:7"', { priority: 3 });
			} catch (e) {
				err = e as PoolSettingsError;
			}
			expect(err!.kind).toBe('rejected');
			expect(err!.status).toBe(status);
			expect(fetchMock).toHaveBeenCalledTimes(1);
			vi.unstubAllGlobals();
		}
	});

	it('never dispatches without a strong ETag', async () => {
		const fetchMock = vi.fn();
		vi.stubGlobal('fetch', fetchMock);

		await expect(patchPoolSettings(ref, '', { priority: 3 })).rejects.toBeInstanceOf(PoolSettingsError);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it('rejects empty or non-strict patches before dispatch', async () => {
		const fetchMock = vi.fn();
		vi.stubGlobal('fetch', fetchMock);

		await expect(patchPoolSettings(ref, '"42:7"', {})).rejects.toBeInstanceOf(PoolSettingsError);
		await expect(
			patchPoolSettings(ref, '"42:7"', { priority: 1, flavor: 'big' } as never)
		).rejects.toBeInstanceOf(PoolSettingsError);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it('PATCH answered from a stored request receipt reports replayed with the resulting ETag', async () => {
		const fetchMock = vi.fn().mockResolvedValue(
			jsonResponse(200, { outcome: 'applied_changed', resulting_etag: '"43:2"' }, '"43:2"')
		);
		vi.stubGlobal('fetch', fetchMock);

		const result = await patchPoolSettings(ref, '"42:7"', { priority: 20 });

		expect(result.replayed).toBe(true);
		expect(result.settings).toBeNull();
		expect(result.etag).toBe('"43:2"');
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	it('PATCH answered from a stored noop receipt replays without a settings tuple', async () => {
		const fetchMock = vi.fn().mockResolvedValue(
			jsonResponse(200, { outcome: 'applied_noop', resulting_etag: '"43:3"' }, '"43:3"')
		);
		vi.stubGlobal('fetch', fetchMock);

		const result = await patchPoolSettings(ref, '"43:3"', { priority: 20 });

		expect(result.replayed).toBe(true);
		expect(result.settings).toBeNull();
		expect(result.etag).toBe('"43:3"');
	});

	it('409 request-id reuse is a definitive rejection, never UNKNOWN', async () => {
		const fetchMock = vi.fn().mockResolvedValue(
			jsonResponse(409, { error: 'Idempotency-Key was already used with a different payload or pool' })
		);
		vi.stubGlobal('fetch', fetchMock);

		let err: PoolSettingsError | undefined;
		try {
			await patchPoolSettings(ref, '"42:7"', { priority: 3 });
		} catch (e) {
			err = e as PoolSettingsError;
		}
		expect(err).toBeInstanceOf(PoolSettingsError);
		expect(err!.kind).toBe('rejected');
		expect(err!.status).toBe(409);
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	it('a 200 that is neither settings nor a replay outcome stays UNKNOWN', async () => {
		const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { hello: 'world' }, '"43:4"'));
		vi.stubGlobal('fetch', fetchMock);

		let err: PoolSettingsError | undefined;
		try {
			await patchPoolSettings(ref, '"42:7"', { priority: 3 });
		} catch (e) {
			err = e as PoolSettingsError;
		}
		expect(err).toBeInstanceOf(PoolSettingsError);
		expect(err!.kind).toBe('unknown');
		expect(err!.status).toBe(200);
	});

	it('rejects out-of-bounds patch values before dispatch', async () => {
		const cases: Array<PoolSettingsPatch> = [
			{ priority: 1_000_001 },
			{ priority: -1 },
			{ max_runners: 0 },
			{ max_runners: 1_000_001 },
			{ min_idle_runners: 1_000_001 }
		];
		for (const patch of cases) {
			const fetchMock = vi.fn();
			vi.stubGlobal('fetch', fetchMock);

			let err: PoolSettingsError | undefined;
			try {
				await patchPoolSettings(ref, '"42:7"', patch);
			} catch (e) {
				err = e as PoolSettingsError;
			}
			expect(err).toBeInstanceOf(PoolSettingsError);
			expect(err!.kind).toBe('rejected');
			expect(fetchMock).not.toHaveBeenCalled();
			vi.unstubAllGlobals();
		}
	});

	it('rejects min_idle_runners above max_runners in the same patch before dispatch', async () => {
		const fetchMock = vi.fn();
		vi.stubGlobal('fetch', fetchMock);

		let err: PoolSettingsError | undefined;
		try {
			await patchPoolSettings(ref, '"42:7"', { max_runners: 2, min_idle_runners: 3 });
		} catch (e) {
			err = e as PoolSettingsError;
		}
		expect(err).toBeInstanceOf(PoolSettingsError);
		expect(err!.kind).toBe('rejected');
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it('accepts patch values on the contract bounds and min_idle equal to max_runners', async () => {
		const fetchMock = vi.fn().mockImplementation(() =>
			Promise.resolve(jsonResponse(200, { priority: 1_000_000, max_runners: 1, min_idle_runners: 0 }, '"43:6"'))
		);
		vi.stubGlobal('fetch', fetchMock);

		const onBounds = await patchPoolSettings(ref, '"42:7"', { priority: 1_000_000 });
		expect(onBounds.etag).toBe('"43:6"');

		const equalBounds = await patchPoolSettings(ref, '"43:6"', { max_runners: 1_000_000, min_idle_runners: 1_000_000 });
		expect(equalBounds.etag).toBe('"43:6"');
		expect(fetchMock).toHaveBeenCalledTimes(2);
	});

	it('holds UNKNOWN when an applied-200 document has min_idle_runners above max_runners', async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValue(jsonResponse(200, { priority: 10, max_runners: 5, min_idle_runners: 6 }, '"43:7"'));
		vi.stubGlobal('fetch', fetchMock);

		let err: PoolSettingsError | undefined;
		try {
			await patchPoolSettings(ref, '"42:7"', { priority: 3 });
		} catch (e) {
			err = e as PoolSettingsError;
		}
		expect(err).toBeInstanceOf(PoolSettingsError);
		expect(err!.kind).toBe('unknown');
		expect(err!.status).toBe(200);
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	it('holds UNKNOWN when an applied-200 document carries an out-of-range value', async () => {
		const documents = [
			{ priority: 1_000_001, max_runners: 5, min_idle_runners: 1 },
			{ priority: 10, max_runners: 0, min_idle_runners: 1 },
			{ priority: 10, max_runners: 5, min_idle_runners: 1_000_001 }
		];
		for (const document of documents) {
			const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, document, '"43:8"'));
			vi.stubGlobal('fetch', fetchMock);

			let err: PoolSettingsError | undefined;
			try {
				await patchPoolSettings(ref, '"42:7"', { priority: 3 });
			} catch (e) {
				err = e as PoolSettingsError;
			}
			expect(err).toBeInstanceOf(PoolSettingsError);
			expect(err!.kind).toBe('unknown');
			expect(err!.status).toBe(200);
			vi.unstubAllGlobals();
		}
	});

	it('GET with a corrupt settings document throws the UNKNOWN error', async () => {
		const fetchMock = vi.fn().mockResolvedValue(
			jsonResponse(200, { priority: -1, max_runners: 5, min_idle_runners: 1 }, '"42:7"')
		);
		vi.stubGlobal('fetch', fetchMock);

		let err: PoolSettingsError | undefined;
		try {
			await getPoolSettings(ref);
		} catch (e) {
			err = e as PoolSettingsError;
		}
		expect(err).toBeInstanceOf(PoolSettingsError);
		expect(err!.kind).toBe('unknown');
		expect(err!.status).toBe(200);
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	it('treats a 200 carrying both a replay outcome and settings fields as UNKNOWN', async () => {
		const fetchMock = vi.fn().mockResolvedValue(
			jsonResponse(200, { outcome: 'applied_changed', resulting_etag: '"43:9"', priority: 20 }, '"43:9"')
		);
		vi.stubGlobal('fetch', fetchMock);

		let err: PoolSettingsError | undefined;
		try {
			await patchPoolSettings(ref, '"42:7"', { priority: 3 });
		} catch (e) {
			err = e as PoolSettingsError;
		}
		expect(err).toBeInstanceOf(PoolSettingsError);
		expect(err!.kind).toBe('unknown');
		expect(err!.status).toBe(200);
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	describe('strict validation of successful (2xx) responses', () => {
		it('a 200 without an ETag header stays UNKNOWN', async () => {
			const fetchMock = vi.fn().mockResolvedValue(
				jsonResponse(200, { priority: 3, max_runners: 5, min_idle_runners: 1 })
			);
			vi.stubGlobal('fetch', fetchMock);

			let err: PoolSettingsError | undefined;
			try {
				await patchPoolSettings(ref, '"42:7"', { priority: 3 });
			} catch (e) {
				err = e as PoolSettingsError;
			}
			expect(err).toBeInstanceOf(PoolSettingsError);
			expect(err!.kind).toBe('unknown');
			expect(err!.status).toBe(200);
			expect(err!.etag).toBeNull();
			expect(err!.message).toContain('no strong ETag');
			expect(fetchMock).toHaveBeenCalledTimes(1);
		});

		it('a 200 with a malformed ETag stays UNKNOWN', async () => {
			const malformed = ['W/"42:7"', '42:7', '"abc:1"', '"0:0"', '"42"', '"42:-1"', '"42:7:1"', '"42: 7"'];
			for (const etag of malformed) {
				const fetchMock = vi.fn().mockResolvedValue(
					jsonResponse(200, { priority: 3, max_runners: 5, min_idle_runners: 1 }, etag)
				);
				vi.stubGlobal('fetch', fetchMock);

				let err: PoolSettingsError | undefined;
				try {
					await patchPoolSettings(ref, '"42:7"', { priority: 3 });
				} catch (e) {
					err = e as PoolSettingsError;
				}
				expect(err).toBeInstanceOf(PoolSettingsError);
				expect(err!.kind).toBe('unknown');
				expect(err!.status).toBe(200);
				expect(err!.message).toContain('malformed ETag');
				expect(fetchMock).toHaveBeenCalledTimes(1);
				vi.unstubAllGlobals();
			}
		});

		it('a GET 200 without an ETag stays UNKNOWN', async () => {
			const fetchMock = vi.fn().mockResolvedValue(
				jsonResponse(200, { priority: 3, max_runners: 5, min_idle_runners: 1 })
			);
			vi.stubGlobal('fetch', fetchMock);

			let err: PoolSettingsError | undefined;
			try {
				await getPoolSettings(ref);
			} catch (e) {
				err = e as PoolSettingsError;
			}
			expect(err).toBeInstanceOf(PoolSettingsError);
			expect(err!.kind).toBe('unknown');
			expect(err!.status).toBe(200);
			expect(fetchMock).toHaveBeenCalledTimes(1);
		});

		it('an out-of-range number in a 200 settings document stays UNKNOWN', async () => {
			const fetchMock = vi.fn().mockResolvedValue(
				jsonResponse(200, { priority: 3, max_runners: 5, min_idle_runners: 1_000_001 }, '"43:1"')
			);
			vi.stubGlobal('fetch', fetchMock);

			let err: PoolSettingsError | undefined;
			try {
				await patchPoolSettings(ref, '"42:7"', { priority: 3 });
			} catch (e) {
				err = e as PoolSettingsError;
			}
			expect(err).toBeInstanceOf(PoolSettingsError);
			expect(err!.kind).toBe('unknown');
			expect(err!.status).toBe(200);
			expect(fetchMock).toHaveBeenCalledTimes(1);
		});

		it('min_idle_runners above max_runners in a 200 settings document stays UNKNOWN', async () => {
			const fetchMock = vi.fn().mockResolvedValue(
				jsonResponse(200, { priority: 3, max_runners: 2, min_idle_runners: 3 }, '"43:1"')
			);
			vi.stubGlobal('fetch', fetchMock);

			let err: PoolSettingsError | undefined;
			try {
				await patchPoolSettings(ref, '"42:7"', { priority: 3 });
			} catch (e) {
				err = e as PoolSettingsError;
			}
			expect(err).toBeInstanceOf(PoolSettingsError);
			expect(err!.kind).toBe('unknown');
			expect(err!.status).toBe(200);
			expect(fetchMock).toHaveBeenCalledTimes(1);
		});

		it('an unknown field in a 200 settings document stays UNKNOWN', async () => {
			const fetchMock = vi.fn().mockResolvedValue(
				jsonResponse(
					200,
					{ priority: 3, max_runners: 5, min_idle_runners: 1, image: 'ubuntu-latest' },
					'"43:1"'
				)
			);
			vi.stubGlobal('fetch', fetchMock);

			let err: PoolSettingsError | undefined;
			try {
				await patchPoolSettings(ref, '"42:7"', { priority: 3 });
			} catch (e) {
				err = e as PoolSettingsError;
			}
			expect(err).toBeInstanceOf(PoolSettingsError);
			expect(err!.kind).toBe('unknown');
			expect(err!.status).toBe(200);
			expect(fetchMock).toHaveBeenCalledTimes(1);
		});

		it('an unknown field in a replay envelope stays UNKNOWN', async () => {
			const fetchMock = vi.fn().mockResolvedValue(
				jsonResponse(
					200,
					{ outcome: 'applied_changed', resulting_etag: '"43:2"', request_id: 'req-9' },
					'"43:2"'
				)
			);
			vi.stubGlobal('fetch', fetchMock);

			let err: PoolSettingsError | undefined;
			try {
				await patchPoolSettings(ref, '"42:7"', { priority: 3 }, 'req-1');
			} catch (e) {
				err = e as PoolSettingsError;
			}
			expect(err).toBeInstanceOf(PoolSettingsError);
			expect(err!.kind).toBe('unknown');
			expect(err!.status).toBe(200);
			expect(fetchMock).toHaveBeenCalledTimes(1);
		});

		it('a replay envelope whose resulting_etag mismatches the response ETag stays UNKNOWN', async () => {
			const fetchMock = vi.fn().mockResolvedValue(
				jsonResponse(200, { outcome: 'applied_changed', resulting_etag: '"44:0"' }, '"43:2"')
			);
			vi.stubGlobal('fetch', fetchMock);

			let err: PoolSettingsError | undefined;
			try {
				await patchPoolSettings(ref, '"42:7"', { priority: 3 }, 'req-1');
			} catch (e) {
				err = e as PoolSettingsError;
			}
			expect(err).toBeInstanceOf(PoolSettingsError);
			expect(err!.kind).toBe('unknown');
			expect(err!.status).toBe(200);
			expect(err!.message).toContain('resulting_etag');
			expect(fetchMock).toHaveBeenCalledTimes(1);
		});

		it('failed validation leaves the caller pending record and stored revision untouched', async () => {
			// The body alone looks like a clean apply; only the missing ETag
			// header gives it away as malformed success evidence.
			const fetchMock = vi.fn().mockResolvedValue(
				jsonResponse(200, { priority: 3, max_runners: 5, min_idle_runners: 1 })
			);
			vi.stubGlobal('fetch', fetchMock);

			// Caller-side state a confirmed result would mutate: the stored
			// revision precondition and the durable pending record journaled
			// before dispatch (mirrors the editor's dispatchPatch routing).
			let storedEtag: string | null = '"42:7"';
			const pending = {
				requestId: 'req-pending-1',
				payload: { priority: 3 } as PoolSettingsPatch,
				draft: { priority: 3, max_runners: 5, min_idle_runners: 1 }
			};
			let pendingCleared = false;
			let adoptedEtag: string | null = null;

			let err: PoolSettingsError | undefined;
			try {
				const res = await patchPoolSettings(ref, storedEtag, pending.payload, pending.requestId);
				// The confirmed-result path: only reachable when the response
				// passes strict validation.
				pendingCleared = true;
				adoptedEtag = res.etag;
				storedEtag = res.etag;
			} catch (e) {
				err = e as PoolSettingsError;
				// The UNKNOWN routing keeps both the journal and the stored
				// revision, exactly like the editor's catch block.
			}

			expect(err).toBeInstanceOf(PoolSettingsError);
			expect(err!.kind).toBe('unknown');
			expect(err!.status).toBe(200);
			expect(pendingCleared).toBe(false);
			expect(adoptedEtag).toBeNull();
			expect(storedEtag).toBe('"42:7"');
			expect(pending).toEqual({
				requestId: 'req-pending-1',
				payload: { priority: 3 },
				draft: { priority: 3, max_runners: 5, min_idle_runners: 1 }
			});
			expect(fetchMock).toHaveBeenCalledTimes(1);
		});
	});
});
