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
    await page.close();
  }
  await writeFile(artifacts+'/report.json',JSON.stringify(report,null,2));
  console.log('PASS '+report.length+' UI matrix cases; artifacts '+artifacts);
} finally {await browser.close();await server.close();}
