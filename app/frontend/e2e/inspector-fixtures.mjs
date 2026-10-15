import { createServer } from 'vite';
import { fileURLToPath } from 'node:url';

export async function startInspectorFixtureServer() {
  const root = fileURLToPath(new URL('..', import.meta.url));
  const html = `<!doctype html><html><head><meta charset="utf-8"><link rel="icon" href="data:,"></head><body><div id="app" style="width:320px;height:480px"></div>
  <script type="module">
  import '/src/app.css';
  import { mount } from 'svelte';
  import Inspector from '/src/lib/components/Inspector.svelte';
  import { inspector } from '/src/lib/stores/inspector.svelte';
  window.inspectorTest = {
    populate() { inspector.closeAll(); for (let i=0;i<18;i++) inspector.openOutput('Long resource title number '+i, 'Output '+i, 'tab-'+i); },
    activate(id) { inspector.activeId=id; },
    width(value) { document.getElementById('app').style.width=value+'px'; },
    active() { return inspector.activeId; },
  };
  mount(Inspector,{target:document.getElementById('app')});
  window.inspectorTest.populate();
  </script></body></html>`;
  const server = await createServer({ root, server: { host:'127.0.0.1', port:0 }, plugins:[{name:'inspector-fixtures',configureServer(server){server.middlewares.use('/__inspector.html',async(_req,res)=>{res.setHeader('Content-Type','text/html');res.end(await server.transformIndexHtml('/__inspector.html',html));});}}] });
  await server.listen();
  return {url:server.resolvedUrls.local[0]+'__inspector.html',close:()=>server.close()};
}
