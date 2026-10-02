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
  catch (e) { failed++; console.log('FAIL', name, e.message.split('\n')[0]); await p.screenshot({ path: `${scheme}-fail-${name.replace(/\W+/g, '_')}.png` }); }
};

await p.goto('http://127.0.0.1:5173/');
await step('connects and opens the project', async () => {
  await p.getByRole('button', { name: /demo/ }).first().waitFor({ timeout: 10000 });
});
await p.screenshot({ path: scheme + '-01-empty.png' });

await step('edit lands as a fileChange with a diff', async () => {
  await p.getByPlaceholder(/Ask about demo/).fill('please list the files and add a line');
  await p.keyboard.press('Enter');
  await p.getByText('notes.txt').first().waitFor({ timeout: 15000 });
  await p.getByRole('button', { name: 'diff' }).first().click();
  await p.getByText('+three').first().waitFor({ timeout: 5000 });
});
await p.screenshot({ path: scheme + '-02-filechange.png' });

await step('the diff stays open in the inspector', async () => {
  await p.getByText('+three').first().waitFor({ timeout: 5000 });
  await p.screenshot({ path: scheme + '-03-inspector.png' });
});

await step('side chat opens in its own column', async () => {
  await p.getByRole('button', { name: 'Ask about this' }).first().click();
  await p.getByRole('button', { name: /Promote/ }).waitFor({ timeout: 10000 });
  const side = p.getByPlaceholder('Ask about this…');
  await side.fill('why?');
  await side.press('Enter');
  await p.getByText('Hello from the fake model.').first().waitFor({ timeout: 10000 });
});
await p.screenshot({ path: scheme + '-04-sidechat.png' });

await step('approval offers to remember the decision', async () => {
  await p.getByPlaceholder(/Ask about demo/).fill('please run a command');
  await p.keyboard.press('Enter');
  const approve = p.getByRole('button', { name: 'Approve', exact: true }).first();
  await approve.waitFor({ timeout: 15000 });
  await p.getByText('always in demo').first().waitFor({ timeout: 5000 });
  await p.screenshot({ path: scheme + '-05-approval.png' });
  await approve.click();
  await p.getByText('The command printed approved-run.').waitFor({ timeout: 15000 });
});

await step('project settings shows instructions and changes', async () => {
  await p.locator('aside nav').getByRole('button', { name: 'Project' }).click();
  await p.waitForTimeout(300);
  await p.getByText('Project instructions').waitFor({ timeout: 5000 });
  const instructions = await p.locator('textarea').first().inputValue();
  if (!instructions.includes('Mention the branch')) throw new Error('UMCODE.md not loaded: ' + instructions);
  await p.getByText('Changes the agent has made').waitFor({ timeout: 5000 });
  await p.screenshot({ path: scheme + '-06-project.png' });
});

await step('undo turn puts the file back', async () => {
  await p.locator('aside nav').getByRole('button', { name: 'Chat' }).click();
  await p.waitForTimeout(400);
  await p.getByText('undo this turn').first().click({ force: true });
  await p.getByRole('button', { name: 'Undo changes' }).click();
  await p.getByText(/Put back notes.txt/).waitFor({ timeout: 10000 });
});

console.log('console errors:', errors.length ? errors.slice(0, 5) : 'none');
console.log(failed ? `${failed} step(s) failed` : 'all steps passed');
await b.close();
