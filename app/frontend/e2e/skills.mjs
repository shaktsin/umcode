// Isolated browser test of the real composer. Only the engine RPC boundary is faked.
// PLAYWRIGHT_MODULE may point to an installed playwright-core module.
import { createServer } from 'vite';
import { fileURLToPath } from 'node:url';
import assert from 'node:assert/strict';

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright-core');
const root = fileURLToPath(new URL('..', import.meta.url));
const html = `<!doctype html><html><head><meta charset="utf-8"><link rel="icon" href="data:,"></head><body>
<main style="max-width:800px;margin:300px auto 0"><div id="main"></div><div id="side"></div></main>
<script type="module">
import '/src/app.css';
import { mount } from 'svelte';
import Composer from '/src/lib/components/Composer.svelte';
import { app } from '/src/lib/stores/app.svelte';
import { chat, ThreadView } from '/src/lib/stores/chat.svelte';
import { projects } from '/src/lib/stores/projects.svelte';
const state = window.skillTest = { calls: [], mode: 'normal', delay: 0 };
projects.activeId = 'main-project';
app.conn = 'open';
app.call = async (method, params) => {
  state.calls.push({ method, params });
  if (method === 'skill/list') {
    const mode = state.mode;
    if (state.delay) await new Promise(resolve => setTimeout(resolve, state.delay));
    if (mode === 'error') throw new Error('Test load failure');
    return { skills: mode === 'empty' ? [] : [
      { name: 'tools:review', description: 'Inspect code changes' },
      { name: 'tools:test', description: 'Check test coverage' },
      { name: 'broken', description: 'Invalid skill', error: 'invalid' }
    ] };
  }
  if (method === 'thread/start') return { id: 'new-thread', projectId: params.projectId, updatedAt: '', settings: {} };
  if (method === 'turn/start') chat.main.sending = false;
  return {};
};
mount(Composer, { target: document.getElementById('main'), props: { view: chat.main } });
const side = new ThreadView();
side.id = 'side-thread';
side.thread = { id: side.id, projectId: 'side-project', settings: {} };
mount(Composer, { target: document.getElementById('side'), props: { view: side, compact: true } });
</script></body></html>`;

const server = await createServer({
  root,
  server: { port: 0, strictPort: false, host: '127.0.0.1' },
  plugins: [{ name: 'skill-test-page', configureServer(server) {
    server.middlewares.use('/__skills_test.html', async (_req, res) => {
      res.setHeader('Content-Type', 'text/html');
      res.end(await server.transformIndexHtml('/__skills_test.html', html));
    });
  } }],
});
let browser;
try {
  await server.listen();
  browser = await chromium.launch({
    executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    headless: true,
  });
  const page = await browser.newPage({ viewport: { width: 1100, height: 900 } });
  page.setDefaultTimeout(5000);
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('console', (message) => { if (message.type() === 'error') errors.push(message.text()); });
  page.on('response', (response) => { if (response.status() >= 400) errors.push(`${response.status()} ${response.url()}`); });
  await page.goto(`${server.resolvedUrls.local[0]}__skills_test.html`);
  const main = page.locator('#main textarea');
  const side = page.locator('#side textarea');
  const options = page.getByRole('option');
  const sends = () => page.evaluate(() => window.skillTest.calls.filter(call => call.method === 'turn/start'));

  await main.fill('/');
  await options.first().waitFor();
  assert.equal(await options.count(), 2, 'invalid skills must be excluded');
  await main.press('ArrowDown');
  await main.press('Enter');
  assert.equal(await main.inputValue(), '/tools:test ');
  assert.equal((await sends()).length, 0, 'selecting must not send');
  assert.equal(await main.evaluate(el => el.selectionStart), 12);
  await main.press('End');
  await main.press('t');
  await main.press('Enter');
  assert.equal((await sends())[0].params.text, '/tools:test t');
  console.log('ok keyboard selection inserts and waits, then sends the command');

  await main.fill('/inspect');
  await options.first().waitFor();
  assert.equal(await options.count(), 1, 'description search');
  await main.press('Tab');
  assert.equal(await main.inputValue(), '/tools:review ');
  await main.fill('/');
  await options.first().waitFor();
  await main.press('Escape');
  assert.equal(await options.count(), 0);
  assert.equal(await main.inputValue(), '/');
  await main.fill('/tmp/file');
  assert.equal(await options.count(), 0, 'paths must not open the picker');
  console.log('ok filtering, Tab, Escape, and ordinary paths');

  await main.fill('/rev\ncheck my changes');
  await main.evaluate(el => { el.setSelectionRange(4, 4); el.dispatchEvent(new Event('select', { bubbles: true })); });
  await options.first().waitFor();
  await options.first().click();
  assert.equal(await main.inputValue(), '/tools:review \ncheck my changes');
  assert.equal(await main.evaluate(el => el === document.activeElement), true);
  console.log('ok mouse selection preserves draft text and focus');

  await side.fill('/');
  await options.first().waitFor();
  assert.equal(await page.evaluate(() => window.skillTest.calls.filter(c => c.method === 'skill/list').at(-1).params.projectId), 'side-project');
  await options.first().click();
  assert.equal(await side.inputValue(), '/tools:review ');
  console.log('ok side chat uses its own project');

  await page.evaluate(() => { window.skillTest.mode = 'empty'; });
  await main.fill('/');
  await page.getByText('No skills available.', { exact: false }).waitFor();
  await main.fill('/nothing');
  await page.evaluate(() => { window.skillTest.mode = 'error'; });
  await main.fill('');
  await main.fill('/');
  await page.getByRole('alert').waitFor();
  await main.press('Shift+Tab');
  assert.equal(await page.getByRole('button', { name: 'Retry', exact: true }).evaluate(el => el === document.activeElement), true, 'Retry must be reachable by keyboard');
  await page.evaluate(() => { window.skillTest.mode = 'normal'; window.skillTest.delay = 200; });
  await page.getByRole('button', { name: 'Retry', exact: true }).press('Enter');
  await page.getByText('Loading skills…').waitFor();
  await options.first().waitFor();
  await main.fill('/missing');
  await page.getByText('No matching skills.', { exact: true }).waitFor();
  console.log('ok empty, loading, retry, and no-match states');

  assert.deepEqual(errors, [], 'browser errors');
  if (process.env.SCREENSHOT_PATH) {
    await main.fill('/');
    await options.first().waitFor();
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({ path: process.env.SCREENSHOT_PATH });
  }
} finally {
  await browser?.close();
  await server.close();
}
