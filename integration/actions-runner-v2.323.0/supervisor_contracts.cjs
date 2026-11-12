'use strict';
// Executes exact official RunnerService.js with ONLY fake process/child/timer
// boundaries. NO child_process require, real spawn/kill, service or network calls.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const vm = require('node:vm');
const sourceRoot = process.argv[2];
assert.ok(sourceRoot && path.isAbsolute(sourceRoot), 'explicit pinned source required');
const filename = path.join(sourceRoot, 'src/Misc/layoutbin/RunnerService.js');
const source = fs.readFileSync(filename, 'utf8');
assert.equal(crypto.createHash('sha256').update(source).digest('hex'),
  '843c5d27f4e92ce2b11d3ae0ba974f200e705eb762475c06fe0d732cb585331d');
function fixture(env = {}) {
  const children = [], timers = [], handlers = new Map();
  const processStub = { platform: 'linux', argv: ['node', filename], env: { ...env },
    stdout: { write() {} }, on(name, callback) { handlers.set(name, callback); },
    exit(code) { throw new Error(`unexpected fixture process.exit(${code})`); } };
  const spawn = (...args) => {
    const callbacks = new Map(), signals = [];
    const child = { pid: 100 + children.length, args, signals,
      stdout: { on() {} }, stderr: { on() {} },
      on(name, callback) { callbacks.set(name, callback); },
      kill(signal) { signals.push(signal); }, close(code) { callbacks.get('close')(code); } };
    children.push(child); return child;
  };
  const context = vm.createContext({ __dirname: path.dirname(filename), process: processStub,
    console: { log() {}, error() {} },
    require(name) { if (name === 'child_process') return { spawn }; if (name === 'path') return path; throw new Error('unexpected require'); },
    setTimeout(callback, delay) { const timer = { callback, delay, unrefCalled: false }; timers.push(timer); return { unref() { timer.unrefCalled = true; } }; } },
    { codeGeneration: { strings: false, wasm: false } });
  vm.runInContext(source, context, { filename, timeout: 1000 });
  assert.equal(children.length, 1);
  return { children, timers, handlers };
}
const passed = [];
function test(name, run) { run(); passed.push(name); }
for (const code of [0, 1, 5]) {
  test(`terminal-${code}-does-not-schedule-internal-restart`, () => {
    const f = fixture(); f.children[0].close(code);
    assert.equal(f.children.length, 1); assert.equal(f.timers.length, 0);
  });
}
for (const code of [2, 3, 4]) {
  test(`exit-${code}-restarts-even-with-unknown-failure-limit-one`, () => {
    const f = fixture({ GITHUB_ACTIONS_SERVICE_EXIT_AFTER_N_FAILURES: '1' });
    f.children[0].close(code);
    assert.equal(f.timers.length, 1); assert.equal(f.timers[0].delay, 5000);
    f.timers[0].callback(); assert.equal(f.children.length, 2);
    assert.equal(f.children[1].args[1].join(' '), 'run --startuptype service');
  });
}
test('unknown-exit-default-restarts-but-limit-one-stops', () => {
  const retry = fixture(); retry.children[0].close(99);
  assert.equal(retry.timers.length, 1); retry.timers[0].callback(); assert.equal(retry.children.length, 2);
  const stop = fixture({ GITHUB_ACTIONS_SERVICE_EXIT_AFTER_N_FAILURES: '1' }); stop.children[0].close(99);
  assert.equal(stop.timers.length, 0); assert.equal(stop.children.length, 1);
});
test('service-signal-sends-interrupt-then-thirty-second-kill', () => {
  for (const signal of ['SIGINT', 'SIGTERM']) {
    const f = fixture(); f.handlers.get(signal)();
    assert.deepEqual(f.children[0].signals, ['SIGINT']);
    assert.equal(f.timers.length, 1); assert.equal(f.timers[0].delay, 30000);
    assert.equal(f.timers[0].unrefCalled, true);
    f.timers[0].callback(); assert.deepEqual(f.children[0].signals, ['SIGINT', 'SIGKILL']);
    f.children[0].close(2); assert.equal(f.timers.length, 1); // no restart while stopping
  }
});
assert.equal(passed.length, 8);
process.stdout.write(JSON.stringify({ source_commit: 'd8b82f44a9ec0258904d5869ae683c31d5aac299',
  source_sha256: '843c5d27f4e92ce2b11d3ae0ba974f200e705eb762475c06fe0d732cb585331d',
  node: process.version, passed, real_children_spawned: 0, real_signals_sent: 0,
  installed_runner_qualified: false, restart_fence_qualified: false, retirement_authority: false }, null, 2) + '\n');
