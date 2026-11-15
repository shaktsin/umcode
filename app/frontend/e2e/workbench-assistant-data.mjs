// Real app + engine + local deterministic provider; never uses a paid provider or user data.
// Covers assistant data work: approved imports show up in the Data tab, and a data
// script approval shows the code, offers no "always allow", and honors denial.
// Set UMCODE_E2E_SANDBOX=1 on a machine with Seatbelt/bubblewrap to also run an approved script.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { createServer as httpServer } from 'node:http';
import { createServer as netServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'vite';

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright-core');
assert.ok(process.env.UMCODE_TEST_ENGINE, 'Set UMCODE_TEST_ENGINE to a built engine');
const withSandbox = process.env.UMCODE_E2E_SANDBOX === '1';
const artifacts = process.env.UI_ARTIFACT_DIR || join(tmpdir(), 'umcode-workbench-assistant-data');
await mkdir(artifacts, { recursive: true });
const fixture = await mkdtemp(join(tmpdir(), 'umc-assistant-data-')), home = join(fixture, 'home'), root = join(fixture, 'project');
await Promise.all([home, root].map(path => mkdir(path)));
await writeFile(join(root, 'reviews.csv'), 'text,label\ngreat,pos\nawful,neg\n');
const requests = [], errors = [];
const content = value => typeof value === 'string' ? value : (value ?? []).map(part => part.text ?? '').join('\n');
const scriptArgs = { title: 'Balance reviews', inputDatasetIds: [], outputName: 'balanced', script: "rows = ['text,label', 'great,pos', 'awful,neg']\nopen('out/balanced.csv', 'w').write('\\n'.join(rows) + '\\n')\nprint('kept 1 per class')", expectOutput: 'dataset' };
const provider = httpServer(async (req, res) => {
  if (req.method === 'GET') { res.setHeader('content-type', 'application/json'); res.end(JSON.stringify({ data: [{ id: 'local-qwen' }] })); return; }
  let raw = ''; for await (const chunk of req) raw += chunk;
  const body = JSON.parse(raw); requests.push(body);
  const messages = body.messages ?? [], system = messages.filter(m => m.role === 'system').map(m => content(m.content)).join('\n');
  const user = content(messages.findLast(m => m.role === 'user')?.content), last = messages.at(-1);
  let response = 'Okay.', tool;
  if (system.startsWith('Privately classify')) {
    const input = JSON.parse(user);
    response = JSON.stringify({ taskId: input.taskId, expectedRevision: input.expectedRevision, originItemId: input.originItemId, relation: 'continue', expectations: [] });
  } else if (last?.role === 'tool') response = 'Done with that step.';
  else if (user.includes('Please import reviews.csv')) tool = { name: 'workbench__import_data', arguments: JSON.stringify({ sources: [{ type: 'local', path: 'reviews.csv' }] }) };
  else if (user.includes('Please run the balancing script')) tool = { name: 'workbench__run_data_script', arguments: JSON.stringify(scriptArgs) };
  res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' });
  const event = value => res.write('data: ' + JSON.stringify(value) + '\n\n');
  if (tool) event({ choices: [{ index: 0, delta: { tool_calls: [{ index: 0, id: 'call_' + requests.length, type: 'function', function: tool }] }, finish_reason: 'tool_calls' }] });
  else event({ choices: [{ index: 0, delta: { content: response }, finish_reason: 'stop' }] });
  event({ choices: [], usage: { prompt_tokens: 400, completion_tokens: 50 } }); res.end('data: [DONE]\n\n');
});
provider.listen(0, '127.0.0.1'); await once(provider, 'listening');
const portListener = netServer(); portListener.listen(0, '127.0.0.1'); await once(portListener, 'listening');
const port = portListener.address().port; await new Promise(resolve => portListener.close(resolve));
process.env.UMCODE_HOME = home; process.env.UMCODE_ENGINE_WS_PORT = String(port);
const engine = spawn(process.env.UMCODE_TEST_ENGINE, ['engine'], { env: { ...process.env, UMCODE_NO_SERVICE: '1' }, stdio: ['ignore', 'pipe', 'pipe'] });
let engineLog = ''; engine.stdout.on('data', b => engineLog += b); engine.stderr.on('data', b => engineLog += b);
let ws, server, browser;
async function eventually(fn) { const deadline = Date.now() + 25000; for (;;) { try { return await fn(); } catch (err) { if (Date.now() > deadline || engine.exitCode !== null) throw err; await new Promise(resolve => setTimeout(resolve, 100)); } } }
try {
  const token = await eventually(async () => (await readFile(join(home, 'run/token'), 'utf8')).trim());
  ws = await eventually(async () => { const socket = new WebSocket(`ws://127.0.0.1:${port}/ws?token=${encodeURIComponent(token)}`); await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject; }); return socket; });
  let id = 0; const pending = new Map();
  ws.onmessage = event => { const value = JSON.parse(event.data), p = pending.get(value.id); if (!p) return; clearTimeout(p.timer); pending.delete(value.id); if (value.error) p.reject(new Error(value.error.message)); else p.resolve(value.result); };
  const rpc = (method, params = {}) => new Promise((resolve, reject) => { const next = ++id, timer = setTimeout(() => { pending.delete(next); reject(new Error('RPC timeout ' + method)); }, 15000); pending.set(next, { resolve, reject, timer }); ws.send(JSON.stringify({ jsonrpc: '2.0', id: next, method, params })); });
  await rpc('initialize', { clientName: 'workbench-assistant-data-test', clientVersion: '0.4.3', protocolVersion: '1.0.0', admin: true });
  const project = await rpc('project/create', { root, name: 'Data lab' });
  await rpc('credential/add', { provider: 'openai_compatible', label: 'Fixture provider', secret: 'fixture-only', baseUrl: `http://127.0.0.1:${provider.address().port}/v1` });
  await rpc('routing/set', { models: [{ id: 'helper', name: 'Fixture assistant', provider: 'openai_compatible', model: 'local-qwen', enabled: true }], pools: [{ id: 'helper-pool', name: 'Fixture assistant', strategy: 'priority', models: ['helper'], enabled: true }], defaultPool: 'helper-pool' });
  const route = await rpc('model/route', { projectId: project.id }); assert.ok(route.chosen, JSON.stringify(route));
  server = await createServer({ root: fileURLToPath(new URL('..', import.meta.url)), server: { host: '127.0.0.1', port: 0, strictPort: false } }); await server.listen();
  const { starterGraph } = await server.ssrLoadModule('/src/lib/workbench/graph.ts');
  const model = await rpc('workbench/model/create', { projectId: project.id, name: 'Review classifier', task: 'classification' });
  await rpc('workbench/graph/save', { projectId: project.id, modelId: model.id, graphJson: JSON.stringify(starterGraph('mlp')) });
  const datasetCount = async () => (await rpc('workbench/data/get', { projectId: project.id, modelId: model.id })).datasets.length;
  browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } }); page.setDefaultTimeout(15000); page.setDefaultNavigationTimeout(60000);
  page.on('pageerror', e => errors.push(e.message)); page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
  await page.addInitScript(({ projectId, modelId }) => { localStorage.setItem('umcode.view', 'workbench'); localStorage.setItem('umcode.lastProject', projectId); localStorage.setItem('umcode.workbench.model.' + projectId, modelId); }, { projectId: project.id, modelId: model.id });
  await page.goto(server.resolvedUrls.local[0]);
  const workbench = page.getByRole('main', { name: 'ML Workbench' }), canvas = workbench.getByRole('region', { name: 'Model layer canvas' }); await canvas.waitFor();
  await canvas.locator('.svelte-flow__node').filter({ has: page.locator('[data-layer-kind="linear"]') }).first().click();
  await workbench.getByRole('button', { name: 'Ask about this layer', exact: true }).click();
  const chat = page.locator('[data-workbench-chat]'); await chat.waitFor();
  const message = chat.getByRole('textbox', { name: 'Message', exact: true }); await message.waitFor();
  async function ask(text) { await chat.getByRole('button', { name: 'Send', exact: true }).waitFor(); await message.fill(text); await message.press('Enter'); }

  // 1. Import: approval card names the source; approving imports it and opens the Data tab on it.
  await ask('Please import reviews.csv');
  const dock = page.getByRole('region', { name: 'Approval needed' });
  await dock.getByText('Import 1 dataset source(s): reviews.csv', { exact: false }).waitFor();
  assert.equal(await datasetCount(), 0, 'Nothing is imported before approval');
  await page.screenshot({ path: join(artifacts, '01-import-approval.png'), animations: 'disabled' });
  await dock.getByRole('button', { name: 'Allow once', exact: true }).click();
  await eventually(async () => assert.equal(await datasetCount(), 1));
  await workbench.getByRole('heading', { name: 'reviews.csv' }).waitFor();
  await page.screenshot({ path: join(artifacts, '02-imported-in-data-tab.png'), animations: 'disabled' });

  // 2. Script: the approval shows the code, offers no "always allow", and denial creates nothing.
  await ask('Please run the balancing script');
  const code = dock.getByLabel('Script the assistant wants to run'); await code.waitFor();
  assert.ok((await code.textContent()).includes("open('out/balanced.csv'"), 'Approval shows the real script text as code');
  assert.equal(await page.getByRole('button', { name: /Always allow/ }).count(), 0, 'Scripts cannot be remembered');
  assert.equal(await dock.getByRole('button', { name: 'Allow once', exact: true }).count(), 1, 'One clear approve button');
  await page.screenshot({ path: join(artifacts, '03-script-approval.png'), animations: 'disabled' });
  if (!withSandbox) {
    await dock.getByRole('button', { name: 'Deny', exact: true }).click();
    await chat.getByText('Done with that step.', { exact: false }).last().waitFor();
    assert.equal(await datasetCount(), 1, 'A denied script creates no dataset');
  } else {
    await dock.getByRole('button', { name: 'Allow once', exact: true }).click();
    await eventually(async () => assert.equal(await datasetCount(), 2));
    await workbench.getByRole('heading', { name: 'balanced' }).waitFor();
    await workbench.getByText('Balance reviews', { exact: false }).waitFor();
    await workbench.getByText('View script', { exact: true }).click();
    await page.screenshot({ path: join(artifacts, '04-assistant-dataset-provenance.png'), animations: 'disabled' });
  }
  assert.deepEqual(errors, []);
  await writeFile(join(artifacts, 'report.json'), JSON.stringify({ realEngine: true, paidProvider: false, sandboxRun: withSandbox, workflows: ['import approval + Data tab', 'script approval shows code', 'no always-allow for scripts', withSandbox ? 'approved script + provenance' : 'denied script creates nothing'] }, null, 2));
  console.log('Workbench assistant data smoke passed; artifacts: ' + artifacts);
} catch (err) {
  await browser?.contexts()[0]?.pages()[0]?.screenshot({ path: join(artifacts, 'failure.png') }).catch(() => {});
  console.error('Browser errors:', errors); throw err;
} finally {
  ws?.close(); await browser?.close(); await server?.close(); provider.close();
  if (engine.exitCode === null) { engine.kill('SIGTERM'); await once(engine, 'exit'); }
  await writeFile(join(artifacts, 'engine.log'), engineLog); await rm(fixture, { recursive: true, force: true });
  await writeFile(join(artifacts, 'provider-requests.json'), JSON.stringify(requests, null, 2));
}
