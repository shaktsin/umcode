// First run: an engine home with no credentials. Run this against a fresh
// UMCODE_HOME (see README.md); it connects a key through the Providers tiles.
import { chromium } from 'playwright-core';
const b = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/opt/pw-browsers/chromium-1194/chrome-linux/chrome' });
const scheme = process.env.SCHEME === 'light' ? 'light' : 'dark';
const p = await b.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: scheme });
const errors = [];
p.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
p.on('pageerror', (e) => errors.push(String(e)));
let failed = 0;
const step = async (name, fn) => {
  try { await fn(); console.log('ok  ', name); }
  catch (e) { failed++; console.log('FAIL', name, e.message.split('\n')[0]); await p.screenshot({ path: `${scheme}-firstrunfail-${name.replace(/\W+/g, '_')}.png` }); }
};
const nav = (name) => p.locator('aside nav').getByRole('button', { name, exact: true });
const welcome = p.getByText('Welcome to UMCode', { exact: true });
const openAiCompatible = p.getByRole('button', { name: /OpenAI-compatible/ });

await p.goto('http://127.0.0.1:5173/');

await step('a home without credentials lands on Settings with the welcome strip', async () => {
  await welcome.waitFor({ timeout: 15000 });
  await p.getByText('Connect one model provider to start chatting. You can add more later.').waitFor({ timeout: 5000 });
  await p.getByRole('heading', { name: 'Settings', exact: true }).waitFor({ timeout: 5000 });
  await p.getByRole('button', { name: 'Providers', exact: true }).waitFor({ timeout: 5000 });
});

await step('Providers shows a tile for each provider', async () => {
  for (const name of [/^Anthropic/, /^OpenAI(?!-)/, /^Gemini/, /^OpenAI-compatible/]) {
    await p.getByRole('button', { name }).first().waitFor({ timeout: 5000 });
  }
  if (await p.getByText('Connected', { exact: true }).count()) throw new Error('a tile claims to be connected before any key exists');
});
await p.screenshot({ path: scheme + '-first-01-providers.png' });

await step('Chat is blocked with a connect prompt and does not bounce back to Settings', async () => {
  await nav('Chat').click();
  const box = p.getByPlaceholder('Connect a provider to chat');
  await box.waitFor({ timeout: 5000 });
  if (!(await p.getByRole('button', { name: 'Send', exact: true }).isDisabled())) throw new Error('send is enabled with no provider');
  await p.waitForTimeout(1500);
  await box.waitFor({ timeout: 2000 });
  if (await p.getByRole('heading', { name: 'Settings', exact: true }).count()) throw new Error('the app returned to Settings by itself');
});
await p.screenshot({ path: scheme + '-first-02-chat-blocked.png' });

await step('connecting an OpenAI-compatible key clears the strip and marks the tile', async () => {
  await p.locator('aside').getByRole('button', { name: 'Settings' }).click();
  await welcome.waitFor({ timeout: 5000 });
  await openAiCompatible.click();
  await p.locator('#k-url').fill('http://127.0.0.1:18997/v1');
  await p.getByRole('button', { name: 'Save and test' }).click();
  await welcome.waitFor({ state: 'detached', timeout: 20000 });
  await openAiCompatible.getByText('Connected', { exact: true }).waitFor({ timeout: 10000 });
});
await p.screenshot({ path: scheme + '-first-03-connected.png' });

await step('the composer accepts text once a provider is connected', async () => {
  await nav('Chat').click();
  const input = p.getByRole('textbox', { name: 'Message', exact: true }).first();
  await input.waitFor({ timeout: 5000 });
  if (await p.getByPlaceholder('Connect a provider to chat').count()) throw new Error('still showing the connect prompt');
  await input.fill('hello');
  if (await input.inputValue() !== 'hello') throw new Error('composer did not take text');
  if (!(await p.getByRole('button', { name: 'Send', exact: true }).isEnabled())) throw new Error('send stayed disabled');
});

console.log('console errors:', errors.length ? errors.slice(0, 5) : 'none');
console.log(failed ? `${failed} step(s) failed` : 'all steps passed');
await b.close();
process.exit(failed ? 1 : 0);
