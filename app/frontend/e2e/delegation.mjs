// Real App, Composer and Agents history. Only RPC responses,
// notification delivery and connection changes are deterministic fixtures.
import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import { startUiFixtureServer } from './ui-fixtures.mjs';
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright-core');
const artifacts = process.env.UI_ARTIFACT_DIR || '/tmp/umcode-delegation';
await mkdir(artifacts, { recursive: true });
const setup = `
  const originalCall = app.call;
  state.snapshots = []; state.listError = false;
  const thread = {id:'new-main', title:'Delegation acceptance', projectId:'demo', settings:{}, updatedAt:'2026-10-15T12:00:00Z'};
  const usage = {inputTokens:110, outputTokens:30, cachedInputTokens:0, reasoningTokens:0, costUsd:0.01, requests:10};
  state.notify = (method, params) => app.rpc.handle(JSON.stringify({jsonrpc:'2.0',method,params}));
  app.credentials = [{id:'fixture-key', provider:'claude', enabled:true}];
  app.call = async (method, params) => {
    if (!['delegation/list','delegation/enable','delegation/cancel','thread/read','thread/list','project/list','credential/list','turn/start','turn/interrupt'].includes(method)) return originalCall(method,params);
    state.calls.push({method,params});
    if(method==='thread/read') return {thread, turns:[], items:[]};
    if(method==='thread/list') return {threads:[thread]};
    if(method==='project/list') return {projects:projects.list};
    if(method==='credential/list') return {credentials:app.credentials};
    if(method==='delegation/list') {if(state.listError) throw new Error('Hydration failed; retained outcomes remain available');return {tasks:structuredClone(state.snapshots)};}
    if(method==='delegation/enable') return {activationToken:'one-use-token',expiresAt:'2099-01-01T00:00:00Z'};
    if(method==='turn/interrupt') {state.notify('turn/completed',{turn:{...state.runningTurn,status:'interrupted'}});return {};}
    if(method==='delegation/cancel') {
      const s=state.snapshots[0];s.task.state='cancelled';s.task.updatedAt='2026-10-15T12:00:05Z';
      state.notify('delegation/updated',{threadId:thread.id,taskId:s.task.id,task:s.task});
      return {task:s.task};
    }
    if(method==='turn/start') {chat.main.sending=false;return {};}
    return {};
  };
  state.startAgents = async () => {
    const task={id:'task-1',threadId:thread.id,rootTurnId:'turn-1',projectId:'demo',activation:'explicit_user',state:'running',baseRevision:'abc1234',baseRoot:'',parentRoot:'',createdAt:'2026-10-15T12:00:00Z',updatedAt:'2026-10-15T12:00:01Z'};
    const child={id:'worker-1',taskId:task.id,model:{model:'deterministic-worker'},assignment:{prompt:'Earlier bounded assignment'},state:'completed',result:{summary:'Retained implementation result'},usage,version:2,createdAt:task.createdAt,updatedAt:task.updatedAt};
    state.snapshots=[{task,children:[child],agents:[],usage,budgetAvailable:true}];
    state.notify('delegation/updated',{threadId:thread.id,taskId:task.id,task});
  };
  state.reconnect = () => {app.rpc.setState('closed','fixture connection lost');app.rpc.setState('open');};
  state.parentRunning = () => {
    const s=state.snapshots[0];s.task.state='running';s.task.updatedAt='2026-10-15T12:00:05.5Z';
    state.notify('delegation/updated',{threadId:thread.id,taskId:s.task.id,task:s.task});
    state.runningTurn={id:'parent-live',threadId:thread.id,status:'running',selection:{},resolved:{},usage,startedAt:s.task.updatedAt};
    state.notify('turn/started',{turn:state.runningTurn});
  };
  state.interrupt = () => {
    const s=state.snapshots[0];s.task.state='interrupted';s.task.updatedAt='2026-10-15T12:00:06Z';
  };
`;
const server = await startUiFixtureServer({ setup });
let browser;
const report = [];
try {
  browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
  for (const theme of ['light', 'dark']) for (const width of [320, 720, 1200]) {
    const page = await browser.newPage({ viewport: { width, height: 900 }, colorScheme: theme });
    page.setDefaultTimeout(7000);
    const errors = [];
    page.on('pageerror', e => errors.push(e.message));
    page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
    page.on('requestfailed', r => errors.push('request failed: ' + r.url() + ' ' + r.failure()?.errorText));
    page.on('response', r => { if (r.status() >= 400) errors.push(r.status() + ' ' + r.url()); });
    const shot = name => page.screenshot({ path: `${artifacts}/${name}-${theme}-${width}.png` });
    try {
      await page.goto(server.url + '?scenario=delegation');
      const more = page.getByRole('button', { name: 'More options', exact: true });
      await more.focus(); await page.keyboard.press('Enter');
      await page.getByRole('menuitem', { name: /Agents for next message/ }).click();
      await page.getByTitle('Agents are on for the next message', { exact: true }).waitFor();
      assert.equal(await page.evaluate(() => window.uiTest.calls.filter(c => c.method === 'thread/start').length), 1);
      assert.equal(await page.evaluate(() => window.uiTest.calls.filter(c => c.method === 'turn/start').length), 0, 'enable never submits');
      const input = page.getByRole('textbox', { name: 'Message', exact: true }).first();
      await input.fill('Implement the assignment');
      await page.getByRole('button', { name: 'Send', exact: true }).click();
      assert.equal(await page.evaluate(() => window.uiTest.calls.find(c => c.method === 'turn/start').params.delegationActivationToken), 'one-use-token');
      await page.evaluate(() => window.uiTest.startAgents());
      const agents = page.locator('[data-agents]');
      await agents.waitFor(); assert.equal(await agents.getAttribute('open'), null, 'collapsed by default');
      const summary = agents.locator(':scope > summary'); await summary.focus(); await page.keyboard.press('Enter');
      await agents.getByText('Earlier bounded assignment', { exact: true }).waitFor();
      await agents.getByText('Earlier agent run', { exact: true }).waitFor();
      await agents.getByText('Retained implementation result', { exact: true }).waitFor();
      await shot('expanded');
      assert.equal(await agents.getByRole('button', { name: /Cancel Earlier bounded assignment/ }).count(), 0, 'historical run is read-only');
      await page.getByRole('button', { name: 'Cancel task', exact: true }).focus(); await page.keyboard.press('Enter');
      await page.waitForFunction(() => window.uiTest.calls.some(c => c.method === 'delegation/cancel'));
      assert.equal(await page.getByRole('button', { name: 'Cancel task', exact: true }).count(), 0);
      await page.evaluate(() => window.uiTest.parentRunning());
      const stop = page.getByRole('button', { name: 'Stop', exact: true });
      await stop.focus(); await page.keyboard.press('Enter');
      await page.waitForFunction(() => window.uiTest.calls.some(c => c.method === 'turn/interrupt'));
      const methods = await page.evaluate(() => window.uiTest.calls.map(c => c.method));
      assert.equal(methods.filter(m => m === 'delegation/cancel').length, 2, 'parent Stop cancels delegated task');
      assert.ok(methods.lastIndexOf('delegation/cancel') < methods.lastIndexOf('turn/interrupt'), 'task cancellation precedes parent interruption');
      await page.evaluate(() => { window.uiTest.listError = true; window.uiTest.reconnect(); });
      await page.getByRole('alert').filter({ hasText: 'Hydration failed; retained outcomes remain available' }).waitFor();
      await page.evaluate(() => { window.uiTest.listError = false; window.uiTest.interrupt(); window.uiTest.reconnect(); });
      await agents.getByText('Earlier bounded assignment', { exact: true }).waitFor();
      const bounds = await agents.boundingBox();
      assert.ok(bounds.x >= 0 && bounds.x + bounds.width <= width, `Agents bounds ${JSON.stringify(bounds)} at ${width}`);
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'no horizontal document overflow');
      assert.deepEqual(errors, []);
      await shot('reconnected');
      report.push({ width, theme, passed: true, bounds, errors });
    } catch (e) {
      await shot('failure-debug'); report.push({ width, theme, passed: false, error: e.message, errors }); throw e;
    } finally { await page.close(); }
  }
  console.log(`PASS ${report.length} delegation size/theme cases; artifacts ${artifacts}`);
} finally {
  await writeFile(artifacts + '/report.json', JSON.stringify(report, null, 2));
  if (browser) await browser.close();
  await server.close();
}
