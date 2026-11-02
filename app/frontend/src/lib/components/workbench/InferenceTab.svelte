<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import { ArrowRight, Check, Cpu, FlaskConical, MessageCircle, Play, RefreshCw, Upload } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import { chat } from '$lib/stores/chat.svelte';
  import { WorkbenchInferenceState } from '$lib/workbench/inference.svelte';
  import { checkpointSize } from '$lib/workbench/inference';
  let { projectId, modelId, modelCreatedAt, name, connected, dirty, onBuild, onData, initialCheckpointId = '' }: { projectId: string; modelId: string; modelCreatedAt: string; name: string; connected: boolean; dirty: boolean; initialCheckpointId?: string; onBuild: () => void; onData: () => void } = $props();
  const inference = new WorkbenchInferenceState((m,p,t) => app.call(m,p,t), () => app.refreshCatalog());
  let weightsInput = $state<HTMLInputElement>(), imageInput = $state<HTMLInputElement>();
  let checkpointName = $state('');
  $effect(() => { const context = { projectId, modelId, modelCreatedAt }, online = connected, checkpointId = initialCheckpointId; untrack(() => { void inference.activate(context, online, checkpointId); }); });
  onDestroy(() => inference.dispose());
  async function chooseWeights(event: Event) {
    const input = event.currentTarget as HTMLInputElement, file = input.files?.[0]; input.value = '';
    if (file) await inference.importCheckpoint(file, checkpointName.trim() || file.name.replace(/\.[^.]+$/, ''));
  }
  async function openChat() {
    const promotion = inference.promotion; if (!promotion) return;
    chat.newChat(projectId); await chat.main.setSelection({ provider: 'workbench_local', model: promotion.id });
    window.dispatchEvent(new CustomEvent('umcode:compose'));
  }
</script>

<section class="inference-tab" data-inference-tab aria-label="Local model inference">
  <header class="heading"><div><h2>Try {name}</h2><p>Run this model on your Mac, then promote a tested chat checkpoint.</p></div><span class="badge"><Cpu size={14} />Local only · CPU</span></header>
  {#if dirty}<div class="feedback warning">You have unsaved Build changes. Architecture tests and checkpoint imports use the saved model. <button class="text-button" onclick={onBuild}>Review in Build</button></div>{/if}
  <div class="layout">
    <aside>
      <section class="card">
        <h3>Model</h3><strong>{name}</strong><p>This model’s saved architecture and preparation are pinned when weights are imported.</p>
        <label for="inference-checkpoint">Checkpoint</label>
        <div class="select-row"><select id="inference-checkpoint" class="select" value={inference.checkpointId} disabled={inference.busy || !connected} onchange={(e) => inference.selectCheckpoint(e.currentTarget.value)}><option value="">Choose trained weights</option>{#each inference.checkpoints as cp}<option value={cp.id}>{cp.name}</option>{/each}</select><button class="icon-button" aria-label="Refresh checkpoints" disabled={inference.busy || !connected} onclick={() => void inference.refresh()}><RefreshCw size={14} /></button></div>
        {#if inference.checkpoint}<p>{checkpointSize(inference.checkpoint.byteCount)} · {inference.checkpoint.format} · {new Date(inference.checkpoint.createdAt).toLocaleString()}</p>{/if}
        <label for="inference-runtime">Runtime</label><select id="inference-runtime" class="select" value={inference.runtime} disabled={inference.busy || !connected} onchange={(e) => inference.selectRuntime(e.currentTarget.value)}>{#each inference.runtimes as runtime}<option value={runtime.id} disabled={!runtime.available}>{runtime.name}{runtime.available ? '' : ' · unavailable'}</option>{/each}</select>
        <details class="runtime-details"><summary>Runtime compatibility</summary>{#each inference.runtimes as runtime}<p><strong>{runtime.name}</strong> — {runtime.available ? 'Available for this model.' : runtime.reason}</p>{/each}</details>
        <div class="actions"><button class="btn-outline btn-sm" disabled={!connected || inference.busy || !inference.checkpoint || !inference.availableRuntime} onclick={() => void inference.load()}><Play size={13} />Load checkpoint</button>{#if inference.loadedCheckpointId}<button class="btn-ghost btn-sm" disabled={inference.busy || !connected} onclick={() => void inference.unload()}>Unload</button>{/if}</div>
        <p class="status" class:ready={inference.loadedCheckpointId === inference.checkpointId && !!inference.checkpointId}>{inference.loadedCheckpointId === inference.checkpointId && inference.checkpointId ? 'Loaded locally' : 'Not loaded'}</p>
      </section>
      <section class="card">
        <h3>Import trained weights</h3><p>Save your Data preparation first. Import a tensor state_dict exported from this architecture; weights must match its layers.</p>
        <label for="checkpoint-name">Checkpoint name (optional)</label><input id="checkpoint-name" class="input" bind:value={checkpointName} maxlength="100" placeholder="e.g. trained v1" />
        <button class="btn-outline btn-sm" disabled={!connected || inference.busy || dirty} onclick={() => weightsInput?.click()}><Upload size={14} />Import weights</button>
        <input bind:this={weightsInput} type="file" accept=".pt,.pth,.safetensors" onchange={chooseWeights} hidden />
        <p>.pt / .pth / .safetensors · up to 64 MiB</p><button class="text-button" onclick={onData}>Review Data preparation <ArrowRight size={12} /></button>
      </section>
    </aside>
    <div class="main-column">
      <section class="card architecture">
        <div><h3>No trained checkpoint yet?</h3><p>Test that the saved layers execute and their input/output shapes match. Random weights won’t produce learned predictions.</p></div>
        <button class="btn-outline btn-sm" disabled={!connected || inference.busy || !inference.availableRuntime || dirty} onclick={() => void inference.testArchitecture()}><FlaskConical size={14} />Test architecture</button>
        {#if inference.architecture}<div class="architecture-result" role="status"><strong><Check size={14} />Architecture ran successfully</strong><p>Input [{inference.architecture.inputShape.join(' × ')}] → Output [{inference.architecture.outputShape.join(' × ')}]</p><p>{inference.architecture.parameterCount.toLocaleString()} parameters · {inference.architecture.device} · untrained random weights</p><p>Import trained weights to test predictions or promote to Chat.</p></div>{/if}
      </section>
      <section class="card playground">
        <div class="section-heading"><h3>Test a prediction</h3>{#if inference.checkpoint}<span class="badge">{inference.checkpoint.input.mode} input</span>{/if}</div>
        {#if !inference.checkpoint}<div class="empty"><FlaskConical size={26} /><h4>Choose trained weights to begin</h4><p>You can check your architecture above without training. For learned predictions, import weights from a trained version of this model.</p></div>
        {:else if inference.checkpoint.input.mode === 'text'}
          <label for="inference-prompt">{inference.checkpoint.chatCompatible ? 'Try a prompt' : 'Input text'}</label><textarea id="inference-prompt" class="input" rows="6" value={inference.prompt} placeholder="Type a prompt…" disabled={inference.busy} oninput={(e) => inference.setPrompt(e.currentTarget.value)}></textarea>
          <p>Context: {inference.checkpoint.input.sequenceLength} UTF-8 byte tokens{inference.checkpoint.chatCompatible ? ' including message framing and output.' : '.'}</p>
          {#if inference.checkpoint.chatCompatible}<div class="generation-settings"><label>Temperature<input class="input" type="number" min="0" max="2" step="0.1" value={inference.temperature} onchange={(e) => inference.setTemperature(Number(e.currentTarget.value))} disabled={inference.busy} /></label><label>Max output tokens<input class="input" type="number" min="1" max="256" value={inference.maxNewTokens} onchange={(e) => inference.setMaxNewTokens(Number(e.currentTarget.value))} disabled={inference.busy} /></label></div>{/if}
        {:else if inference.checkpoint.input.mode === 'features'}
          <p>Enter raw feature values. Saved training normalization is applied automatically.</p><div class="feature-grid">{#each inference.checkpoint.input.features as key}<label>{key}<input class="input" type="number" step="any" value={inference.featureValue(key)} disabled={inference.busy} oninput={(e) => inference.setFeature(key,e.currentTarget.value)} /></label>{/each}</div>
        {:else if inference.checkpoint.input.mode === 'image'}
          <p>Images are resized to {inference.checkpoint.input.imageWidth} × {inference.checkpoint.input.imageHeight}, {inference.checkpoint.input.imageChannels} channels, and scaled to 0–1.</p><button class="btn-outline" disabled={inference.busy} onclick={() => imageInput?.click()}>Choose an image</button><input type="file" bind:this={imageInput} accept=".png,.jpg,.jpeg,.webp,.bmp" hidden onchange={(e) => {const input=e.currentTarget, file=input.files?.[0];input.value='';if(file)void inference.importImage(file);}} />{#if inference.imageName}<p>{inference.imageName}</p>{/if}
        {/if}
        <div class="actions primary-actions"><button class="btn-primary" disabled={!inference.canPredict} onclick={() => void inference.predict()}><Play size={14} />{inference.checkpoint?.chatCompatible ? 'Try prompt' : 'Run prediction'}</button><button class="btn-outline" disabled={!inference.canPromote || inference.isPromoted} onclick={() => void inference.promote()}><MessageCircle size={14} />{inference.isPromoted ? 'Promoted to Chat' : 'Promote to Chat'}</button></div>
        <p>{inference.checkpoint?.chatCompatible ? 'Run a successful prompt test to enable promotion. The tested checkpoint will appear in your regular chat model picker.' : 'Chat promotion is available for trained causal text-generation models.'}</p>
        {#if inference.prediction}<div class="prediction" aria-label="Prediction result"><div class="section-heading"><h4>Result</h4><small>{inference.prediction.device} · {inference.prediction.durationMs} ms</small></div>{#if inference.prediction.text !== undefined}<pre>{inference.prediction.text}</pre>{:else if inference.prediction.value !== undefined}<strong class="value">{inference.prediction.value}</strong>{:else}<strong class="value">{inference.prediction.label}</strong>{#each inference.prediction.scores ?? [] as score}<div class="score"><span>{score.label}</span><meter min="0" max="1" value={score.value}></meter><span>{(score.value*100).toFixed(1)}%</span></div>{/each}{/if}{#if inference.prediction.truncated}<p>Stopped at the output or context limit.</p>{/if}</div>{/if}
      </section>
      {#if inference.promotion}<section class="card promoted"><div><h3><Check size={14} />Available in Chat</h3><p>{inference.promotion.name} · pinned checkpoint</p>{#if inference.promotion.promptFormat === 'prompt-response-v1'}<p>This model was trained on individual question/answer pairs. Start a new chat for each question.</p>{/if}</div><div class="actions"><button class="btn-primary btn-sm" onclick={() => void openChat()}><MessageCircle size={14} />Open in Chat</button><button class="btn-ghost btn-sm" disabled={!connected || inference.busy} onclick={() => void inference.unpromote()}>Remove from Chat</button></div></section>{/if}
    </div>
  </div>
  <div class="feedback-area" aria-live="polite">{#if inference.stage}<div class="feedback"><RefreshCw size={14} />{inference.stage}</div>{/if}{#if inference.error}<div class="feedback error" role="alert">{inference.error}</div>{/if}{#if inference.notice}<div class="feedback">{inference.notice}</div>{/if}{#each inference.issues as issue}<div class="feedback warning">{issue}</div>{/each}{#if !connected}<div class="feedback warning">Engine disconnected. Reconnect to run locally.</div>{/if}</div>
</section>

<style>
.inference-tab{flex:1;min-height:0;overflow:auto;padding:24px;position:relative;user-select:text}.heading,.section-heading,.actions,.badge,.select-row,.promoted{display:flex;align-items:center;gap:10px}.heading{justify-content:space-between;margin-bottom:24px}.heading h2{font-size:22px;font-weight:650}p{font-size:12px;line-height:1.7;color:var(--color-muted);margin:8px 0}h3{font-size:14px;font-weight:650}h4{font-weight:600}.promoted h3,.architecture-result strong{display:flex;gap:6px;align-items:center}.badge{font-size:11px;color:var(--color-muted);border:1px solid var(--color-line);border-radius:20px;padding:5px 10px;white-space:nowrap}.layout{display:grid;grid-template-columns:minmax(240px,310px) minmax(0,1fr);gap:20px;max-width:1240px;margin:auto}aside,.main-column{display:flex;flex-direction:column;gap:18px;min-width:0}.card{border:1px solid var(--color-line);border-radius:12px;padding:20px;background:var(--color-surface);min-width:0}.card>strong{display:block;margin-top:12px}.card>label,label{display:block;font-size:12px;margin:12px 0 6px}.select,.input{width:100%}.select-row select{min-width:0;flex:1}.select-row{margin-bottom:8px}.card>button{margin-top:12px}.runtime-details{font-size:11px;color:var(--color-muted);margin:14px 0}.runtime-details summary{cursor:pointer}.actions{flex-wrap:wrap;margin-top:16px}.status{font-size:11px}.ready{color:var(--color-sage)}.text-button{color:var(--color-accent-strong);display:inline-flex;align-items:center;gap:5px;text-decoration:underline;font-size:12px}.architecture{display:flex;flex-wrap:wrap;align-items:center;gap:12px}.architecture>div:first-child{flex:1;min-width:200px}.architecture-result{width:100%;padding:14px;border:1px solid var(--color-line);border-radius:8px}.architecture-result strong{color:var(--color-sage)}.section-heading{justify-content:space-between;margin-bottom:14px}.empty{text-align:center;padding:34px 20px}.empty :global(svg){margin:auto;color:var(--color-muted)}.empty h4{margin:12px 0}.empty p{max-width:440px;margin:10px auto}.generation-settings,.feature-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px}.primary-actions{border-top:1px solid var(--color-line);padding-top:18px}.prediction{margin-top:24px;background:var(--color-paper);border:1px solid var(--color-line);border-radius:8px;padding:16px}.prediction pre{white-space:pre-wrap;overflow-wrap:anywhere;font-family:inherit;font-size:14px;line-height:1.7;user-select:text}.prediction small{color:var(--color-muted)}.value{font-size:24px}.score{display:flex;align-items:center;gap:10px;margin-top:10px;font-size:12px}.score meter{flex:1}.promoted{justify-content:space-between}.promoted .actions{margin:0}.feedback-area{position:sticky;bottom:0;margin-top:16px}.feedback{display:flex;gap:8px;padding:12px;background:var(--color-paper);border:1px solid var(--color-line);border-radius:8px;margin-top:10px;font-size:12px;line-height:1.7;white-space:pre-wrap;overflow-wrap:anywhere;user-select:text}.error{color:var(--color-rust)}.warning{color:var(--color-amber-warm)}textarea{resize:vertical;min-height:120px}@media(max-width:850px){.inference-tab{padding:16px}.layout{grid-template-columns:1fr}.heading{flex-wrap:wrap}.feature-grid{grid-template-columns:1fr}.promoted{flex-wrap:wrap}}
</style>
