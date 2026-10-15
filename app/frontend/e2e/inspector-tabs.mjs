import assert from 'node:assert/strict';
import { startInspectorFixtureServer } from './inspector-fixtures.mjs';
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright-core');
const server = await startInspectorFixtureServer();
const browser = await chromium.launch({ executablePath:process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless:true });
try {
  for (const theme of ['light','dark']) {
    const page = await browser.newPage({viewport:{width:1000,height:600},colorScheme:theme});
    const errors=[];page.on('pageerror',error=>errors.push(error.message));
    await page.goto(server.url);
    await page.waitForFunction(()=>window.inspectorTest);
    const strip=page.getByRole('tablist',{name:'Inspector tabs'});
    const last=page.getByRole('tab',{name:'Long resource title number 17',exact:true});
    const visible=async(tab)=>{
      await page.waitForFunction(()=>{const strip=document.querySelector('[role=tablist]'),tab=strip?.querySelector('[aria-selected=true]');if(!tab)return false;const s=strip.getBoundingClientRect(),r=tab.parentElement.getBoundingClientRect();return r.left>=s.left-1&&r.right<=s.right+1;});
      assert.equal(await tab.getAttribute('aria-selected'),'true');
    };
    await visible(last);
    assert.equal(await page.getByRole('button',{name:'Scroll tabs left'}).isVisible(),true);
    for(const width of [320,720,280,900,320]) {
      await page.evaluate(width=>window.inspectorTest.width(width),width);await visible(last);
      const bounds=await page.locator('#app').boundingBox();
      const close=await page.getByRole('button',{name:'Close inspector',exact:true}).boundingBox();
      assert.ok(close.x+close.width<=bounds.x+bounds.width+1,'inspector close stays within panel');
      assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'document does not overflow');
    }
    await last.focus();await page.keyboard.press('Home');
    const first=page.getByRole('tab',{name:'Long resource title number 0',exact:true});await visible(first);
    assert.equal(await first.evaluate(el=>el===document.activeElement),true);
    await page.keyboard.press('ArrowRight');await visible(page.getByRole('tab',{name:'Long resource title number 1',exact:true}));
    await page.keyboard.press('End');await visible(last);
    await page.keyboard.press('Tab');
    assert.equal(await page.evaluate(()=>document.activeElement.getAttribute('aria-label')),'Close Long resource title number 17');
    await page.keyboard.press('Enter');
    assert.equal(await last.count(),0);
    await visible(page.getByRole('tab',{name:'Long resource title number 16',exact:true}));
    await page.evaluate(()=>window.inspectorTest.activate('tab-0'));await visible(first);
    await page.getByRole('button',{name:'Scroll tabs right'}).click();
    await page.waitForFunction(()=>document.querySelector('[role=tablist]').scrollLeft>0);
    await page.getByRole('button',{name:'Scroll tabs left'}).click();
    await page.waitForFunction(()=>document.querySelector('[role=tablist]').scrollLeft===0);
    assert.deepEqual(errors,[]);
    await page.close();
  }
  console.log('PASS inspector tab overflow, active reveal, resize, keyboard navigation and close (light/dark)');
} finally {await browser.close();await server.close();}
