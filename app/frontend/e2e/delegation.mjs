// Real App, Composer, Agents, ApprovalCard and inspector. Only RPC responses,
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
    if (!['delegation/list','delegation/enable','delegation/cancel','delegation/file','thread/read','thread/list','project/list','credential/list','turn/start','turn/interrupt','approval/respond'].includes(method)) return originalCall(method,params);
    state.calls.push({method,params});
    if(method==='thread/read') return {thread, turns:[], items:[]};
    if(method==='thread/list') return {threads:[thread]};
    if(method==='project/list') return {projects:projects.list};
    if(method==='credential/list') return {credentials:app.credentials};
    if(method==='delegation/list') {if(state.listError) throw new Error('Hydration failed; retained outcomes remain available');return {tasks:structuredClone(state.snapshots)};}
    if(method==='delegation/enable') return {activationToken:'one-use-token',expiresAt:'2099-01-01T00:00:00Z'};
    if(method==='delegation/file') return {path:params.path,content:'export const retained = 2;',bytes:26};
    if(method==='approval/respond') return {};
    if(method==='turn/interrupt') {state.notify('turn/completed',{turn:{...state.runningTurn,status:'interrupted'}});return {};}
    if(method==='delegation/cancel') {
      const s=state.snapshots[0];s.task.state='cancelled';s.task.updatedAt='2026-10-15T12:00:05Z';
      s.children=s.children.map(c=>({...c,state:'cancelled',version:c.version+1}));
      state.notify('delegation/updated',{threadId:thread.id,taskId:s.task.id,task:s.task});
      return {task:s.task,children:s.children};
    }
    if(method==='turn/start') {chat.main.sending=false;return {};}
    return {};
  };
  state.startAgents = async () => {
    const task={id:'task-1',threadId:thread.id,rootTurnId:'turn-1',projectId:'demo',activation:'explicit_user',state:'running',baseRevision:'abc1234',baseRoot:'',parentRoot:'',createdAt:'2026-10-15T12:00:00Z',updatedAt:'2026-10-15T12:00:01Z'};
    const child={id:'worker-1',taskId:task.id,role:'implementer',model:{model:'deterministic-worker'},assignment:{prompt:'Implement the bounded acceptance assignment',acceptanceChecks:['Exact revision passes checks']},state:'running',revision:'abc123456789',result:{summary:'Retained implementation result',changedPaths:['src/result.ts'],findings:[{path:'src/result.ts',line:1,severity:'important',message:'Inspect this exact snapshot'}],checks:[{command:'worker check',status:'passed',summary:'Worker evidence only'}]},usage,correctionRounds:1,version:2,createdAt:task.createdAt,updatedAt:task.updatedAt};
    state.snapshots=[{task,children:[child],usage,budgetAvailable:true,workflow:{taskId:task.id,childId:child.id,revision:child.revision,checks:[{label:'Lead executable check',command:'npm test',status:'passed'},{label:'Required visual QA',command:'Interactive visual QA',status:'blocked',reason:'Trusted interactive visual receipt unavailable'}]}}];
    state.notify('delegation/updated',{threadId:thread.id,taskId:task.id,task,child,sequence:2});
  };
  state.approval = () => state.notify('approval/request',{approval:{id:'approval-1',threadId:thread.id,projectId:'demo',agentTaskId:'task-1',agentChildId:'worker-1',tool:'shell.run',args:{command:'npm test'},actionSummary:'Run assignment checks',reason:'Implementer requests command execution',expiresAt:'2099-01-01T00:00:00Z',status:'pending'}});
  state.failWorker = () => {
    const s=state.snapshots[0];s.children[0]={...s.children[0],state:'failed',version:3,result:{...s.children[0].result,blocker:'Task output budget exhausted'}};
    s.childErrors={'worker-1':'Outcome saving failed; artifacts retained'};
    state.notify('delegation/updated',{threadId:thread.id,taskId:s.task.id,child:s.children[0],error:s.childErrors['worker-1'],sequence:3});
  };
  state.reconnect = () => {app.rpc.setState('closed','fixture connection lost');app.rpc.setState('open');};
  state.parentRunning = () => {
    const s=state.snapshots[0];s.task.state='running';s.task.updatedAt='2026-10-15T12:00:05.5Z';
    state.notify('delegation/updated',{threadId:thread.id,taskId:s.task.id,task:s.task});
    state.runningTurn={id:'parent-live',threadId:thread.id,status:'running',selection:{},resolved:{},usage,startedAt:s.task.updatedAt};
    state.notify('turn/started',{turn:state.runningTurn});
  };
  state.interrupt = () => {
    const s=state.snapshots[0];s.task.state='interrupted';s.task.updatedAt='2026-10-15T12:00:06Z';s.children[0]={...s.children[0],state:'interrupted',version:5};
    delete s.childErrors;
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
      const enable = page.getByRole('button', { name: 'Enable agents for next message', exact: true });
      await enable.focus(); await page.keyboard.press('Enter');
      await page.getByText('Agents enabled for the next message', { exact: true }).waitFor();
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
      await agents.getByText('Implement the bounded acceptance assignment', { exact: true }).waitFor();
      assert.equal(await agents.locator('article').count(), 1);
      await agents.getByText('Result and checks', { exact: false }).click();
      await agents.getByText('Retained implementation result', { exact: true }).waitFor();
      await agents.getByText('Lead verification', { exact: true }).click();
      await agents.getByText('Trusted interactive visual receipt unavailable', { exact: true }).waitFor();
      await page.evaluate(() => window.uiTest.approval());
      const approval = agents.locator('#approval-approval-1');
      await approval.getByText('Run assignment checks', { exact: true }).waitFor();
      assert.equal(await approval.getByText('implementer · Implement the bounded acceptance assignment', { exact: true }).count(), 1, 'approval identifies requesting worker');
      for (const name of ['Deny', 'Always allow in this chat', 'Allow once']) {
        const button = approval.getByRole('button', { name, exact: true });
        assert.ok(await button.evaluate(el => el.scrollHeight <= el.clientHeight), name + ' approval text fits its control');
        const b = await button.boundingBox();
        assert.ok(b.x >= 0 && b.x + b.width <= width, name + ' approval control is within viewport');
      }
      await approval.getByRole('button', { name: 'Allow once', exact: true }).focus();
      await shot('approval'); await page.keyboard.press('Enter');
      await page.waitForFunction(() => window.uiTest.calls.some(c => c.method === 'approval/respond'));
      assert.deepEqual(await page.evaluate(() => window.uiTest.calls.find(c => c.method === 'approval/respond').params), { approvalId: 'approval-1', approve: true, remember: false });
      await shot('expanded');
      const file = agents.getByRole('button', { name: 'src/result.ts', exact: true });
      await file.click();
      await page.getByRole('tab', { name: 'result.ts · abc1234', exact: true }).waitFor();
      // At narrow widths the inspector is an overlay; Escape restores Agents.
      if (width < 1200) await page.keyboard.press('Escape');
      await file.click();
      await page.getByRole('tab', { name: 'result.ts · abc1234', exact: true }).waitFor();
      assert.equal(await page.getByRole('tab', { name: 'result.ts · abc1234', exact: true }).count(), 1, 'repeated snapshot link reuses inspector tab');
      if (width < 1200) await page.keyboard.press('Escape');
      await page.evaluate(() => window.uiTest.failWorker());
      await agents.getByText('Task output budget exhausted', { exact: true }).waitFor();
      await agents.getByText('Outcome saving failed; artifacts retained', { exact: true }).waitFor();
      assert.equal(await agents.locator('article').count(), 1, 'event updates existing row');
      await shot('failure');
      await agents.getByRole('button', { name: 'Cancel task', exact: true }).focus(); await page.keyboard.press('Enter');
      await page.waitForFunction(() => window.uiTest.calls.some(c => c.method === 'delegation/cancel'));
      assert.equal(await agents.getByRole('button', { name: 'Cancel task', exact: true }).count(), 0);
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
      await agents.getByText('Interrupted', { exact: true }).waitFor();
      assert.equal(await agents.locator('article').count(), 1, 'reconnect preserves one row');
      assert.equal(await agents.getByText('Outcome saving failed; artifacts retained', { exact: true }).count(), 0, 'durable restart clears stale error');
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
