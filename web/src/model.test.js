import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { percent, validateOverview } from './model.js';
const fixture = JSON.parse(readFileSync(new URL('./fixture.json', import.meta.url)));
test('capacity percentages handle zero and overcommit without NaN', () => {
  assert.equal(percent(0, 0), 0);
  assert.equal(percent(1, 0), 100);
  assert.equal(percent(100, 10), 100);
  assert.equal(percent(3, 4), 75);
  assert.equal(percent(-1, 4), null);
  assert.equal(percent(1, NaN), null);
});
test('fixture remains explicitly non-enforcing', () => {
  assert.equal(validateOverview(fixture).enforcement, false);
  assert.throws(() => validateOverview({ ...fixture, mode: 'production' }));
  assert.throws(() => validateOverview({ ...fixture, enforcement: true }));
  assert.throws(() => validateOverview({ ...fixture, hosts: null }));
});
test('reject malformed capacity instead of showing optimistic availability', () => {
  const broken = structuredClone(fixture);
  broken.hosts[0].budget.cpu_millis = -1;
  assert.throws(() => validateOverview(broken));
});
test('global and host fixture accounting are consistent', () => {
  for (const key of ['cpu_millis', 'ram_mib', 'runners']) {
    assert.equal(fixture.hosts.reduce((sum, host) => sum + host.allocated[key], 0), fixture.global.allocated[key]);
  }
  assert.equal(fixture.pools.reduce((sum, pool) => sum + pool.allocated, 0), fixture.global.allocated.runners);
});
test('Go API and browser preview share an identical fixture', () => {
  const apiFixture = JSON.parse(readFileSync(new URL('../../internal/demo/fixture.json', import.meta.url)));
  assert.deepEqual(fixture, apiFixture);
});

test('reject malformed policy and events before rendering', () => {
  const broken = structuredClone(fixture);
  broken.pools[0].priority = 'high';
  assert.throws(() => validateOverview(broken));
  const badEvent = structuredClone(fixture);
  badEvent.events[0].reason = {};
  assert.throws(() => validateOverview(badEvent));
});
