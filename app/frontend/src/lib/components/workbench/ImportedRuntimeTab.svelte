<script lang="ts">
 import {onDestroy,untrack} from 'svelte';
 import {app} from '$lib/stores/app.svelte';
 import type {Promotion} from '$lib/workbench/inferenceTypes';
 import ImportedChatActions from './ImportedChatActions.svelte';
 import {modelAssets,importAvailability,uploadModelBundle,readRuntimeSnapshot,type RuntimeStatus,type ImportJob} from '$lib/workbench/importRuntime';
 import {parseImportedGraph} from '$lib/workbench/modelImport';
 import DataTab from './DataTab.svelte';
 import type {WorkbenchTask} from '$lib/workbench/types';
 let {projectId,modelId,modelCreatedAt,tab,graphText,task,connected,dirty,onBuild,onData,onInference}:{
 projectId:string;modelId:string;modelCreatedAt:string;tab:string;graphText:string;task:WorkbenchTask;connected:boolean;dirty:boolean;onBuild:()=>void;onData:()=>void;onInference:()=>void;
 }=$props();
 let status=$state<RuntimeStatus|null>(null),job=$state<ImportJob|null>(null),busy=$state(''),error=$state(''),prompt=$state(''),imageBase64=$state(''),imageName=$state(''),response=$state(''),predictions=$state<{label:string;score:number}[]>([]),epochs=$state(1),learningRate=$state(.0001),maxTokens=$state(64),checkpoint=$state('');
 let folder=$state<HTMLInputElement>(),image=$state<HTMLInputElement>();let alive=true,epoch=0,polling=-1,timer:ReturnType<typeof setTimeout>|null=null;
 let promotion=$state<Promotion|null>(null),testId=$state(''),testedScope=$state(''),chatCompatible=$state<boolean|null>(null);
 const promptScope=()=>JSON.stringify([checkpoint,prompt,maxTokens]);
 function clearTest(){testId='';testedScope='';chatCompatible=null;}
 const family=$derived.by(()=>{try{return parseImportedGraph(graphText).source.family;}catch{return '';}});
 const imageModel=$derived(['resnet','convnext','vit'].includes(family));
 const context=()=>({projectId,modelId,modelCreatedAt});
 const call=(action:string,params:Record<string,unknown>={},timeout=300000)=>app.call('workbench/import/runtime',{...context(),action,...params},timeout);
 $effect(()=>{const key=projectId+'/'+modelId+'/'+modelCreatedAt+'/'+connected+'/'+dirty+'/'+(dirty?'':graphText);untrack(()=>{epoch++;if(timer)clearTimeout(timer);status=null;job=null;response='';prompt='';imageBase64='';imageName='';predictions=[];checkpoint='';promotion=null;clearTest();error='';busy='';if(connected&&!dirty)void refresh(epoch);});});
 onDestroy(()=>{alive=false;epoch++;if(timer)clearTimeout(timer);});
 function current(ticket:number){return alive&&ticket===epoch&&connected;}
 async function refresh(ticket=epoch){
  if(polling===ticket)return;polling=ticket;
  const captured=context();
  try{const [snapshot,saved]=await Promise.all([readRuntimeSnapshot(action=>app.call('workbench/import/runtime',{...captured,action},300000)),app.call('workbench/import/runtime',{...captured,action:'promotion'}) as Promise<{promotion:Promotion|null}>]);
      if(current(ticket)){status=snapshot.status;job=snapshot.job;promotion=saved.promotion;}
  }catch(e){if(current(ticket))error=(e as Error).message;}
  finally{if(polling===ticket)polling=-1;if(current(ticket)&&job?.status==='running'){if(timer)clearTimeout(timer);timer=setTimeout(()=>void refresh(ticket),2000);}}
 }
 async function validate(){if(tab!=='Build'||!connected||dirty||busy||running)return;const ticket=epoch;busy='Loading and checking the imported weights locally…';error='';try{const result=await call('validate') as RuntimeStatus;if(current(ticket))status=result;}catch(e){if(current(ticket))error=(e as Error).message;}finally{if(current(ticket))busy='';}}
 async function attach(event:Event){
  const input=event.currentTarget as HTMLInputElement,files=Array.from(input.files??[]);input.value='';if(!files.length||tab!=='Build'||!connected||dirty||busy||running)return;
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
 async function infer(){const ticket=epoch,scope=promptScope();busy='Running locally on CPU…';error='';response='';predictions=[];clearTest();
  try{const result=await call('infer',{prompt,imageBase64,checkpoint,maxTokens}) as {text?:string;testId?:string;chatCompatible?:boolean;predictions?:{label:string;score:number}[]};if(current(ticket)){response=result.text??'';predictions=result.predictions??[];chatCompatible=!!result.chatCompatible;if(scope===promptScope()){testId=result.testId??'';testedScope=scope;}if(status?.task==='text-generation'&&!response.trim())error='This model returned no text. Try another prompt or weights before promoting it.';}}
  catch(e){if(current(ticket))error=(e as Error).message;}finally{if(current(ticket))busy='';}
 }
 async function promote(){if(!canPromote)return;const ticket=epoch;busy='Adding the tested weights to Chat…';error='';
  try{const result=await call('promote',{checkpoint,testId}) as Promotion;if(current(ticket))promotion=result;await app.refreshCatalog();}
  catch(e){if(current(ticket))error=(e as Error).message;}finally{if(current(ticket))busy='';}
 }
 async function removePromotion(){const ticket=epoch;busy='Removing from Chat…';error='';try{await call('unpromote');if(current(ticket))promotion=null;await app.refreshCatalog();}catch(e){if(current(ticket))error=(e as Error).message;}finally{if(current(ticket))busy='';}}
 async function openChat(){if(!promotion)return;const ticket=epoch,selected=promotion;error='';try{const {chat}=await import('$lib/stores/chat.svelte');if(!current(ticket))return;chat.newChat(projectId);await chat.main.setSelection({provider:'workbench_local',model:selected.id});window.dispatchEvent(new CustomEvent('umcode:compose'));}catch(e){if(current(ticket))error=(e as Error).message;}}
 async function train(){const ticket=epoch;busy='Starting local fine-tuning…';error='';
  try{const result=await call('train',{epochs,learningRate}) as ImportJob;if(current(ticket)){job=result;void refresh(ticket);}}
  catch(e){if(current(ticket))error=(e as Error).message;}finally{if(current(ticket))busy='';}
 }
 async function cancel(){error='';try{await call('cancel');await refresh();}catch(e){error=(e as Error).message;}}
 const gate=$derived(dirty?'Save or revert your Build changes before validating the imported weights.':status?.reason||'Checking imported model readiness…');
 const blocked=$derived(!connected||dirty||!status?.ready);
 const running=$derived(job?.status==='running');
 const canPromote=$derived(!blocked&&!busy&&!running&&!!testId&&chatCompatible===true&&testedScope===promptScope());
 const metrics=$derived.by(()=>{const m=job?.result?.metrics;if(!m||typeof m!=='object')return [];return Object.entries(m).filter(([,v])=>v&&typeof v==='object').map(([split,v])=>({split,values:Object.entries(v as Record<string,unknown>).filter(([,n])=>typeof n==='number')}));});
</script>
<section class="runtime-tab" class:is-build={tab==='Build'} aria-label={'Imported model '+tab}>
 {#if tab!=='Build'}
 <header><div><h2>{tab} · Imported {imageModel?'vision':'text'} model</h2><p>Runs on your Mac · Built-in Transformers runtime · Source files stay unchanged</p></div><button class="btn-outline btn-sm" disabled={!connected||!!busy} onclick={()=>void refresh()}>Refresh</button></header>
 {/if}
 {#if tab==='Build'}
  <section class="model-files" aria-label="Model files">
   <div><h3>Model files</h3><p>{!connected?'Connect to the local engine to set up model files.':blocked?gate:'Model files validated. Data, Train and Inference use this model’s attached files.'}</p>
   {#if blocked}<p>Add the complete folder for this architecture here. Folders with a different configuration or tensor layout are rejected; import those as a new model. You can still inspect and edit layers below.</p>{/if}</div>
   <div class="actions">{#if !status?.ready}<button class="btn-outline" disabled={!connected||!!busy||dirty||running} onclick={()=>folder?.click()}>Add complete model folder</button>{/if}<button class="btn-outline" disabled={!connected||!!busy||dirty||running} onclick={validate}>Validate local model</button></div>
  </section>
 {:else if blocked}
  <section class="gate" aria-label={tab+' unavailable'}>
   <h3>{tab} is not ready yet</h3><p>{!connected?'Connect to the local engine to continue.':gate}</p>
   <p>{tab==='Data'?'Data preparation needs this model’s tokenizer or image processor so the inputs match its weights.':tab==='Train'?'Training needs compatible weights, preprocessing, and saved Data preparation. A pair of architecture files is not enough.':'Inference needs all trained weight files plus the matching tokenizer or image processor. It does not require training again.'}</p>
   <p>Model files are managed only in Build. All tabs use the same model selected there.</p>
   <div class="actions"><button class="btn-primary" onclick={onBuild}>Go to Build</button></div>
  </section>
 {:else if tab==='Data'}
  <DataTab {projectId} {modelId} {modelCreatedAt} {task} {graphText} {connected} {onBuild} imported />
 {:else if tab==='Inference'}
  <section class="card">
   <h3>Try the loaded model</h3><p>The model uses its original tokenizer or image preprocessing. Architecture edits must be compatible before execution.</p>
   <label>Weights<select class="select" bind:value={checkpoint} disabled={!!busy||running} onchange={()=>{clearTest();response='';predictions=[];}}><option value="">Original imported weights</option>{#if status?.checkpoint}<option value={status.checkpoint}>Latest local fine-tuned checkpoint</option>{/if}</select></label>
   {#if imageModel}<label>Image<button class="btn-outline" disabled={!!busy||running} onclick={()=>image?.click()}>Choose PNG / JPEG</button></label><p>{imageName}</p>
   {:else}<label>Input text<textarea class="input" rows="5" maxlength="16000" bind:value={prompt} placeholder={status?.task==='text-generation'?'Ask a question or enter a prompt…':'Enter the text to classify…'}></textarea></label>{/if}
   {#if status?.task==='text-generation'}<label>Maximum new tokens<input class="input" type="number" min="1" max="256" bind:value={maxTokens} /></label>{/if}
   <div class="actions"><button class="btn-primary" disabled={!!busy||running||(imageModel?!imageBase64:!prompt.trim())} onclick={infer}>Run {imageModel?'prediction':'prompt'}</button>
   {#if status?.task==='text-generation'}<ImportedChatActions {canPromote} busy={!!busy||running||!connected} {promotion} onPromote={()=>void promote()} onOpen={()=>void openChat()} onRemove={()=>void removePromotion()} />{/if}</div>
   {#if status?.task==='text-generation'}<p>{chatCompatible===false?'This tokenizer has no chat template. Prompt completion works, but this model cannot be promoted to Chat.':'Run a successful prompt test, then promote these weights to your regular chat model picker. Chat requires this model’s native chat template; promotion does not improve response quality.'}</p>{/if}
   {#if response}<div class="result" aria-live="polite">{response}</div>{/if}
   {#if predictions.length}<ul class="predictions">{#each predictions as p}<li><strong>{p.label}</strong><span>{(p.score*100).toFixed(2)}%</span></li>{/each}</ul>{/if}
  </section>
 {:else if tab==='Train'}
  <section class="card"><h3>{status?.task==='text-generation'?'Fine-tune with LoRA':'Fine-tune the classification head'}</h3>
   <p>{status?.task==='text-generation'?'Trains small attention adapters (rank 8) on CPU while the base weights stay frozen. Each run starts from your original imported weights. The best validation checkpoint is saved as a separate model with the adapters merged for Inference and Chat.':'Updates the classification head on CPU while the backbone stays frozen.'} Original source files are never overwritten.</p>
   {#if status?.task==='text-generation'}<p>For questions and answers, select both the prompt and response columns in Data, then click Save preparation again. Training uses the native chat format and learns from answers only. A text corpus trains continuation, not question answering. Start with one epoch; LoRA does not guarantee better answers.</p>{/if}
   <div class="settings"><label>Epochs<input class="input" type="number" min="1" max="10" bind:value={epochs} disabled={running} /></label><label>Learning rate<input class="input" type="number" min="0.0000001" max={status?.task==='text-generation'?0.001:0.1} step="0.00001" bind:value={learningRate} disabled={running} /></label></div>
   <div class="actions"><button class="btn-primary" disabled={!!busy||running} onclick={train}>Start fine-tuning</button><button class="btn-outline" onclick={onData}>Review Data</button>{#if running}<button class="btn-outline" onclick={cancel}>Stop training</button>{/if}</div>
   {#if job}<h3 class="job-state">{job.status} · Epoch {job.epoch}/{job.epochs}</h3><progress max={job.epochs||1} value={job.epoch}></progress>
    {#if job.trainLoss!==undefined}<p>Training loss: {job.trainLoss.toFixed(4)}{job.validationLoss!==undefined?' · Validation loss: '+job.validationLoss.toFixed(4):''}</p>{/if}
    {#if job.error}<p class="error">{job.error}</p>{/if}
    {#if job.status==='completed'}<p>Checkpoint saved separately from the original model.{#if typeof job.result?.bestEpoch==='number'} Selected epoch: {job.result.bestEpoch===0?'0 · original baseline':job.result.bestEpoch}.{/if}{#if typeof job.result?.trainableParameters==='number'} Trainable parameters: {job.result.trainableParameters.toLocaleString()}.{/if}</p>{#if typeof job.result?.metricsNote==='string'}<p class="feedback">{job.result.metricsNote}</p>{/if}<button class="btn-primary" onclick={()=>{checkpoint=status?.checkpoint??'';onInference();}}>Try checkpoint in Inference</button>{/if}
   {/if}
   {#if metrics.length}<h3>Held-out evaluation</h3><div class="settings">{#each metrics as metric}<section><h4>{metric.split}</h4><dl>{#each metric.values as [key,value]}<dt>{key}</dt><dd>{Number(value).toLocaleString(undefined,{maximumFractionDigits:4})}</dd>{/each}</dl></section>{/each}</div>{/if}
  </section>
 {/if}
 {#if tab==='Build'}<input bind:this={folder} type="file" webkitdirectory multiple onchange={attach} hidden />{/if}
 {#if tab==='Inference'}<input bind:this={image} type="file" accept="image/png,image/jpeg" onchange={chooseImage} hidden />{/if}
 {#if busy}<p role="status" class="feedback">{busy}</p>{/if}
 {#if error}<p role="alert" class="feedback error">{error}</p>{/if}
</section>
<style>
 .runtime-tab.is-build{flex:0 0 auto;padding:12px 16px;border-bottom:1px solid var(--color-line);max-height:35%;overflow:auto}.model-files{display:flex;align-items:center;justify-content:space-between;gap:20px}.model-files>div:first-child{flex:1;min-width:0}.model-files h3{margin:0}.model-files p{margin:4px 0}.model-files .actions{margin:0;flex-shrink:0}@media(max-width:900px){.model-files{align-items:flex-start;flex-direction:column;gap:8px}}
 .runtime-tab{flex:1;min-height:0;overflow:auto;padding:24px;user-select:text}header{display:flex;justify-content:space-between;align-items:center;gap:15px;margin-bottom:20px}h2{font-size:20px;font-weight:650}h3{font-size:15px;font-weight:600;margin-bottom:10px}h4{text-transform:capitalize}p{font-size:12px;line-height:1.7;color:var(--color-muted);margin:10px 0}.gate,.card{padding:24px;border:1px solid var(--color-line);border-radius:12px;background:var(--color-surface);max-width:900px;margin:20px auto}.actions{display:flex;gap:12px;flex-wrap:wrap;margin-top:20px}label{display:flex;flex-direction:column;gap:8px;font-size:12px;margin:16px 0}.select,.input{width:100%}.settings{display:flex;gap:24px;flex-wrap:wrap}.settings>label{flex:1}.result{padding:16px;margin-top:20px;border:1px solid var(--color-line);border-radius:8px;white-space:pre-wrap;overflow-wrap:anywhere;font-size:14px}.error{color:var(--color-rust)}.feedback{white-space:pre-wrap;overflow-wrap:anywhere}progress{width:100%}.job-state{margin-top:24px}.predictions{margin-top:20px}.predictions li{display:flex;justify-content:space-between;font-size:13px;margin:10px 0}dl{display:grid;grid-template-columns:1fr 1fr;gap:8px;font-size:12px;margin-top:12px}dt{color:var(--color-muted)}
</style>
