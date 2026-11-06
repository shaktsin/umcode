<script lang="ts">
 import {onDestroy,untrack} from 'svelte';
 import {app} from '$lib/stores/app.svelte';
 import {modelAssets,importAvailability,uploadModelBundle,readRuntimeSnapshot,type RuntimeStatus,type ImportJob} from '$lib/workbench/importRuntime';
 import {parseImportedGraph} from '$lib/workbench/modelImport';
 import DataTab from './DataTab.svelte';
 import type {WorkbenchTask} from '$lib/workbench/types';
 let {projectId,modelId,modelCreatedAt,tab,graphText,task,connected,dirty,onBuild,onData,onInference}:{
 projectId:string;modelId:string;modelCreatedAt:string;tab:string;graphText:string;task:WorkbenchTask;connected:boolean;dirty:boolean;onBuild:()=>void;onData:()=>void;onInference:()=>void;
 }=$props();
 let status=$state<RuntimeStatus|null>(null),job=$state<ImportJob|null>(null),busy=$state(''),error=$state(''),prompt=$state(''),imageBase64=$state(''),imageName=$state(''),response=$state(''),predictions=$state<{label:string;score:number}[]>([]),epochs=$state(1),learningRate=$state(.001),maxTokens=$state(64),checkpoint=$state('');
 let folder:HTMLInputElement,image:HTMLInputElement;let alive=true,epoch=0,polling=-1,timer:ReturnType<typeof setTimeout>|null=null;
 const family=$derived.by(()=>{try{return parseImportedGraph(graphText).source.family;}catch{return '';}});
 const imageModel=$derived(['resnet','convnext','vit'].includes(family));
 const context=()=>({projectId,modelId,modelCreatedAt});
 const call=(action:string,params:Record<string,unknown>={},timeout=300000)=>app.call('workbench/import/runtime',{...context(),action,...params},timeout);
 $effect(()=>{const key=projectId+'/'+modelId+'/'+modelCreatedAt+'/'+connected;untrack(()=>{epoch++;if(timer)clearTimeout(timer);status=null;job=null;response='';prompt='';imageBase64='';imageName='';predictions=[];checkpoint='';error='';busy='';if(connected)void refresh(epoch);});});
 onDestroy(()=>{alive=false;epoch++;if(timer)clearTimeout(timer);});
 function current(ticket:number){return alive&&ticket===epoch&&connected;}
 async function refresh(ticket=epoch){
  if(polling===ticket)return;polling=ticket;
  const captured=context();
  try{const snapshot=await readRuntimeSnapshot(action=>app.call('workbench/import/runtime',{...captured,action},300000));
      if(current(ticket)){status=snapshot.status;job=snapshot.job;}
  }catch(e){if(current(ticket))error=(e as Error).message;}
  finally{if(polling===ticket)polling=-1;if(current(ticket)&&job?.status==='running'){if(timer)clearTimeout(timer);timer=setTimeout(()=>void refresh(ticket),2000);}}
 }
 async function validate(){const ticket=epoch;busy='Loading and checking the imported weights locally…';error='';try{const result=await call('validate') as RuntimeStatus;if(current(ticket))status=result;}catch(e){if(current(ticket))error=(e as Error).message;}finally{if(current(ticket))busy='';}}
 async function attach(event:Event){
  const input=event.currentTarget as HTMLInputElement,files=Array.from(input.files??[]);input.value='';if(!files.length)return;
  const ticket=epoch;busy='Checking selected folder…';error='';
  try{const assets=modelAssets(files),availability=importAvailability(family,assets.map(a=>a.name));if(!availability.complete)throw Error('Complete folder still needs: '+availability.missing.join(', '));
   const captured=context();
   await uploadModelBundle((m,p,t)=>app.call(m,p,t),captured,assets,()=>current(ticket),(done,total)=>busy='Copying model locally · '+Math.round(done/total*100)+'%');
   if(current(ticket)){busy='Checking weights and preprocessing…';const result=await call('validate') as RuntimeStatus;if(current(ticket))status=result;}
  }catch(e){if(current(ticket))error=(e as Error).message;}finally{if(current(ticket))busy='';}
 }
 async function chooseImage(event:Event){const f=(event.currentTarget as HTMLInputElement).files?.[0];if(!f)return;error='';
  if(!['image/png','image/jpeg'].includes(f.type)||f.size>8*1024*1024){error='Choose a PNG or JPEG smaller than 8 MiB.';return;}
  const ticket=epoch,bytes=new Uint8Array(await f.arrayBuffer());let binary='';for(let i=0;i<bytes.length;i+=8192)binary+=String.fromCharCode(...bytes.subarray(i,i+8192));if(current(ticket)){imageBase64=btoa(binary);imageName=f.name;}
 }
 async function infer(){const ticket=epoch;busy='Running locally on CPU…';error='';response='';predictions=[];
  try{const result=await call('infer',{prompt,imageBase64,checkpoint,maxTokens}) as {text?:string;predictions?:{label:string;score:number}[]};if(current(ticket)){response=result.text??'';predictions=result.predictions??[];}}
  catch(e){if(current(ticket))error=(e as Error).message;}finally{if(current(ticket))busy='';}
 }
 async function train(){const ticket=epoch;busy='Starting local fine-tuning…';error='';
  try{const result=await call('train',{epochs,learningRate}) as ImportJob;if(current(ticket)){job=result;void refresh(ticket);}}
  catch(e){if(current(ticket))error=(e as Error).message;}finally{if(current(ticket))busy='';}
 }
 async function cancel(){error='';try{await call('cancel');await refresh();}catch(e){error=(e as Error).message;}}
 const gate=$derived(dirty?'Save or revert your Build changes before validating the imported weights.':status?.reason||'Checking imported model readiness…');
 const blocked=$derived(!connected||dirty||!status?.ready);
 const running=$derived(job?.status==='running');
 const metrics=$derived.by(()=>{const m=job?.result?.metrics;if(!m||typeof m!=='object')return [];return Object.entries(m).filter(([,v])=>v&&typeof v==='object').map(([split,v])=>({split,values:Object.entries(v as Record<string,unknown>).filter(([,n])=>typeof n==='number')}));});
</script>
<section class="runtime-tab" aria-label={'Imported model '+tab}>
 <header><div><h2>{tab} · Imported {imageModel?'vision':'text'} model</h2><p>Runs on your Mac · Built-in Transformers runtime · Source files stay unchanged</p></div><button class="btn-outline btn-sm" disabled={!connected||!!busy} onclick={()=>void refresh()}>Refresh</button></header>
 {#if blocked}
  <section class="gate" aria-label={tab+' unavailable'}>
   <h3>{tab} is not ready yet</h3><p>{!connected?'Connect to the local engine to continue.':gate}</p>
   <p>{tab==='Data'?'Data preparation needs this model’s tokenizer or image processor so the inputs match its weights.':tab==='Train'?'Training needs compatible weights, preprocessing, and saved Data preparation. A pair of architecture files is not enough.':'Inference needs all trained weight files plus the matching tokenizer or image processor. It does not require training again.'}</p>
   <div class="actions"><button class="btn-outline" disabled={!connected||!!busy||dirty} onclick={()=>folder.click()}>Add complete model folder</button><button class="btn-primary" disabled={!connected||!!busy||dirty} onclick={validate}>Validate local model</button><button class="btn-ghost" onclick={onBuild}>Back to Build</button></div>
  </section>
 {:else if tab==='Data'}
  <DataTab {projectId} {modelId} {modelCreatedAt} {task} {graphText} {connected} {onBuild} imported />
 {:else if tab==='Inference'}
  <section class="card">
   <h3>Try the loaded model</h3><p>The model uses its original tokenizer or image preprocessing. Architecture edits must be compatible before execution.</p>
   <label>Weights<select class="select" bind:value={checkpoint}><option value="">Original pretrained weights</option>{#if status?.checkpoint}<option value={status.checkpoint}>Latest local fine-tuned checkpoint</option>{/if}</select></label>
   {#if imageModel}<label>Image<button class="btn-outline" disabled={!!busy||running} onclick={()=>image.click()}>Choose PNG / JPEG</button></label><p>{imageName}</p>
   {:else}<label>Input text<textarea class="input" rows="5" maxlength="16000" bind:value={prompt} placeholder={status?.task==='text-generation'?'Ask a question or enter a prompt…':'Enter the text to classify…'}></textarea></label>{/if}
   {#if status?.task==='text-generation'}<label>Maximum new tokens<input class="input" type="number" min="1" max="256" bind:value={maxTokens} /></label>{/if}
   <button class="btn-primary" disabled={!!busy||running||(imageModel?!imageBase64:!prompt.trim())} onclick={infer}>Run {imageModel?'prediction':'prompt'}</button>
   {#if response}<div class="result" aria-live="polite">{response}</div>{/if}
   {#if predictions.length}<ul class="predictions">{#each predictions as p}<li><strong>{p.label}</strong><span>{(p.score*100).toFixed(2)}%</span></li>{/each}</ul>{/if}
  </section>
 {:else if tab==='Train'}
  <section class="card"><h3>Fine-tune the output head</h3><p>Updates the prediction head on CPU; the backbone stays frozen. First save your Data preparation. Original source weights are never overwritten.</p>
   <div class="settings"><label>Epochs<input class="input" type="number" min="1" max="10" bind:value={epochs} /></label><label>Learning rate<input class="input" type="number" min="0.000001" max="0.1" step="0.0001" bind:value={learningRate} /></label></div>
   <div class="actions"><button class="btn-primary" disabled={!!busy||running} onclick={train}>Start fine-tuning</button><button class="btn-outline" onclick={onData}>Review Data</button>{#if running}<button class="btn-outline" onclick={cancel}>Stop training</button>{/if}</div>
   {#if job}<h3 class="job-state">{job.status} · Epoch {job.epoch}/{job.epochs}</h3><progress max={job.epochs||1} value={job.epoch}></progress>
    {#if job.trainLoss!==undefined}<p>Training loss: {job.trainLoss.toFixed(4)}{job.validationLoss!==undefined?' · Validation loss: '+job.validationLoss.toFixed(4):''}</p>{/if}
    {#if job.error}<p class="error">{job.error}</p>{/if}
    {#if job.status==='completed'}<p>Fine-tuned checkpoint saved separately from the original model.</p><button class="btn-primary" onclick={()=>{checkpoint=status?.checkpoint??'';onInference();}}>Try checkpoint in Inference</button>{/if}
   {/if}
   {#if metrics.length}<h3>Held-out evaluation</h3><div class="settings">{#each metrics as metric}<section><h4>{metric.split}</h4><dl>{#each metric.values as [key,value]}<dt>{key}</dt><dd>{Number(value).toLocaleString(undefined,{maximumFractionDigits:4})}</dd>{/each}</dl></section>{/each}</div>{/if}
  </section>
 {/if}
 <input bind:this={folder} type="file" webkitdirectory multiple onchange={attach} hidden />
 <input bind:this={image} type="file" accept="image/png,image/jpeg" onchange={chooseImage} hidden />
 {#if busy}<p role="status" class="feedback">{busy}</p>{/if}
 {#if error}<p role="alert" class="feedback error">{error}</p>{/if}
</section>
<style>
 .runtime-tab{flex:1;min-height:0;overflow:auto;padding:24px;user-select:text}header{display:flex;justify-content:space-between;align-items:center;gap:15px;margin-bottom:20px}h2{font-size:20px;font-weight:650}h3{font-size:15px;font-weight:600;margin-bottom:10px}h4{text-transform:capitalize}p{font-size:12px;line-height:1.7;color:var(--color-muted);margin:10px 0}.gate,.card{padding:24px;border:1px solid var(--color-line);border-radius:12px;background:var(--color-surface);max-width:900px;margin:20px auto}.actions{display:flex;gap:12px;flex-wrap:wrap;margin-top:20px}label{display:flex;flex-direction:column;gap:8px;font-size:12px;margin:16px 0}.select,.input{width:100%}.settings{display:flex;gap:24px;flex-wrap:wrap}.settings>label{flex:1}.result{padding:16px;margin-top:20px;border:1px solid var(--color-line);border-radius:8px;white-space:pre-wrap;overflow-wrap:anywhere;font-size:14px}.error{color:var(--color-rust)}.feedback{white-space:pre-wrap;overflow-wrap:anywhere}progress{width:100%}.job-state{margin-top:24px}.predictions{margin-top:20px}.predictions li{display:flex;justify-content:space-between;font-size:13px;margin:10px 0}dl{display:grid;grid-template-columns:1fr 1fr;gap:8px;font-size:12px;margin-top:12px}dt{color:var(--color-muted)}
</style>
