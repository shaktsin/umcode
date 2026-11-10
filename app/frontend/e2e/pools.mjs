import { chromium } from 'playwright-core';
const b = await chromium.launch({ executablePath: '/opt/pw-browsers/chromium-1194/chrome-linux/chrome' });
const scheme = process.env.SCHEME === 'light' ? 'light' : 'dark';
const p = await b.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: scheme });
const errors = [];
p.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
p.on('pageerror', (e) => errors.push(String(e)));
let failed = 0;
const step = async (name, fn) => {
  try { await fn(); console.log('ok  ', name); }
  catch (e) { failed++; console.log('FAIL', name, e.message.split('\n')[0]); await p.screenshot({ path: `${scheme}-poolfail-${name.replace(/\W+/g, '_')}.png` }); }
};

await p.goto('http://127.0.0.1:5173/');
await p.getByRole('button', { name: /demo/ }).first().waitFor({ timeout: 10000 });

const openModels = async () => {
  await p.locator('aside').getByRole('button', { name: 'Settings' }).click();
  await p.getByRole('button', { name: 'Models', exact: true }).click();
};

await step('wake anything resting from an earlier run', async () => {
  await openModels();
  for (let i = 0; i < 8 && (await p.getByRole('button', { name: 'Try it now' }).count()); i++) {
    await p.getByRole('button', { name: 'Try it now' }).first().click();
    await p.waitForTimeout(400);
  }
});

await step('add a second configured model', async () => {
  // Start clean so reruns don't pile up rows.
  for (let i = 0; i < 6 && (await p.getByRole('button', { name: 'Remove pool' }).count()); i++) {
    await p.getByRole('button', { name: 'Remove pool' }).first().click();
  }
  for (let i = 0; i < 6 && (await p.getByRole('button', { name: 'Remove model' }).count()) > 1; i++) {
    await p.getByRole('button', { name: 'Remove model' }).last().click();
    await p.waitForTimeout(150);
  }
  if (await p.getByRole('button', { name: /Save changes/ }).isEnabled()) {
    await p.getByRole('button', { name: /Save changes/ }).click();
    await p.waitForTimeout(600);
  }
  const add = p.locator('div.card', { hasText: 'Add model' }).last();
  if (await p.getByRole('row', { name: /local-qwen/ }).count()) return;
  const existing = await p.locator('input.font-mono').count();
  await add.getByPlaceholder('Daily model').fill('Backup qwen');
  await add.getByRole('combobox').selectOption('openai_compatible');
  await add.getByPlaceholder('Provider model ID').fill('local-qwen');
  await add.getByRole('button', { name: 'Add' }).click();
  await p.waitForTimeout(300);
  if ((await p.locator('input.font-mono').count()) <= existing) throw new Error('model row was not added');
});
await p.screenshot({ path: scheme + '-pool-01-models.png' });

await step('create a pool with both models and make it the default', async () => {
  await p.getByPlaceholder('Coding pool').fill('Everything');
  await p.getByRole('button', { name: 'Add pool' }).click();
  await p.waitForTimeout(300);
  const poolCard = p.locator('div.card', { hasText: 'Pool name' }).first();
  const addToPool = poolCard.getByRole('combobox').last();
  const qwen = (await addToPool.locator('option').allTextContents()).find((t) => /local-qwen/.test(t));
  if (!qwen) throw new Error('the new model is not offered to the pool');
  await addToPool.selectOption({ label: qwen });
  await p.waitForTimeout(200);
  await p.locator('select').filter({ hasText: 'No default pool' }).selectOption({ label: 'Everything' });
  await p.getByRole('button', { name: /Save changes/ }).click();
  await p.getByText('Models and pools saved.').first().waitFor({ timeout: 5000 });
});
await p.screenshot({ path: scheme + '-pool-02-pools.png' });

await step('the composer offers the pool and routes through it', async () => {
  await p.locator('aside nav').getByRole('button', { name: 'Chat' }).click();
  await p.getByPlaceholder(/Ask about demo/).waitFor({ timeout: 5000 });
  const picker = p.locator('div[aria-label="Message composer"] [data-model-picker] button').first();
  await picker.click();
  const menu = p.getByRole('dialog', { name: 'Choose models' });
  await menu.waitFor({ timeout: 5000 });
  const pool = menu.getByRole('button', { name: /Everything/ });
  if (!(await pool.count())) throw new Error('pool missing from the picker');
  console.log('     picker:', (await menu.textContent())?.replace(/\s+/g, ' ').trim());
  await pool.click();
  await menu.waitFor({ state: 'detached', timeout: 5000 });
  const label = (await picker.textContent())?.trim();
  console.log('     selected:', label);
  if (!/Everything/.test(label ?? '')) throw new Error('pool not selected in the picker');
});

await step('a rate limit walks down the pool', async () => {
  await p.getByPlaceholder(/Ask about demo/).fill('please drop down a model');
  await p.keyboard.press('Enter');
  await p.getByText(/Switched to local-qwen after a rate limit on local-llama/).first().waitFor({ timeout: 20000 });
  await p.getByText(/switched from local-llama after a rate limit/).first().waitFor({ timeout: 10000 });
});
await p.screenshot({ path: scheme + '-pool-03-switch.png' });

console.log('console errors:', errors.length ? errors.slice(0, 5) : 'none');
console.log(failed ? `${failed} step(s) failed` : 'all steps passed');
await b.close();
process.exit(failed ? 1 : 0);
