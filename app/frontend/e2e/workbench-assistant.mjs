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
      const selected = graph.nodes.find(n => n.id === snapshot.selectedNodeId) ?? graph.nodes.find(n => n.kind === 'linear');
      let operations = [{ type: 'update', nodeId: selected.id, config: { outFeatures: question.includes('Invalid') ? -1 : selected.config.outFeatures === 64 ? 96 : 64 } }];
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
  assert.equal(await chat.getByRole('button', { name: /^Approval mode/ }).count(), 0, 'Model suggestions do not expose file-action approval controls');
  assert.equal((await message.getAttribute('placeholder')).includes('/ for skills'), false, 'Workbench guidance does not advertise unavailable coding skills');
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
  await eventually(async () => {
    const view = await canvas.boundingBox();
    for (const node of await canvas.locator('.svelte-flow__node').all()) {
      const box = await node.boundingBox();
      assert.ok(box.x >= view.x && box.y >= view.y && box.x + box.width <= view.x + view.width + 1 && box.y + box.height <= view.y + view.height + 1, 'Preview fits the full architecture after its layout settles');
    }
  });
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
  const proposals = () => chat.getByRole('region', { name: 'Suggested model changes' });
  async function askProposal(text) { const count = await proposals().count(); await ask(text); await eventually(async () => assert.equal(await proposals().count(), count + 1)); return proposals().nth(count); }
  async function editWidth(value) {
    await workbench.getByRole('button', { name: 'Fit model to canvas', exact: true }).click();
    await canvas.locator('[data-layer-kind="linear"]').first().click();
    const field = workbench.getByLabel('Output features', { exact: true }); await field.fill(String(value)); await field.press('Tab');
  }
  await editWidth(48);
  const unsaved = await askProposal('Widen this layer from my unsaved draft');
  assert.equal(JSON.parse(snapshots.at(-1).graphJson).nodes[1].config.outFeatures, 48, 'Assistant receives unsaved settings');
  assert.equal(JSON.parse(await readFile(graphFile, 'utf8')).nodes[1].config.outFeatures, 64);
  await editWidth(50);
  await unsaved.getByText('Model changed since this suggestion.', { exact: false }).waitFor();
  assert.equal(await unsaved.getByRole('button', { name: 'Preview changes', exact: true }).isDisabled(), true);
  await page.screenshot({ path: join(artifacts, '05-stale-suggestion.png'), animations: 'disabled' });
  const invalid = await askProposal('Invalid change');
  await invalid.getByText('This suggestion cannot be applied:', { exact: false }).waitFor();
  assert.equal(await invalid.getByRole('button', { name: 'Preview changes', exact: true }).isDisabled(), true);
  const insertion = await askProposal('Insert dropout after this layer');
  await insertion.getByRole('button', { name: 'Preview changes', exact: true }).click(); await preview.waitFor();
  assert.equal(await canvas.locator('[data-layer-kind="dropout"]').count(), 1);
  assert.equal(await workbench.getByRole('button', { name: 'Add Input layer', exact: true }).isDisabled(), true);
  assert.equal(await workbench.getByRole('button', { name: 'Save model', exact: true }).isDisabled(), true);
  const previewCount = await canvas.locator('.svelte-flow__node').count();
  await page.keyboard.press('Delete'); await page.keyboard.press('Meta+z');
  assert.equal(await canvas.locator('.svelte-flow__node').count(), previewCount, 'Preview cannot mutate the draft');
  await preview.getByRole('button', { name: 'Cancel preview', exact: true }).click();
  assert.equal(await canvas.locator('[data-layer-kind="dropout"]').count(), 0);
  await insertion.getByRole('button', { name: 'Preview changes', exact: true }).click();
  await preview.getByRole('button', { name: 'Apply changes', exact: true }).click();
  await workbench.getByRole('button', { name: 'Undo model change', exact: true }).click();
  assert.equal(await canvas.locator('[data-layer-kind="dropout"]').count(), 0);
  await workbench.getByRole('button', { name: 'Redo model change', exact: true }).click();
  assert.equal(await canvas.locator('[data-layer-kind="dropout"]').count(), 1);
  await workbench.getByRole('button', { name: 'Save model', exact: true }).click();
  await eventually(async () => assert.equal(JSON.parse(await readFile(graphFile, 'utf8')).nodes.length, 6));
  const partial = await askProposal('Partial model: remove Output');
  await partial.getByRole('button', { name: 'Preview changes', exact: true }).click();
  await preview.getByText('issue remains', { exact: false }).waitFor();
  await preview.getByRole('button', { name: 'Apply changes', exact: true }).click();
  assert.equal(await canvas.locator('[data-layer-kind="output"]').count(), 0);
  assert.equal(await workbench.getByRole('button', { name: 'Export .py', exact: true }).isDisabled(), true);
  await workbench.getByRole('button', { name: 'Undo model change', exact: true }).click();
  assert.equal(await canvas.locator('[data-layer-kind="output"]').count(), 1);
  const countBeforeDelayed = await proposals().count(), snapshotsBeforeDelayed = snapshots.length;
  await ask('Wait then widen this layer'); await eventually(async () => assert.equal(snapshots.length, snapshotsBeforeDelayed + 1));
  await editWidth(52);
  await eventually(async () => assert.equal(await proposals().count(), countBeforeDelayed + 1));
  await proposals().last().getByText('Model changed since this suggestion.', { exact: false }).waitFor();
  const copied = await rpc('workbench/model/create', { projectId: project.id, name: 'Different model', task: 'classification' });
  // Capture the current draft through the actual submission transport before cloning it.
  const captured = snapshots.length; await ask('Explain the current model'); await eventually(async () => assert.equal(snapshots.length, captured + 1));
  const currentText = snapshots.at(-1).graphJson;
  await rpc('workbench/graph/save', { projectId: project.id, modelId: copied.id, graphJson: currentText });
  await workbench.getByRole('button', { name: 'Refresh project models', exact: true }).click();
  await eventually(async () => assert.equal(await workbench.getByLabel('Project model', { exact: true }).isEnabled(), true));
  const beforeSwitch = await proposals().count(); await ask('Wait then widen this layer');
  await workbench.getByLabel('Project model', { exact: true }).selectOption(copied.id);
  await eventually(async () => assert.equal(await proposals().count(), beforeSwitch + 1));
  await proposals().last().getByText('Open Feature classifier', { exact: false }).waitFor();
  assert.equal(await proposals().last().getByRole('button', { name: 'Preview changes', exact: true }).isDisabled(), true);
  await workbench.getByLabel('Project model', { exact: true }).selectOption(model.id); await canvas.waitFor();
  await workbench.getByText('Unsaved changes', { exact: true }).waitFor();
  const otherRoot = join(fixture, 'other-project'); await mkdir(otherRoot);
  const other = await rpc('project/create', { root: otherRoot, name: 'Other project' });
  await workbench.getByLabel('Workbench project', { exact: true }).selectOption(other.id);
  await page.getByRole('region', { name: 'Project chat', exact: true }).waitFor();
  assert.equal(await preview.count(), 0);
  await workbench.getByLabel('Workbench project', { exact: true }).selectOption(project.id); await canvas.waitFor();
  await workbench.getByText('Unsaved changes', { exact: true }).waitFor();
  const beforeForbidden = await proposals().count(); await ask('Try a forbidden file tool');
  await chat.getByRole('button', { name: 'Send', exact: true }).waitFor();
  assert.equal(await proposals().count(), beforeForbidden);
  await assert.rejects(readFile(join(root, 'assistant-escaped.txt')), { code: 'ENOENT' });
  const layouts = [];
  for (const theme of ['light', 'dark']) for (const [width, height] of [[760, 520], [1180, 780], [1440, 900]]) {
    await page.emulateMedia({ colorScheme: theme }); await page.setViewportSize({ width, height });
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    if (!await chat.count()) await workbench.getByRole('button', { name: 'Show workbench chat', exact: true }).click();
    await message.waitFor({ state: 'visible' });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'No horizontal page overflow');
    const box = await chat.boundingBox(), compose = await chat.locator('[data-composer]').boundingBox();
    assert.ok(compose.x + compose.width <= box.x + box.width + 1, 'Composer fits at ' + width);
    await page.screenshot({ path: join(artifacts, `06-chat-${theme}-${width}.png`), animations: 'disabled' });
    const buttons = await chat.getByRole('button', { name: 'Preview changes', exact: true }).all();
    let enabled; for (const button of buttons.reverse()) if (await button.isEnabled()) { enabled = button; break; }
    assert.ok(enabled, 'Current model suggestion is available after navigation'); await enabled.click(); await preview.waitFor();
    if (width < 1180) assert.equal(await chat.count(), 0, 'Preview closes the narrow overlay');
    const applyBox = await preview.getByRole('button', { name: 'Apply changes', exact: true }).boundingBox();
    assert.ok(applyBox.y >= 0 && applyBox.y + applyBox.height <= height, 'Apply is reachable at ' + width);
    await page.screenshot({ path: join(artifacts, `07-preview-${theme}-${width}.png`), animations: 'disabled' });
    await preview.getByRole('button', { name: 'Cancel preview', exact: true }).click();
    layouts.push({ theme, width, height, passed: true });
  }
  await page.emulateMedia({ colorScheme: 'light' }); await page.setViewportSize({ width: 1440, height: 900 });
  if (!await chat.count()) await workbench.getByRole('button', { name: 'Show workbench chat', exact: true }).click();
  const beforeCancelTool = await proposals().count(), beforeCancelSnapshot = snapshots.length;
  await ask('Wait then widen, cancellation test'); await eventually(async () => assert.equal(snapshots.length, beforeCancelSnapshot + 1));
  await chat.getByRole('button', { name: 'Stop', exact: true }).click(); await chat.getByRole('button', { name: 'Send', exact: true }).waitFor();
  await page.waitForTimeout(1400); assert.equal(await proposals().count(), beforeCancelTool, 'Cancelled tool never becomes actionable');
  const beforeContextFailure = snapshots.length;
  await page.evaluate(() => { window.__assistantDigest = crypto.subtle.digest; crypto.subtle.digest = async () => { throw new Error('Fixture context error'); }; });
  await ask('Keep my question after a context error'); await page.getByText('Fixture context error', { exact: true }).waitFor();
  assert.equal(await message.inputValue(), 'Keep my question after a context error'); assert.equal(snapshots.length, beforeContextFailure);
  await page.evaluate(() => { crypto.subtle.digest = window.__assistantDigest; delete window.__assistantDigest; });
  const beforeProviderFailure = await proposals().count(); await ask('Provider failure');
  await chat.getByText('Fixture provider rejected this request', { exact: true }).waitFor();
  await chat.getByRole('button', { name: 'Send', exact: true }).waitFor(); assert.equal(await proposals().count(), beforeProviderFailure);
  await workbench.getByRole('button', { name: 'Save model', exact: true }).click(); await workbench.getByText('Saved in project', { exact: true }).waitFor();
  const threads = await rpc('thread/list', { projectId: project.id, archived: false, limit: 20 }), savedThread = threads.threads[0];
  const history = await rpc('thread/read', { threadId: savedThread.id });
  assert.ok(history.items.some(item => item.data?.workbenchProposal));
  await page.reload(); await canvas.waitFor();
  await page.getByRole('button', { name: 'Expand sidebar', exact: true }).click();
  await page.getByRole('dialog', { name: 'Projects and chats' }).getByText(savedThread.title, { exact: true }).click();
  await page.getByRole('button', { name: 'Workbench', exact: true }).click();
  if (!await chat.count()) await workbench.getByRole('button', { name: 'Show workbench chat', exact: true }).click();
  await eventually(async () => assert.ok(await proposals().count() > 0, 'Persisted proposals render after reload'));
  assert.equal(JSON.parse(await readFile(graphFile, 'utf8')).nodes[1].config.outFeatures, 52);
  await workbench.getByRole('button', { name: 'Back to models', exact: true }).click();
  await message.fill('Keep this question until a model is selected');
  const beforeNoModel = snapshots.length;
  assert.equal(await chat.getByRole('button', { name: 'Send', exact: true }).isDisabled(), true);
  await message.press('Enter'); await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
  assert.equal(snapshots.length, beforeNoModel, 'No model cannot fall back to generic coding tools');
  assert.equal(await message.inputValue(), 'Keep this question until a model is selected');
  assert.deepEqual(errors, []);
  await writeFile(join(artifacts, 'report.json'), JSON.stringify({ realEngine: true, realProviderTransport: true, paidProvider: false, snapshots: snapshots.length, layouts, workflows: ['selected-layer questions', 'unsaved context', 'explanations', 'preview/cancel', 'apply/undo/redo/save', 'insert/reconnect', 'partial models', 'stale draft/model/project', 'forbidden tool', 'context failure preserves question', 'provider failure', 'cancel during capture/provider call', 'history reload'], browserErrors: errors }, null, 2));
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
