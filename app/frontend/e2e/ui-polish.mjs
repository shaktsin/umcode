import assert from 'node:assert/strict';
import { startUiFixtureServer } from './ui-fixtures.mjs';
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright-core');
const server = await startUiFixtureServer();
const browser = await chromium.launch({ executablePath:process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',headless:true });
const fullName='A deliberately long installed component name which must remain discoverable';
const fullError='The integration could not start because its configured executable is unavailable.';
const description='A complete skill description explaining the workflow, its inputs, expected output, and when to select it. '.repeat(5);
const near=(actual,expected,label)=>assert.ok(Math.abs(actual-expected)<=1,`${label}: ${actual} expected ${expected}`);
try {
  for (const theme of ['light','dark']) for (const [width,height] of [[760,520],[960,640],[1180,780],[1440,900],[1920,1080],[1180,520]]) {
    const page=await browser.newPage({viewport:{width,height},colorScheme:theme});page.setDefaultTimeout(5000);
    const errors=[];page.on('pageerror',error=>errors.push(error.message));
    await page.goto(server.url+'?scenario=app');await page.waitForFunction(()=>window.uiTest);
    await page.evaluate(async ({fullName,fullError,description})=>{
      const {app}=await import('/src/lib/stores/app.svelte.ts');const original=app.call;
      app.call=async(method,params)=>{
        if(method==='skill/list')return {skills:[{name:'review:complete',description}]};
        if(method==='plugin/list')return {plugins:[{id:'source/review',name:'Review plugin',version:'1.0',format:'codex',source:'/plugins/review',mode:'managed',enabled:false,settings:{},schema:[],diagnostics:[],executables:[],health:'partially_unavailable',hookFailures:[],components:[{kind:'mcp',name:fullName,path:'/plugins/review/component',required:false,supported:true,health:'failed',error:fullError}]}]};
        return original(method,params);
      };
      app.view='extensions';
    },{fullName,fullError,description});
    const inspect=page.getByRole('button',{name:'Inspect',exact:true});await inspect.waitFor();
    near((await inspect.boundingBox()).height,32,'Inspect height');near((await page.locator('#plugin-mode').boundingBox()).height,32,'install mode height');
    await page.getByRole('button',{name:'Details',exact:true}).click();
    const component=page.getByText(fullName,{exact:true});await component.waitFor();
    assert.ok((await component.getAttribute('title')).includes(fullName),'inventory tooltip preserves full name');
    assert.ok((await component.getAttribute('title')).includes('/plugins/review/component'),'inventory tooltip preserves path');
    const failed=page.getByText('failed',{exact:true});assert.equal(await failed.getAttribute('title'),fullError);assert.equal(await failed.getAttribute('aria-label'),'failed: '+fullError);
    await page.evaluate(async()=>{const {app}=await import('/src/lib/stores/app.svelte.ts');app.view='chat';});
    const input=page.getByRole('textbox',{name:'Message',exact:true}).first();await input.fill('/review:');
    const option=page.getByRole('option').first();await option.waitFor();
    assert.equal(await page.locator('#'+await option.getAttribute('aria-describedby')).textContent(),description);
    assert.equal(await option.locator('span').last().getAttribute('title'),description);
    await input.press('Escape');await input.fill('');
    await page.evaluate(()=>window.uiTest.panels());
    const measureClose=async(panel,label)=>{
      const region=page.locator(`[data-panel="${panel}"]`);
      if(!await region.isVisible())await page.getByRole('button',{name:label,exact:true}).click();
      const close=region.getByRole('button',{name:panel==='side'?'Close side chat':/^Close (tab|Test output)$/}).first();
      const box=await close.boundingBox();near(box.width,28,panel+' close width');near(box.height,28,panel+' close height');
      const glyph=await close.locator('svg').boundingBox();near(glyph.width,16,panel+' close icon');near(glyph.height,16,panel+' close icon');
      await close.focus();assert.equal(await close.evaluate(el=>el===document.activeElement),true,'close keyboard focus');
      if(await page.getByRole('dialog',{name:panel==='side'?'Side chats panel':'Inspector panel'}).count())await page.keyboard.press('Escape');
    };
    await page.getByRole('dialog',{name:'Inspector panel'}).count() && await page.keyboard.press('Escape');
    await measureClose('side','Show side chats');
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth&&document.documentElement.scrollHeight<=innerHeight),'no document overflow');
    assert.deepEqual(errors,[]);await page.close();
  }
  console.log('PASS 12 compact UI polish viewport/theme cases');
} finally {await browser.close();await server.close();}
