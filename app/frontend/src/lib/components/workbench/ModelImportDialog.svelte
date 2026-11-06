<script lang="ts">
  import { onDestroy } from 'svelte';
  import { FolderOpen, FileUp, X } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import { modalFocus } from '$lib/ui/modalFocus';
  import { ModelImportState } from '$lib/workbench/modelImport.svelte';
  import { importedLayerTitle } from '$lib/workbench/importedLayers';
  import { readModelFiles } from '$lib/workbench/modelImport';
  import { importAvailability,modelAssets,uploadModelBundle,type ModelAsset } from '$lib/workbench/importRuntime';
  import type { OpenWorkbenchModel } from '$lib/workbench/types';
  let { projectId, connected, onClose, onImported }: { projectId: string; connected: boolean; onClose: () => void; onImported: (model: OpenWorkbenchModel) => void } = $props();
  const importer = new ModelImportState((method, params, timeout) => app.call(method, params, timeout));
  let folderInput: HTMLInputElement, fileInput: HTMLInputElement;
  let name = $state(''), reading = $state(false), selection = $state(''), localError = $state(''), tensorSearch = $state('');
  let generation = 0, alive = true;
  let assets=$state.raw<ModelAsset[]>([]), copying=$state(''), savedBundle=$state.raw<OpenWorkbenchModel|null>(null);
  const availability=$derived.by(()=>{try{return importer.inspection?importAvailability(importer.inspection.family,assets.map(a=>a.name)):null;}catch{return null;}});
  $effect(() => { importer.activate(projectId, connected); generation++; reading = false; selection = ''; name = ''; localError = ''; });
  onDestroy(() => { alive = false; generation++; importer.dispose(); });
  const tensors = $derived((importer.inspection?.graph.source.tensors ?? []).filter(t => t.name.toLowerCase().includes(tensorSearch.toLowerCase())));
  async function choose(input: HTMLInputElement) {
    const files = Array.from(input.files ?? []); input.value = ''; if (!files.length) return;
    const token = ++generation; importer.reset(); localError = ''; reading = true; name = ''; tensorSearch = '';
    assets=[];savedBundle=null;
    selection = files.length === 1 ? files[0].name : `${files.length} selected files`;
    const current = () => alive && generation === token && connected;
    try { assets=modelAssets(files); const metadata = await readModelFiles(files, current); if (!current()) return; reading = false; await importer.inspect(metadata); if (current() && importer.inspection) name = `${importer.inspection.name || importer.inspection.family} editable copy`; }
    catch (e) { if (current()) localError = e instanceof Error ? e.message : 'Unable to read model metadata.'; }
    finally { if (current()) reading = false; }
  }
  async function create() {
    const token=generation,current=()=>alive&&generation===token&&connected;
    if(copying)return;localError='';
    const bundle=savedBundle??await importer.create(name);if(!bundle||!current())return;savedBundle=bundle;
    try{
      if(availability?.complete){
        copying='Copying model files to this project…';
        const context={projectId,modelId:bundle.model.id,modelCreatedAt:bundle.model.createdAt};
        await uploadModelBundle((m,p,t)=>app.call(m,p,t),context,assets,current,(done,total)=>copying='Copying model files locally · '+Math.round(done/total*100)+'%');
        copying='Checking weights and preprocessing locally…';
        await app.call('workbench/import/runtime',{...context,action:'validate'},300000);
      }
      if(current())onImported(bundle);
    }catch(e){if(current()){localError='The architecture was saved. '+(e as Error).message+' Open it to complete runtime setup.';onImported(bundle);}}
    finally{if(current())copying='';}
  }
</script>

<div class="import-backdrop">
  <div class="import-dialog" role="dialog" aria-modal="true" aria-labelledby="model-import-title" tabindex="-1" use:modalFocus={onClose}>
    <header><div><h2 id="model-import-title">Import a model</h2><p>Inspect a local Transformer or CNN, then create an editable architecture in Build.</p></div><button class="icon-button" aria-label="Close model import" disabled={!!copying} onclick={onClose}><X size={17} /></button></header>
    <div class="import-content">
      <div class="file-actions"><button class="btn-outline btn-sm" disabled={!connected || importer.busy || !!copying} onclick={() => folderInput.click()}><FolderOpen size={15} />Choose model folder</button><button class="btn-ghost btn-sm" disabled={!connected || importer.busy} onclick={() => fileInput.click()}><FileUp size={15} />Choose files</button><span>{selection}</span></div>
      <input bind:this={folderInput} class="hidden-picker" aria-label="Select model folder" type="file" webkitdirectory multiple onchange={() => void choose(folderInput)} />
      <input bind:this={fileInput} class="hidden-picker" aria-label="Select config and weight files" type="file" multiple accept=".json,.safetensors,.model,.txt" onchange={() => void choose(fileInput)} />
      <p class="privacy">Choose <code>config.json</code> with <code>.safetensors</code> weights, or select the complete model folder once. Include every shard and its index. Complete folders are copied to this project on your Mac, never to a cloud service. Original files stay unchanged.</p>
      {#if !connected}<p class="error" role="alert">Connect to the engine to inspect or create a model.</p>{/if}
      {#if reading || importer.busy || copying}<p role="status" class="status">{copying || (reading ? 'Reading bounded model metadata…' : importer.stage)}</p>{/if}
      {#if localError || importer.error}<p class="error" role="alert">{localError || importer.error}</p>{/if}
      {#if importer.inspection}
        {@const preview = importer.inspection}
        <section class="inspection" aria-label="Model inspection">
          <div class="inspection-title"><h3>{preview.name || preview.family}</h3><span class="badge">{preview.family}</span><span class:verified={preview.weightsVerified} class="badge">{preview.weightsVerified ? 'Weight metadata verified' : 'Weights unverified · architecture preview'}</span></div>
          <dl class="counts"><div><dt>Tensors</dt><dd>{preview.tensorCount.toLocaleString()}</dd></div><div><dt>Parameters in headers</dt><dd>{preview.parameterCount.toLocaleString()}</dd></div><div><dt>Source weight files</dt><dd>{(preview.totalWeightBytes / 1048576).toLocaleString(undefined, { maximumFractionDigits: 1 })} MiB</dd></div><div><dt>Architecture groups</dt><dd>{preview.graph.nodes.length}</dd></div></dl>
          {#if preview.warnings?.length}<ul class="warnings">{#each preview.warnings as warning}<li>{warning}</li>{/each}</ul>{/if}
          {#if !preview.supported}<p class="notice">This family has no architecture adapter yet. Inspect its tensor hierarchy below; creating an editable flow is unavailable.</p>{/if}
          <details open={!preview.supported}><summary>{preview.supported ? 'Native architecture groups' : 'Tensor hierarchy'}</summary><ul class="groups">{#each preview.graph.nodes as node}<li><strong>{importedLayerTitle(node,preview.family)}</strong><span>{node.tensorNames?.length ?? 0} tensors · {node.kind}</span></li>{/each}</ul></details>
          <details><summary>Tensor metadata · {preview.tensorCount.toLocaleString()}</summary><input class="input tensor-search" aria-label="Search inspected tensors" placeholder="Search tensor names" bind:value={tensorSearch} /><div class="tensor-table"><table><thead><tr><th>Tensor</th><th>Shape</th><th>Dtype</th><th>Shard</th></tr></thead><tbody>{#each tensors.slice(0, 100) as tensor}<tr><td>{tensor.name}</td><td>[{tensor.shape.join(', ')}]</td><td>{tensor.dtype}</td><td>{tensor.file}</td></tr>{/each}</tbody></table></div>{#if tensors.length > 100}<p>Showing 100 of {tensors.length.toLocaleString()} matching tensors. Refine the search.</p>{/if}</details>
        </section>
        <p class="runtime-note">{availability?.complete ? 'Complete model assets selected. Creating the copy also imports and validates weights and preprocessing for local Data, Train and Inference. Unsupported model tasks or modified architectures stay gated with a reason.' : 'Build and Ask are available. Data, Train and Inference remain gated until you add: '+(availability?.missing.join(', ')||'the complete model folder')+'.'}</p>
        {#if preview.supported}<label class="model-name">Editable copy name<input class="input" maxlength="128" bind:value={name} disabled={importer.busy} /></label>{/if}
      {/if}
    </div>
    <footer><button class="btn-ghost btn-sm" disabled={!!copying} onclick={onClose}>Cancel</button><button class="btn-primary btn-sm" disabled={!connected || reading || importer.busy || !!copying || !importer.inspection?.supported || !name.trim()} onclick={() => void create()}>Create editable copy</button></footer>
  </div>
</div>

<style>
  .import-backdrop { position:fixed; inset:0; z-index:80; background:#0006; display:flex; align-items:center; justify-content:center; padding:24px; }
  .import-dialog { width:min(800px,100%); max-height:90dvh; display:flex; flex-direction:column; background:var(--color-paper); color:var(--color-ink); border:1px solid var(--color-line); border-radius:14px; box-shadow:0 18px 80px #0004; }
  header, footer { display:flex; align-items:center; gap:16px; padding:17px 22px; } header { align-items:flex-start; border-bottom:1px solid var(--color-line); } header > div { flex:1; } h2 { font-size:18px; font-weight:650; } h3 { font-weight:650; font-size:14px; }
  p { font-size:12px; line-height:1.7; color:var(--color-muted); } header p { margin-top:5px; } .import-content { padding:20px 22px; overflow:auto; } .file-actions { display:flex; flex-wrap:wrap; gap:8px; align-items:center; } .file-actions span { font-size:11px; color:var(--color-muted); } .hidden-picker { display:none; }
  .privacy { margin:12px 0 18px; } code { font:11px var(--font-mono); } .error { color:var(--color-rust); margin:12px 0; } .status { margin:15px 0; } .inspection { border:1px solid var(--color-line); border-radius:9px; padding:15px; } .inspection-title { display:flex; flex-wrap:wrap; align-items:center; gap:8px; } .badge { font-size:10px; background:var(--color-surface); border:1px solid var(--color-line); border-radius:20px; padding:4px 7px; } .verified { color:var(--color-sage); }
  .counts { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:10px; margin:18px 0; } dt { font-size:10px; color:var(--color-muted); } dd { font-size:15px; margin-top:5px; } .warnings { margin:12px 0; color:var(--color-rust); font-size:11px; list-style:disc; padding-left:18px; line-height:1.7; } .notice, .runtime-note { padding:12px; background:var(--color-surface); border-radius:6px; } .runtime-note { margin-top:16px; }
  details { margin-top:12px; font-size:11px; } summary { cursor:pointer; font-weight:600; } .groups { margin:12px 0; max-height:220px; overflow:auto; } .groups li { display:flex; gap:12px; justify-content:space-between; padding:7px 0; border-bottom:1px solid var(--color-line); } .groups span { color:var(--color-muted); flex-shrink:0; } .tensor-search { width:100%; margin:10px 0; } .tensor-table { max-height:250px; overflow:auto; } table { width:100%; border-collapse:collapse; font-size:10px; } th, td { text-align:left; padding:6px; border-bottom:1px solid var(--color-line); } td:first-child { overflow-wrap:anywhere; } td:not(:first-child) { white-space:nowrap; }
  .model-name { display:flex; flex-direction:column; gap:7px; font-size:12px; margin-top:18px; } footer { border-top:1px solid var(--color-line); justify-content:flex-end; } @media(max-width:600px) { .import-backdrop { padding:10px; } .counts { grid-template-columns:repeat(2,1fr); } .groups li { flex-direction:column; gap:4px; } }
</style>
