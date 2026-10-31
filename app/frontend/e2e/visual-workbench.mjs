// Real-engine visual builder smoke and workflow screenshots, using an isolated home.
// UMCODE_TEST_ENGINE=/absolute/path/to/umcode PLAYWRIGHT_MODULE=/path/to/playwright-core/index.mjs node e2e/visual-workbench.mjs
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { createServer as netServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'vite';

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright-core');
assert.ok(process.env.UMCODE_TEST_ENGINE, 'Set UMCODE_TEST_ENGINE to the built engine binary');
const artifacts = process.env.UI_ARTIFACT_DIR || join(tmpdir(), 'umcode-workbench-smoke');
await mkdir(artifacts, { recursive: true });
const fixture = await mkdtemp(join(tmpdir(), 'umc-ml-'));
const home = join(fixture, 'home');
const rootA = join(fixture, 'project-a'), rootB = join(fixture, 'project-b');
await Promise.all([home, rootA, rootB].map(p => mkdir(p)));
const listener = netServer();
listener.listen(0, '127.0.0.1');
await once(listener, 'listening');
const port = listener.address().port;
await new Promise(resolve => listener.close(resolve));
process.env.UMCODE_HOME = home;
process.env.UMCODE_ENGINE_WS_PORT = String(port);
const engine = spawn(process.env.UMCODE_TEST_ENGINE, ['engine'], { env: { ...process.env, UMCODE_NO_SERVICE: '1' }, stdio: ['ignore', 'pipe', 'pipe'] });
let engineLog = '';
engine.stdout.on('data', data => { engineLog += data; });
engine.stderr.on('data', data => { engineLog += data; });
const errors = [];
let ws, server, browser;
async function eventually(fn) {
  const end = Date.now() + 20000;
  for (;;) {
    try { return await fn(); } catch (err) {
      if (Date.now() >= end || engine.exitCode !== null) throw err;
      await new Promise(resolve => setTimeout(resolve, 100));
    }
  }
}
try {
  const token = await eventually(async () => (await readFile(join(home, 'run/token'), 'utf8')).trim());
  ws = await eventually(async () => {
    const socket = new WebSocket(`ws://127.0.0.1:${port}/ws?token=${encodeURIComponent(token)}`);
    await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject; });
    return socket;
  });
  let nextId = 0;
  const pending = new Map();
  ws.onmessage = event => {
    const message = JSON.parse(event.data), waiter = pending.get(message.id);
    if (!waiter) return;
    pending.delete(message.id);
    clearTimeout(waiter.timer);
    if (message.error) waiter.reject(new Error(message.error.message)); else waiter.resolve(message.result);
  };
  const rpc = (method, params = {}) => new Promise((resolve, reject) => {
    const id = ++nextId;
    const timer = setTimeout(() => { pending.delete(id); reject(new Error(`Timed out: ${method}`)); }, 10000);
    pending.set(id, { resolve, reject, timer });
    ws.send(JSON.stringify({ jsonrpc: '2.0', id, method, params }));
  });
  await rpc('initialize', { clientName: 'workbench-smoke', clientVersion: '0.4.2', protocolVersion: '1.0.0', admin: true });
  const a = await rpc('project/create', { name: 'Workbench demo', root: rootA });
  const b = await rpc('project/create', { name: 'Separate project', root: rootB });
  server = await createServer({ root: fileURLToPath(new URL('..', import.meta.url)), server: { host: '127.0.0.1', port: 0, strictPort: false } });
  await server.listen();
  browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  page.setDefaultTimeout(15000);
  page.setDefaultNavigationTimeout(60000);
  page.on('pageerror', e => errors.push(e.message));
  page.on('console', msg => { if (msg.type() === 'error') errors.push(msg.text()); });
  page.on('dialog', dialog => dialog.accept());
  await page.addInitScript(projectId => {
    localStorage.setItem('umcode.view', 'workbench');
    if (!localStorage.getItem('umcode.lastProject')) localStorage.setItem('umcode.lastProject', projectId);
  }, a.id);
  await page.goto(server.resolvedUrls.local[0]);
  const workbench = page.getByRole('main', { name: 'ML Workbench' });
  await workbench.getByRole('heading', { name: 'What does this model do?' }).waitFor();
  await workbench.getByLabel('Model name', { exact: true }).fill('Visual model');
  await workbench.getByLabel('Starting point', { exact: true }).selectOption('blank');
  await page.screenshot({ path: join(artifacts, 'create-text-model.png'), animations: 'disabled' });
  await workbench.getByRole('button', { name: 'Create model', exact: true }).click();
  const canvas = workbench.getByRole('region', { name: 'Model layer canvas' });
  await canvas.waitFor();
  assert.equal(await workbench.locator('textarea').count(), 0, 'No JSON editor');
  await workbench.getByRole('button', { name: 'Add Input layer', exact: true }).click();
  const issues = workbench.getByRole('region', { name: 'Model issues', exact: true });
  await issues.getByText('Add an Output layer and connect Input to it.', { exact: true }).waitFor();
  await workbench.getByRole('button', { name: 'Hide model issues', exact: true }).click();
  await issues.waitFor({ state: 'detached' });
  await workbench.getByRole('button', { name: 'Show model issues', exact: true }).click();
  await issues.waitFor();
  await workbench.getByRole('button', { name: 'Undo model change', exact: true }).click();
  const card = kind => canvas.locator('[data-layer-kind="' + kind + '"]');
  const closeProperties = async () => {
    const close = workbench.getByRole('button', { name: 'Close layer properties', exact: true });
    if (await close.count()) await close.click();
  };
  const waitNodes = async count => page.waitForFunction(n => document.querySelectorAll('[data-model-canvas] .svelte-flow__node').length === n, count);
  for (const [kind, y] of [['input', 90], ['embedding', 240], ['output', 390]]) {
    await closeProperties();
    await workbench.locator('[data-palette-kind="' + kind + '"]').dragTo(canvas, { targetPosition: { x: 280, y } });
    await card(kind).waitFor();
  }
  await closeProperties();
  const wire = async (source, target) => {
    await card(source).locator('.source').dragTo(card(target).locator('.target'));
  };
  await wire('input', 'embedding'); await wire('embedding', 'output');
  await page.waitForFunction(() => document.querySelectorAll('[data-model-canvas] .svelte-flow__edge').length === 2);
  await workbench.getByText('✓ Layer sizes match', { exact: true }).waitFor();
  // Drag the actual node, then assert the persisted position changes.
  const inputBox = await card('input').boundingBox();
  await page.mouse.move(inputBox.x + 40, inputBox.y + 20);
  await page.mouse.down(); await page.mouse.move(inputBox.x + 95, inputBox.y + 40, { steps: 10 }); await page.mouse.up();
  await card('embedding').click();
  const dimensions = workbench.getByLabel('Embedding dimensions', { exact: true });
  await dimensions.fill('24'); await dimensions.press('Tab');
  await workbench.getByText('[B × 8 × 24]', { exact: true }).first().waitFor();
  await workbench.getByRole('button', { name: 'Undo model change' }).click();
  assert.equal(await dimensions.inputValue(), '16');
  await workbench.getByRole('button', { name: 'Redo model change' }).click();
  assert.equal(await dimensions.inputValue(), '24');
  await workbench.getByRole('button', { name: 'Remove layer', exact: true }).click();
  await waitNodes(2);
  await workbench.getByRole('button', { name: 'Undo model change' }).click();
  await waitNodes(3);
  await closeProperties();
  await workbench.getByRole('button', { name: 'Back to models', exact: true }).click();
  const modelChooser = workbench.getByRole('region', { name: 'Project models', exact: true });
  await modelChooser.waitFor();
  assert.equal(await canvas.count(), 0, 'Back closes the editor');
  await modelChooser.getByRole('button', { name: 'Open Visual model', exact: true }).getByText('Unsaved changes', { exact: true }).waitFor();
  await page.screenshot({ path: join(artifacts, 'models-with-unsaved-draft.png'), animations: 'disabled' });
  await modelChooser.getByRole('button', { name: 'Open Visual model', exact: true }).click();
  await canvas.waitFor(); await card('embedding').click();
  assert.equal(await dimensions.inputValue(), '24', 'Close/reopen retains the unsaved architecture');
  await closeProperties();
  const downloadPromise = page.waitForEvent('download');
  await workbench.getByRole('button', { name: 'Export .py', exact: true }).click();
  const download = await downloadPromise;
  const exportPath = join(artifacts, 'visual-model.py'); await download.saveAs(exportPath);
  assert.ok((await readFile(exportPath, 'utf8')).includes('class Model(nn.Module)'));
  await workbench.getByRole('button', { name: 'Save model', exact: true }).click();
  await workbench.getByText('Model saved to your project.', { exact: true }).waitFor();
  const saved = JSON.parse(await readFile(join(rootA, 'models/visual-model/graph.json'), 'utf8'));
  assert.equal(saved.nodes.length, 3); assert.equal(saved.edges.length, 2);
  assert.equal(saved.nodes.find(n => n.kind === 'embedding').config.width, 24);
  await page.reload(); await canvas.waitFor(); await waitNodes(3);
  for (const tab of ['Data', 'Train', 'Inference']) {
    await workbench.getByRole('button', { name: tab, exact: true }).click();
    if (tab === 'Train') await page.screenshot({ path: join(artifacts, 'train-not-implemented.png'), animations: 'disabled' });
    await workbench.getByRole('button', { name: 'Back to models', exact: true }).click();
    await modelChooser.waitFor();
    await modelChooser.getByRole('button', { name: 'Open Visual model', exact: true }).click();
    await canvas.waitFor();
  }
  await page.setViewportSize({ width: 760, height: 520 });
  const backBounds = await workbench.getByRole('button', { name: 'Back to models', exact: true }).boundingBox();
  assert.ok(backBounds.x >= 0 && backBounds.x + backBounds.width <= 760 && backBounds.y + backBounds.height <= 520, 'Back is reachable in a narrow window');
  await workbench.getByRole('button', { name: 'Back to models', exact: true }).click();
  await modelChooser.waitFor();
  await page.screenshot({ path: join(artifacts, 'model-chooser-narrow.png'), animations: 'disabled' });
  await modelChooser.getByRole('button', { name: 'Open Visual model', exact: true }).click();
  await canvas.waitFor(); await page.setViewportSize({ width: 1440, height: 900 });
  await card('embedding').click(); assert.equal(await dimensions.inputValue(), '24');
  await dimensions.fill('10000000000000000'); await dimensions.press('Tab');
  await workbench.getByRole('alert').filter({ hasText: /Previous value kept/ }).waitFor();
  assert.equal(await dimensions.inputValue(), '24');
  assert.equal(await canvas.count(), 1, 'Invalid settings retain the editable canvas');
  await closeProperties();
  await card('input').click();
  await card('input').locator('..').focus(); await page.keyboard.press('ArrowRight');
  assert.equal(await workbench.getByRole('button', { name: 'Save model', exact: true }).isEnabled(), true, 'Keyboard movement is a persisted model change');
  await workbench.getByRole('button', { name: 'Save model', exact: true }).click();
  await workbench.getByText('Model saved to your project.', { exact: true }).waitFor();
  await closeProperties();
  await workbench.getByRole('button', { name: 'Small text model', exact: true }).click();
  await page.getByRole('button', { name: 'Use starter', exact: true }).click();
  await waitNodes(7);
  await card('transformer').click();
  const heads = workbench.getByLabel('Attention heads', { exact: true });
  await heads.fill('3'); await heads.press('Tab');
  await issues.getByRole('button', { name: 'Transformer block: Model dimensions must be divisible by attention heads.', exact: true }).waitFor();
  await closeProperties();
  await issues.getByRole('button', { name: 'Transformer block: Model dimensions must be divisible by attention heads.', exact: true }).click();
  await workbench.getByRole('complementary', { name: 'Layer properties' }).waitFor();
  await page.screenshot({ path: join(artifacts, 'validation-messages.png'), animations: 'disabled' });
  assert.equal(await workbench.getByRole('button', { name: 'Export .py', exact: true }).isDisabled(), true);
  await workbench.getByRole('button', { name: 'Undo model change' }).click();
  assert.equal(await heads.inputValue(), '4');
  await closeProperties();
  await workbench.getByRole('button', { name: 'Save model', exact: true }).click();
  await workbench.getByText('Model saved to your project.', { exact: true }).waitFor();
  await workbench.getByRole('button', { name: 'Code', exact: true }).click();
  await card('transformer').click();
  const code = workbench.getByRole('complementary', { name: 'Generated PyTorch code' });
  await code.waitFor(); assert.ok(await code.locator('.highlight').count() > 0, 'Selection highlights code');
  await page.screenshot({ path: join(artifacts, 'visual-code.png'), animations: 'disabled' });
  await workbench.getByRole('button', { name: 'Close code preview', exact: true }).click();
  const layouts = [];
  for (const theme of ['light', 'dark']) for (const [width, height] of [[760, 520], [1180, 780], [1440, 900]]) {
    await page.emulateMedia({ colorScheme: theme }); await page.setViewportSize({ width, height });
    await workbench.getByRole('button', { name: 'Fit model to canvas' }).click();
    await card('transformer').click();
    await workbench.getByRole('complementary', { name: 'Layer properties' }).waitFor();
    const param = await heads.boundingBox();
    assert.ok(param.x >= 0 && param.x + param.width <= width + 1, 'Settings reachable at ' + width);
    const save = await workbench.getByRole('button', { name: 'Save model', exact: true }).boundingBox();
    assert.ok(save.y + save.height <= height + 1 && save.x + save.width <= width + 1, 'Save reachable at ' + width);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'No page overflow');
    await page.screenshot({ path: join(artifacts, 'visual-' + theme + '-' + width + '.png'), animations: 'disabled' });
    await closeProperties();
    if (width < 1180) {
      await workbench.getByRole('button', { name: 'Show workbench chat' }).click();
      await page.getByRole('dialog', { name: 'Workbench chat' }).waitFor();
      await page.keyboard.press('Escape');
    }
    layouts.push({ theme, width, height, passed: true });
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await card('transformer').click();
  const repeats = workbench.getByLabel('Repeat blocks', { exact: true });
  await repeats.fill('3'); await repeats.press('Tab');
  await workbench.getByLabel('Workbench project', { exact: true }).selectOption(b.id);
  await workbench.getByRole('heading', { name: 'What does this model do?' }).waitFor();
  await workbench.getByLabel('Workbench project', { exact: true }).selectOption(a.id);
  await canvas.waitFor(); await card('transformer').click();
  assert.equal(await repeats.inputValue(), '3', 'Unsaved architecture survives project switching');
  await workbench.getByRole('button', { name: 'Reload model from disk' }).click();
  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  assert.equal(await repeats.inputValue(), '3', 'Reload cancellation keeps edits');
  await workbench.getByRole('button', { name: 'Reload model from disk' }).click();
  await page.getByRole('button', { name: 'Discard and reload', exact: true }).click();
  await page.waitForFunction(() => document.querySelector('#layer-param-repeat')?.value === '2');
  // Existing arbitrary P0 content cannot be silently erased by the visual editor.
  const legacy = await rpc('workbench/model/create', { projectId: a.id, name: 'Legacy model', task: 'custom' });
  await rpc('workbench/graph/save', { projectId: a.id, modelId: legacy.id, graphJson: '{"version":1,"nodes":[{"id":"legacy"}],"edges":[]}' });
  await workbench.getByRole('button', { name: 'Refresh project models' }).click();
  await workbench.getByLabel('Project model', { exact: true }).selectOption(legacy.id);
  await workbench.getByText('This model needs a different builder', { exact: true }).waitFor();
  const intact = await rpc('workbench/model/open', { projectId: a.id, modelId: legacy.id });
  assert.equal(intact.graph.nodes[0].id, 'legacy');
  await workbench.getByRole('button', { name: 'New model', exact: true }).click();
  await workbench.getByRole('button', { name: /^Classification/ }).click();
  await workbench.getByLabel('Model name', { exact: true }).fill('Ticket topics');
  await workbench.getByRole('button', { name: 'Create model', exact: true }).click();
  await canvas.waitFor(); await waitNodes(7); await card('meanpool').waitFor();
  await card('linear').last().click();
  await page.screenshot({ path: join(artifacts, 'text-classifier.png'), animations: 'disabled' });
  await closeProperties();
  const classification = JSON.parse(await readFile(join(rootA, 'models/ticket-topics/model.json'), 'utf8'));
  assert.equal(classification.task, 'classification');
  await workbench.getByRole('button', { name: 'New model', exact: true }).click();
  await workbench.getByLabel('Model name', { exact: true }).fill('Ticket topics');
  await workbench.getByRole('button', { name: 'Create model', exact: true }).click();
  await workbench.getByRole('alert').filter({ hasText: /already exists/i }).waitFor();
  for (const preset of ['cnn', 'vit']) {
    await workbench.getByRole('button', { name: 'New model', exact: true }).click();
    await workbench.getByRole('button', { name: /^Classification/ }).click();
    await workbench.getByLabel('Input type', { exact: true }).selectOption('image');
    assert.deepEqual(await workbench.getByLabel('Starting point', { exact: true }).locator('option').evaluateAll(options => options.map(o => o.value)), ['blank', 'cnn', 'vit']);
    await workbench.getByLabel('Starting point', { exact: true }).selectOption(preset);
    await workbench.getByLabel('Model name', { exact: true }).fill('Vision ' + preset);
    if (preset === 'cnn') await page.screenshot({ path: join(artifacts, 'create-vision-model.png'), animations: 'disabled' });
    await workbench.getByRole('button', { name: 'Create model', exact: true }).click();
    await canvas.waitFor(); await waitNodes(preset === 'cnn' ? 10 : 8);
    await workbench.getByRole('complementary', { name: 'Layer palette', exact: true }).getByRole('heading', { name: 'Vision', exact: true }).waitFor();
    await card('input').click();
    assert.equal(await workbench.getByLabel('Input type', { exact: true }).inputValue(), 'image');
    assert.equal(await workbench.getByLabel('Sequence length', { exact: true }).count(), 0);
    assert.equal(await workbench.getByLabel('Input features', { exact: true }).count(), 0);
    if (preset === 'cnn') {
      for (const [field, value] of [['Image channels', '1'], ['Image height', '28'], ['Image width', '30']]) {
        const input = workbench.getByLabel(field, { exact: true }); await input.fill(value); await input.press('Tab');
      }
      await closeProperties(); await card('conv2d').first().click();
      const channels = workbench.getByLabel('Output channels', { exact: true }); await channels.fill('24'); await channels.press('Tab');
      await closeProperties(); await card('maxpool2d').click();
      const padding = workbench.getByLabel('Padding', { exact: true }); await padding.fill('2'); await padding.press('Tab');
      await issues.getByRole('button', { name: /Max pool 2D: Pooling padding/ }).waitFor();
      await workbench.getByRole('button', { name: 'Undo model change', exact: true }).click();
    } else {
      const imageWidth = workbench.getByLabel('Image width', { exact: true }); await imageWidth.fill('30'); await imageWidth.press('Tab');
      await issues.getByRole('button', { name: 'Patch embedding: Image height and width must be divisible by patch size.', exact: true }).waitFor();
      await imageWidth.fill('32'); await imageWidth.press('Tab');
      await closeProperties(); await card('patchembedding').click();
      const patch = workbench.getByLabel('Patch size', { exact: true }); await patch.fill('8'); await patch.press('Tab');
      await closeProperties(); await card('transformer').click();
      assert.equal(await workbench.getByLabel('Causal mask', { exact: true }).isChecked(), false);
    }
    await workbench.getByText('✓ Layer sizes match', { exact: true }).waitFor();
    await workbench.getByRole('button', { name: 'Save model', exact: true }).click();
    await workbench.getByText('Model saved to your project.', { exact: true }).waitFor();
    const disk = JSON.parse(await readFile(join(rootA, 'models/vision-' + preset + '/graph.json'), 'utf8'));
    assert.equal(disk.nodes[0].config.mode, 'image');
    if (preset === 'cnn') { assert.equal(disk.nodes[0].config.channels, 1); assert.equal(disk.nodes.find(n => n.kind === 'conv2d').config.channels, 24); }
    else { assert.equal(disk.nodes.find(n => n.kind === 'patchembedding').config.patch, 8); assert.equal(disk.nodes.find(n => n.kind === 'transformer').config.causal, false); }
    await page.reload(); await canvas.waitFor(); await card('input').click();
    assert.equal(await workbench.getByLabel('Image channels', { exact: true }).inputValue(), preset === 'cnn' ? '1' : '3');
    assert.equal(await workbench.getByRole('button', { name: 'Save model', exact: true }).isDisabled(), true, 'Selection does not dirty the image model');
    const exporting = page.waitForEvent('download');
    await workbench.getByRole('button', { name: 'Export .py', exact: true }).click();
    await (await exporting).saveAs(join(artifacts, 'vision-' + preset + '.py'));
    if (preset === 'vit') {
      await closeProperties(); await card('transformer').click();
      assert.equal(await workbench.getByLabel('Causal mask', { exact: true }).isChecked(), false);
      await page.screenshot({ path: join(artifacts, 'vision-transformer-settings.png'), animations: 'disabled' });
    }
    for (const theme of ['light', 'dark']) for (const [width, height] of [[760, 520], [1180, 780], [1440, 900]]) {
      await closeProperties(); await page.emulateMedia({ colorScheme: theme }); await page.setViewportSize({ width, height });
      await workbench.getByRole('button', { name: 'Fit model to canvas' }).click(); await card('input').click();
      await workbench.getByLabel('Image width', { exact: true }).scrollIntoViewIfNeeded();
      const save = await workbench.getByRole('button', { name: 'Save model', exact: true }).boundingBox();
      assert.ok(save.y + save.height <= height + 1 && save.x + save.width <= width + 1, 'Vision controls fit at ' + width);
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'No vision layout overflow');
      await page.screenshot({ path: join(artifacts, 'vision-' + preset + '-' + theme + '-' + width + '.png'), animations: 'disabled' });
      layouts.push({ preset, theme, width, height, passed: true });
    }
    await closeProperties(); await page.setViewportSize({ width: 1440, height: 900 });
  }
  await workbench.getByRole('button', { name: 'New model', exact: true }).click();
  await workbench.getByRole('button', { name: /^Classification/ }).click();
  await workbench.getByLabel('Input type', { exact: true }).selectOption('features');
  assert.equal(await workbench.getByLabel('Starting point', { exact: true }).inputValue(), 'mlp');
  await workbench.getByLabel('Model name', { exact: true }).fill('Feature classifier');
  await workbench.getByRole('button', { name: 'Create model', exact: true }).click();
  await canvas.waitFor(); await waitNodes(5); await card('input').click();
  assert.equal(await workbench.getByLabel('Input type', { exact: true }).inputValue(), 'features');
  assert.equal(await workbench.getByLabel('Input features', { exact: true }).inputValue(), '16');
  assert.equal(await workbench.getByLabel('Image channels', { exact: true }).count(), 0);
  await page.screenshot({ path: join(artifacts, 'feature-classifier.png'), animations: 'disabled' });
  await closeProperties();
  await workbench.getByRole('button', { name: 'New model', exact: true }).click();
  await workbench.getByRole('button', { name: /^Custom/ }).click();
  await workbench.getByLabel('Input type', { exact: true }).selectOption('image');
  await workbench.getByLabel('Starting point', { exact: true }).selectOption('blank');
  await workbench.getByLabel('Model name', { exact: true }).fill('Custom vision');
  await workbench.getByRole('button', { name: 'Create model', exact: true }).click();
  await canvas.waitFor(); await waitNodes(1); await card('input').click();
  assert.equal(await workbench.getByLabel('Input type', { exact: true }).inputValue(), 'image');
  assert.equal(JSON.parse(await readFile(join(rootA, 'models/custom-vision/model.json'), 'utf8')).task, 'custom');
  await closeProperties();
  const inputNodeBox = await card('input').boundingBox(), canvasBox = await canvas.boundingBox();
  await workbench.getByRole('button', { name: 'Add Conv2d layer', exact: true }).dragTo(canvas, { targetPosition: { x: Math.min(canvasBox.width - 110, inputNodeBox.x - canvasBox.x + inputNodeBox.width / 2), y: Math.min(canvasBox.height - 70, inputNodeBox.y - canvasBox.y + inputNodeBox.height / 2 + 160) } });
  await waitNodes(2); await closeProperties(); await wire('input', 'conv2d');
  await card('conv2d').click();
  assert.equal(await workbench.getByLabel('Output channels', { exact: true }).inputValue(), '16');
  await workbench.getByText('[B × 16 × 32 × 32]', { exact: true }).first().waitFor();
  await page.screenshot({ path: join(artifacts, 'custom-vision-building.png'), animations: 'disabled' });
  await closeProperties();
  await workbench.getByRole('button', { name: 'Back to models', exact: true }).click();
  await modelChooser.waitFor();
  await page.screenshot({ path: join(artifacts, 'all-models.png'), animations: 'disabled' });
  await modelChooser.getByRole('button', { name: 'Open Custom vision', exact: true }).click();
  await canvas.waitFor(); await waitNodes(2);
  const thread = await rpc('thread/start', { projectId: a.id, channel: 'app', title: 'Project A conversation' });
  await page.reload(); await workbench.waitFor();
  await page.getByRole('button', { name: 'Expand sidebar', exact: true }).click();
  await page.getByRole('dialog', { name: 'Projects and chats' }).getByText('Project A conversation', { exact: true }).click();
  await page.getByRole('button', { name: 'Workbench', exact: true }).click();
  await workbench.getByLabel('Workbench project', { exact: true }).selectOption(b.id);
  await page.getByRole('region', { name: 'Project chat', exact: true }).waitFor();
  assert.equal(await page.locator('[data-workbench-chat] [data-composer]').count(), 0);
  await page.getByRole('button', { name: 'Start a chat here', exact: true }).click();
  await page.locator('[data-workbench-chat] [data-composer]').waitFor();
  assert.equal((await rpc('thread/read', { threadId: thread.id })).thread.projectId, a.id);
  assert.deepEqual(errors, [], 'No browser errors');
  await writeFile(join(artifacts, 'report.json'), JSON.stringify({ realEngine: true, flows: ['text/vision/feature starter selection', 'palette drag/drop', 'connect ports', 'pointer/keyboard movement', 'properties', 'actionable validation messages', 'undo/redo', 'delete', 'code selection', 'Python download', 'save/reopen', 'back to models and unsaved draft retention', 'text classification', 'feature classification', 'CNN editing', 'noncausal vision Transformer', 'custom vision', 'project draft retention', 'reload confirmation', 'legacy protection', 'duplicate', 'chat project guard'], layouts, browserErrors: errors }, null, 2));
  console.log('Visual workbench smoke passed: real engine, text/CNN/ViT/custom interactions, ' + layouts.length + ' layouts; artifacts: ' + artifacts);
} catch (err) {
  if (browser) await browser.contexts()[0]?.pages()[0]?.screenshot({ path: join(artifacts, 'failure.png') }).catch(() => {});
  console.error('Browser errors:', errors);
  throw err;
} finally {
  ws?.close();
  await browser?.close();
  await server?.close();
  if (engine.exitCode === null) {
    engine.kill('SIGTERM');
    await once(engine, 'exit');
  }
  await writeFile(join(artifacts, 'engine.log'), engineLog);
  await rm(fixture, { recursive: true, force: true });
}
