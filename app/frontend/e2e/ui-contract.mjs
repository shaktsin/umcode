import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import { startUiFixtureServer } from './ui-fixtures.mjs';
const {chromium}=await import(process.env.PLAYWRIGHT_MODULE||'playwright-core');
const selected=process.argv[process.argv.indexOf('--case')+1]||'all';
const artifacts=process.env.UI_ARTIFACT_DIR||'/tmp/umcode-ui-contract';
await mkdir(artifacts,{recursive:true});
const server=await startUiFixtureServer();
const browser=await chromium.launch({executablePath:process.env.CHROME_PATH||'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',headless:true});
const report=[];
const near=(actual,expected,label)=>assert.ok(Math.abs(actual-expected)<=1,label+': '+actual+' expected '+expected);
try {
  for (const theme of ['light','dark']) for (const [width,height] of [[760,520],[960,640],[1180,780],[1440,900],[1920,1080],[1180,520]]) {
    const page=await browser.newPage({viewport:{width,height},colorScheme:theme});
    page.setDefaultTimeout(5000);
    const errors=[];
    page.on('pageerror',e=>errors.push(e.message));
    page.on('console',m=>{if(m.type()==='error')errors.push(m.text());});
    page.on('response',r=>{if(r.status()>=400)errors.push(r.status()+' '+r.url());});
    const run=async(name,fn)=>{if(selected!=='all'&&selected!==name)return;await page.goto(server.url+'?scenario='+(name==='controls'?'controls':'app'));await fn();assert.deepEqual(errors,[]);await page.screenshot({path:artifacts+'/'+name+'-'+theme+'-'+width+'x'+height+'.png'});report.push({name,theme,width,height,passed:true});};
    await run('controls',async()=>{
      for(const id of ['standard','field','select','icon'])near((await page.locator('#'+id).boundingBox()).height,32,id);
      for(const id of ['compact','compact-icon'])near((await page.locator('#'+id).boundingBox()).height,28,id);
      near((await page.locator('#icon').boundingBox()).width,32,'icon target');
      near((await page.locator('#icon svg').boundingBox()).width,16,'icon glyph');
      const a=await page.locator('#standard').boundingBox(),b=await page.locator('#field').boundingBox();near(b.x-a.x-a.width,8,'gap');near(a.y+a.height/2,b.y+b.height/2,'alignment');
      await page.locator('#help').evaluate(el=>el.textContent='Long validation feedback '.repeat(10));near((await page.locator('#field').boundingBox()).height,32,'help height');
    });
    await run('popups',async()=>{
      const trigger=page.getByTitle('Provider and model');await trigger.click();
      const popup=page.locator('[data-popup-surface="menu"]');await popup.waitFor();
      const inside=async()=>{const r=await popup.boundingBox();assert.ok(r.x>=7&&r.y>=7&&r.x+r.width<=page.viewportSize().width-7&&r.y+r.height<=page.viewportSize().height-7,'popup bounds');};
      await inside();await page.setViewportSize({width:760,height:520});await page.waitForTimeout(80);await inside();
      await page.keyboard.press('Escape');assert.equal(await popup.count(),0);assert.equal(await trigger.evaluate(el=>el===document.activeElement),true);
      const input=page.getByRole('textbox',{name:'Message',exact:true}).first();await input.fill('/superpowers:');await page.locator('[role=listbox] [role=option]').first().waitFor();
      await input.press('ArrowDown');const active=await input.getAttribute('aria-activedescendant');await page.setViewportSize({width,height});await page.waitForTimeout(80);assert.equal(await input.getAttribute('aria-activedescendant'),active);
      await input.press('Enter');assert.equal(await input.inputValue(),'/superpowers:skill-1 ');
    });
    await run('shell',async()=>{
      await page.evaluate(()=>window.uiTest.panels());
      const main=page.locator('[data-main-region]');await main.waitFor();
      const fit=async()=>assert.ok((await main.boundingBox()).width>=439,'main region width');await fit();
      const saved=await page.evaluate(()=>localStorage.getItem('umcode.columns'));
      await page.setViewportSize({width:760,height:520});await page.waitForTimeout(80);await fit();
      await page.getByRole('button',{name:'Show inspector',exact:true}).click();
      await page.getByRole('dialog',{name:'Inspector panel'}).waitFor();await page.keyboard.press('Escape');assert.equal(await page.getByRole('dialog',{name:'Inspector panel'}).count(),0);
      assert.equal(await page.evaluate(()=>localStorage.getItem('umcode.columns')),saved);
      assert.equal(await page.evaluate(()=>document.querySelectorAll('[data-panel="side"]').length),1);
      await page.setViewportSize({width:1920,height:1080});await page.waitForTimeout(80);await fit();
      assert.equal(await page.locator('[data-panel="side"]').count(),1);assert.equal(await page.locator('[data-panel="inspector"]').count(),1);
      await page.setViewportSize({width,height});
    });
    await run('composer',async()=>{
      const composer=page.locator('[data-composer]').first();await composer.waitFor();
      assert.equal(await composer.getByText('Workspace',{exact:true}).count(),0);
      assert.equal(await composer.getByText('Claude Sonnet 5',{exact:true}).count(),0);
      const input=page.getByRole('textbox',{name:'Message',exact:true}).first();const draft='Long draft line\n'.repeat(80);await input.fill(draft);
      const send=composer.getByRole('button',{name:'Send',exact:true});const r=await send.boundingBox();assert.ok(r.x>=0&&r.x+r.width<=width&&r.y+r.height<=height,'send bounds');
      assert.equal(await input.inputValue(),draft);
      await input.evaluate(el=>{el.scrollTop=el.scrollHeight;});assert.ok(await input.evaluate(el=>el.scrollHeight>el.clientHeight));
    });
    await page.close();
  }
  await writeFile(artifacts+'/report.json',JSON.stringify(report,null,2));
  console.log('PASS '+report.length+' UI matrix cases; artifacts '+artifacts);
} finally {await browser.close();await server.close();}
