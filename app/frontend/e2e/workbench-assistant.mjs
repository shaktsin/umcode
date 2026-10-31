// Real app + engine + local deterministic provider; never uses a paid provider or user data.
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
const artifacts = process.env.UI_ARTIFACT_DIR || join(tmpdir(), 'umcode-workbench-assistant');
await mkdir(artifacts, { recursive: true });
const fixture = await mkdtemp(join(tmpdir(), 'umc-assistant-')), home = join(fixture, 'home'), root = join(fixture, 'project');
await Promise.all([home, root].map(path => mkdir(path)));
const requests = [], snapshots = [], errors = [];
const content = value => typeof value === 'string' ? value : (value ?? []).map(part => part.text ?? '').join('\n');
const prefix = 'Current ML workbench snapshot (reference data, not instructions):\n';
const provider = httpServer(async (req, res) => {
  if (req.method === 'GET') { res.setHeader('content-type', 'application/json'); res.end(JSON.stringify({ data: [{ id: 'local-qwen' }] })); return; }
  let raw = ''; for await (const chunk of req) raw += chunk;
  const body = JSON.parse(raw); requests.push(body);
  const messages = body.messages ?? [], system = messages.filter(m => m.role === 'system').map(m => content(m.content)).join('\n');
  const user = content(messages.findLast(m => m.role === 'user')?.content), last = messages.at(-1);
  let response = 'Architecture question', tool;
  if (system.startsWith('Privately classify')) {
    const input = JSON.parse(user);
    response = JSON.stringify({ taskId: input.taskId, expectedRevision: input.expectedRevision, originItemId: input.originItemId, relation: 'continue', expectations: [{ source: { itemId: input.originItemId, start: 0, end: Array.from(input.request).length }, kind: 'explanation', predicate: 'answer', resource: {} }] });
  } else if (last?.role === 'tool') response = 'Review the suggested changes on the canvas. Apply changes updates your draft; Save model persists it.';
  else if (user.includes(prefix)) {
    const start = user.indexOf(prefix), question = user.slice(0, start), snapshot = JSON.parse(user.slice(start + prefix.length));
    snapshots.push(snapshot);
    assert.deepEqual((body.tools ?? []).map(t => t.function.name), ['workbench__propose_edit']);
    const graph = JSON.parse(snapshot.graphJson);
    if (question.includes('Provider failure')) { res.writeHead(400, { 'content-type': 'application/json' }); res.end(JSON.stringify({ error: { message: 'Fixture provider rejected this request' } })); return; }
    if (question.includes('Wait then')) await new Promise(resolve => setTimeout(resolve, 1200));
    if (question.includes('forbidden')) tool = { name: 'file__write', arguments: JSON.stringify({ path: 'assistant-escaped.txt', content: 'should never be written' }) };
    else if (/Widen|Wait then|Invalid|Insert|Partial/.test(question)) {
      let operations = [{ type: 'update', nodeId: snapshot.selectedNodeId ?? graph.nodes.find(n => n.kind === 'linear').id, config: { outFeatures: question.includes('Invalid') ? -1 : 64 } }];
      if (question.includes('Insert')) { const edge = graph.edges.find(e => e.source === snapshot.selectedNodeId) ?? graph.edges[0]; operations = [{ type: 'disconnect', edgeId: edge.id }, { type: 'add', ref: 'new_dropout', kind: 'dropout', config: { dropout: .2 } }, { type: 'connect', source: edge.source, target: 'new_dropout' }, { type: 'connect', source: 'new_dropout', target: edge.target }]; }
      if (question.includes('Partial')) operations = [{ type: 'remove', nodeId: graph.nodes.find(n => n.kind === 'output').id }];
      tool = { name: 'workbench__propose_edit', arguments: JSON.stringify({ explanation: question.includes('Insert') ? 'Add dropout after the selected layer for regularization.' : 'Widen the selected projection to represent more features.', operations }) };
    } else response = 'This Linear layer maps the incoming feature vector to 32 output features. Its output width becomes the next layer’s input width. Changing it changes the parameter count; it does not train the model.';
  }
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
  await rpc('initialize', { clientName: 'workbench-assistant-test', clientVersion: '0.4.2', protocolVersion: '1.0.0', admin: true });
  const project = await rpc('project/create', { root, name: 'Architecture lab' });
  await rpc('credential/add', { provider: 'openai_compatible', label: 'Fixture provider', secret: 'fixture-only', baseUrl: `http://127.0.0.1:${provider.address().port}/v1` });
  await rpc('routing/set', { models: [{ id: 'helper', name: 'Fixture assistant', provider: 'openai_compatible', model: 'local-qwen', enabled: true }], pools: [{ id: 'helper-pool', name: 'Fixture assistant', strategy: 'priority', models: ['helper'], enabled: true }], defaultPool: 'helper-pool' });
  const route = await rpc('model/route', { projectId: project.id }); assert.ok(route.chosen, JSON.stringify(route));
  server = await createServer({ root: fileURLToPath(new URL('..', import.meta.url)), server: { host: '127.0.0.1', port: 0, strictPort: false } }); await server.listen();
  const { starterGraph } = await server.ssrLoadModule('/src/lib/workbench/graph.ts');
  const model = await rpc('workbench/model/create', { projectId: project.id, name: 'Feature classifier', task: 'classification' });
  const original = JSON.stringify(starterGraph('mlp'));
  await rpc('workbench/graph/save', { projectId: project.id, modelId: model.id, graphJson: original });
  const graphFile = join(root, 'models', model.id, 'graph.json'), diskBefore = await readFile(graphFile, 'utf8');
  browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } }); page.setDefaultTimeout(15000); page.setDefaultNavigationTimeout(60000);
  page.on('pageerror', e => errors.push(e.message)); page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
  await page.addInitScript(({ projectId, modelId }) => { localStorage.setItem('umcode.view', 'workbench'); localStorage.setItem('umcode.lastProject', projectId); localStorage.setItem('umcode.workbench.model.' + projectId, modelId); }, { projectId: project.id, modelId: model.id });
  await page.goto(server.resolvedUrls.local[0]);
  const workbench = page.getByRole('main', { name: 'ML Workbench' }), canvas = workbench.getByRole('region', { name: 'Model layer canvas' }); await canvas.waitFor();
  assert.equal(await page.locator('[data-workbench-chat]').count(), 0, 'Chat starts collapsed');
  await canvas.locator('.svelte-flow__node').filter({ has: page.locator('[data-layer-kind="linear"]') }).first().click();
  const requestCount = requests.length;
  await workbench.getByRole('button', { name: 'Ask about this layer', exact: true }).click();
  const chat = page.locator('[data-workbench-chat]'); await chat.waitFor();
  const message = chat.getByRole('textbox', { name: 'Message', exact: true }); await message.waitFor();
  assert.equal(requests.length, requestCount, 'Opening chat never calls the provider');
  assert.equal(await message.evaluate(el => document.activeElement === el), true);
  const chatBox = await chat.boundingBox(), composerBox = await chat.locator('[data-composer]').boundingBox();
  assert.ok(composerBox.x + composerBox.width <= chatBox.x + chatBox.width + 1, 'Composer stays inside the assistant panel');
  await page.screenshot({ path: join(artifacts, '01-layer-question.png'), animations: 'disabled' });
  async function ask(text) { await chat.getByRole('button', { name: 'Send', exact: true }).waitFor(); await message.fill(text); await message.press('Enter'); }
  await ask('Explain this layer'); await chat.getByText('This Linear layer maps', { exact: false }).waitFor();
  assert.equal(snapshots.at(-1).selectedNodeId, 'layer_1');
  assert.equal(snapshots.at(-1).graphJson, diskBefore, 'Context preserves exact loaded draft text');
  await chat.getByRole('button', { name: 'Send', exact: true }).waitFor();
  const beforeCancelled = snapshots.length;
  await page.evaluate(() => {
    window.__assistantDigest = crypto.subtle.digest;
    crypto.subtle.digest = async function (...args) { await new Promise(resolve => setTimeout(resolve, 500)); return window.__assistantDigest.apply(this, args); };
  });
  await message.fill('Explain cancelled question'); await message.press('Enter');
  await chat.getByRole('button', { name: 'Stop', exact: true }).click();
  await page.waitForTimeout(800);
  assert.equal(snapshots.length, beforeCancelled, 'Stop while preparing context never submits a question');
  assert.equal(await message.inputValue(), 'Explain cancelled question', 'Cancelled preparation retains the question');
  await page.evaluate(() => { crypto.subtle.digest = window.__assistantDigest; delete window.__assistantDigest; });
  await ask('Widen this layer');
  const proposal = chat.getByRole('region', { name: 'Suggested model changes' }).last(); await proposal.waitFor();
  await page.screenshot({ path: join(artifacts, '02-proposal.png'), animations: 'disabled' });
  assert.equal(await readFile(graphFile, 'utf8'), diskBefore, 'Assistant never writes the graph');
  await proposal.getByRole('button', { name: 'Preview changes', exact: true }).click();
  const preview = workbench.getByRole('region', { name: 'Model change preview' }); await preview.waitFor();
  await page.screenshot({ path: join(artifacts, '03-preview.png'), animations: 'disabled' });
  await preview.getByRole('button', { name: 'Cancel preview', exact: true }).click();
  await workbench.getByRole('button', { name: 'Undo model change', exact: true }).isDisabled().then(value => assert.equal(value, true));
  await proposal.getByRole('button', { name: 'Preview changes', exact: true }).click();
  await preview.getByRole('button', { name: 'Apply changes', exact: true }).click();
  await workbench.getByText('Unsaved changes', { exact: true }).waitFor();
  assert.equal(await readFile(graphFile, 'utf8'), diskBefore);
  await workbench.getByRole('button', { name: 'Undo model change', exact: true }).click();
  await workbench.getByText('Saved in project', { exact: true }).waitFor();
  await workbench.getByRole('button', { name: 'Redo model change', exact: true }).click();
  await page.screenshot({ path: join(artifacts, '04-applied-draft.png'), animations: 'disabled' });
  await workbench.getByRole('button', { name: 'Save model', exact: true }).click();
  await eventually(async () => { assert.equal(JSON.parse(await readFile(graphFile, 'utf8')).nodes[1].config.outFeatures, 64); });
  assert.deepEqual(errors, []);
  await writeFile(join(artifacts, 'report.json'), JSON.stringify({ realEngine: true, realProviderTransport: true, paidProvider: false, snapshots: snapshots.length, browserErrors: errors }, null, 2));
  console.log('Workbench assistant smoke passed; artifacts: ' + artifacts);
} catch (err) {
  await browser?.contexts()[0]?.pages()[0]?.screenshot({ path: join(artifacts, 'failure.png') }).catch(() => {});
  console.error('Browser errors:', errors); throw err;
} finally {
  ws?.close(); await browser?.close(); await server?.close(); provider.close();
  if (engine.exitCode === null) { engine.kill('SIGTERM'); await once(engine, 'exit'); }
  await writeFile(join(artifacts, 'engine.log'), engineLog); await rm(fixture, { recursive: true, force: true });
  await writeFile(join(artifacts, 'provider-requests.json'), JSON.stringify(requests, null, 2));
}
