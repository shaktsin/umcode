import { createServer } from 'vite';
import { fileURLToPath } from 'node:url';

export async function startUiFixtureServer() {
  const root = fileURLToPath(new URL('..', import.meta.url));
  const html = `<!doctype html><html><head><meta charset="utf-8"><link rel="icon" href="data:,"></head><body><div id="app"></div>
  <script type="module">
  import '/src/app.css';
  import { mount } from 'svelte';
  import App from '/src/App.svelte';
  import { app } from '/src/lib/stores/app.svelte';
  import { chat, ThreadView } from '/src/lib/stores/chat.svelte';
  import { projects } from '/src/lib/stores/projects.svelte';
  import { inspector } from '/src/lib/stores/inspector.svelte';
  const mode = new URLSearchParams(location.search).get('scenario');
  const state = window.uiTest = { calls: [], skillMode: 'normal' };
  const plugin = { id:'source/superpowers', name:'Superpowers with a deliberately long package name', version:'6.4.2', format:'codex', mode:'managed', health:'healthy', enabled:false, source:'/very/long/source/'.repeat(12), schema:[], diagnostics:[], hookFailures:[], components:Array.from({length:15},(_,i)=>({kind:'skill', name:'skill-'+i, path:'skills/skill-'+i, supported:true, health:'available'})) };
  app.call = async (method, params) => {
    state.calls.push({method,params});
    if (method === 'skill/list') {
      if (state.skillMode === 'error') throw new Error('Fixture skill failure');
      return {skills:Array.from({length:15},(_,i)=>({name:'superpowers:skill-'+i,description:'A description for the installed skill '+i}))};
    }
    if (method === 'plugin/list') return {plugins:[plugin]};
    if (method === 'thread/start') return {id:'new-main',title:'New chat',settings:{},updatedAt:'',projectId:params.projectId};
    if (method === 'turn/start') chat.main.sending = false;
    if (method === 'routing/get') return app.routing;
    if (method === 'routing/set') return params;
    if (method === 'model/route') return { chosen:{model:'Claude Sonnet 5',displayName:'Claude Sonnet 5'},alternatives:[] };
    return {};
  };
  app.conn='open';app.booted=true;
  app.routing={pools:[],models:Array.from({length:20},(_,i)=>({id:'model-'+i,provider:'claude',model:'claude-sonnet-'+i,name:'Claude Sonnet '+i,enabled:true}))};
  projects.list=[{id:'demo',name:'Demo',root:'/tmp/demo',vcs:{kind:'git',hasCommit:true},tools:{},settings:{}}];
  projects.activeId='demo';
  chat.main.route={chosen:{model:'Claude Sonnet 5',displayName:'Claude Sonnet 5'},alternatives:[]};
  state.panels = () => {
    const side=new ThreadView();side.id='side-1';side.thread={id:'side-1',title:'Side question',projectId:'demo',settings:{}};
    chat.sides=[side];chat.activeSideId=side.id;
    inspector.openOutput('Test output','A retained inspector tab','fixture-tab');
  };
  state.plugins = () => { app.view='extensions'; };
  state.refreshPlugins = () => window.dispatchEvent(new CustomEvent('fixture:refresh'));
  if(mode==='controls') {
    document.getElementById('app').innerHTML='<main style="padding:24px"><div id="control-row" style="display:flex;align-items:center;gap:8px"><button class="btn-primary" id="standard">Standard</button><input class="input" id="field" style="width:160px" aria-label="Field"><select class="select" id="select" aria-label="Select"><option>Choose</option></select><button class="icon-button" id="icon" aria-label="Action"><svg width="16" height="16"></svg></button></div><p id="help">Help</p><button class="btn-ghost btn-sm" id="compact">Compact</button><button class="icon-button compact" id="compact-icon" aria-label="Compact action"><svg width="16" height="16"></svg></button><textarea class="input" rows="3" aria-label="Multiline"></textarea></main>';
  } else { mount(App,{target:document.getElementById('app')}); }
  </script></body></html>`;
  const server = await createServer({root,server:{host:'127.0.0.1',port:0,strictPort:false},plugins:[{name:'ui-fixtures',configureServer(server){server.middlewares.use('/__ui.html',async(_req,res)=>{res.setHeader('Content-Type','text/html');res.end(await server.transformIndexHtml('/__ui.html',html));});}}]});
  await server.listen();
  return {url:server.resolvedUrls.local[0]+'__ui.html',close:()=>server.close()};
}
