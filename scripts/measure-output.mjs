#!/usr/bin/env node
// Offline CLI replay: no cloud credentials, submissions, or paid upstream calls.
// Usage: node scripts/measure-output.mjs BEFORE AFTER OUTPUT_DIR DOCTOR_JSON RESULT_JSON...
import { createServer } from 'node:http';
import { spawn } from 'node:child_process';
import { mkdtemp, mkdir, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, basename, resolve } from 'node:path';
import assert from 'node:assert/strict';

const [before, after, outDir, doctorFile, ...files] = process.argv.slice(2);
if (!files.length || !doctorFile) throw new Error('Expected BEFORE AFTER OUTPUT_DIR DOCTOR_JSON RESULT_JSON...');
const dir = await mkdtemp(join(tmpdir(), 'soseek-replay-'));
await mkdir(outDir, { recursive: true, mode: 0o700 });
const tokenFile = join(dir, 'token');
await writeFile(tokenFile, 'offline-replay-token', { mode: 0o600 });
const doctor = JSON.parse(await readFile(doctorFile, 'utf8'));
const capabilities = doctor.capabilities;
assert(capabilities, 'Baseline doctor must include capabilities');
const results = new Map();
for (const file of files) {
  const parsed = JSON.parse(await readFile(file, 'utf8'));
  const result = parsed.result ?? parsed;
  assert(result.run_id && result.status && result.operation, 'Expected stored result');
  results.set(result.run_id, { label: basename(file, '.json').replace(/-saved$/, ''), result });
}
let requests = 0;
const server = createServer((req, res) => {
  requests++;
  res.setHeader('Content-Type', 'application/json');
  if (req.method !== 'GET') { res.writeHead(405); res.end('{}'); return; }
  if (req.url === '/v1/capabilities') { res.end(JSON.stringify(capabilities)); return; }
  const match = /^\/v1\/runs\/([^/]+)(\/result)?$/.exec(req.url);
  const saved = match && results.get(match[1]);
  if (!saved) { res.writeHead(404); res.end('{}'); return; }
  const result = saved.result;
  res.end(JSON.stringify(match[2] ? result : { run_id: result.run_id, status: result.status, result }));
});
await new Promise(r => server.listen(0, '127.0.0.1', r));
const env = { ...process.env, SOSEEK_URL: `http://127.0.0.1:${server.address().port}`,
  SOSEEK_TOKEN_FILE: tokenFile, SOSEEK_CONNECTION: 'direct',
  XDG_CONFIG_HOME: join(dir, 'config'), XDG_STATE_HOME: join(dir, 'state') };
async function run(binary, args, label) {
  const data = await new Promise((resolveResult, reject) => {
    const child = spawn(resolve(binary), args, { env, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '';
    child.stdout.on('data', b => stdout += b);
    child.stderr.on('data', b => stderr += b);
    child.on('error', reject);
    child.on('close', code => resolveResult({ code, stdout, stderr }));
  });
  assert.equal(data.code, 0, `${label}: ${data.stderr}`);
  const parsed = JSON.parse(data.stdout);
  await writeFile(join(outDir, `${label}.json`), data.stdout, { mode: 0o600 });
  return { bytes: Buffer.byteLength(data.stdout), stderr_bytes: Buffer.byteLength(data.stderr), parsed };
}
try {
  const metrics = [];
  for (const [label, args] of [['doctor', ['doctor', '--json']], ...[...results].map(([id, x]) => [x.label, ['runs', 'get', id, '--json']])]) {
    const old = await run(before, args, `${label}-before`);
    const compact = await run(after, args, `${label}-after`);
    if (label !== 'doctor') {
      const full = await run(after, [...args, '--full'], `${label}-full`);
      // Complete stored result fields remain identical, apart from old CLI view metadata.
      const raw = results.get(args[2]).result;
      for (const key of ['results', 'answer', 'attempts', 'usage', 'citations', 'queries', 'retrieval_events', 'raw', 'routing', 'warnings', 'gaps']) {
        if (raw[key] !== undefined) assert.deepEqual(full.parsed[key], raw[key], `${label}: full lost ${key}`);
      }
      assert.deepEqual(compact.parsed.usage, full.parsed.usage, `${label}: compact lost accounting`);
      assert.equal(compact.parsed.status, full.parsed.status);
      assert.equal(compact.parsed.results.length, full.parsed.results.length);
      assert.deepEqual(compact.parsed.results.map(s => [s.source_id, s.url]), full.parsed.results.map(s => [s.source_id, s.url]));
      if (raw.answer) assert.equal(compact.parsed.answer, raw.answer, `${label}: answer clipped`);
    }
    metrics.push({ label, before_bytes: old.bytes, after_bytes: compact.bytes,
      reduction_percent: Math.round((1 - compact.bytes / old.bytes) * 10000) / 100,
      before_stderr_bytes: old.stderr_bytes, after_stderr_bytes: compact.stderr_bytes });
  }
  await writeFile(join(outDir, 'metrics.json'), JSON.stringify({ mode: 'offline saved-result replay', requests, metrics }, null, 2) + '\n', { mode: 0o600 });
  console.log(JSON.stringify(metrics, null, 2));
} finally {
  await new Promise(r => server.close(r));
  await rm(dir, { recursive: true, force: true });
}
