<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import { ArrowRight, Cpu, Play, RefreshCw, Square } from '@lucide/svelte';
  import { app } from '$lib/stores/app.svelte';
  import { trainingMetricEntries, WorkbenchTrainingState } from '$lib/workbench/training.svelte';
  let { projectId, modelId, modelCreatedAt, name, connected, dirty, onBuild, onData, onInference }: { projectId: string; modelId: string; modelCreatedAt: string; name: string; connected: boolean; dirty: boolean; onBuild: () => void; onData: () => void; onInference: (checkpointId: string) => void } = $props();
  const training = new WorkbenchTrainingState((m, p, t) => app.call(m, p, t));
  $effect(() => { const context = { projectId, modelId, modelCreatedAt }, online = connected, unsaved = dirty; untrack(() => { void training.activate(context, online, unsaved); }); });
  onDestroy(() => training.dispose());
  const run = $derived(training.run);
  const history = $derived(run?.history ?? []);
  const losses = $derived(history.flatMap(row => [row.train?.loss, row.validation?.loss]).filter((value): value is number => typeof value === 'number' && Number.isFinite(value)));
  const minLoss = $derived(Math.min(0, ...losses));
  const maxLoss = $derived(Math.max(1e-8, ...losses));
  const lastEpoch = $derived(Math.max(1, ...history.map(row => row.epoch)));
  const x = (epoch: number) => 48 + (epoch - 1) / Math.max(1, lastEpoch - 1) * 570;
  const y = (loss: number) => 185 - (loss - minLoss) / Math.max(1e-8, maxLoss - minLoss) * 155;
  function points(split: 'train' | 'validation') { return history.filter(row => typeof row[split]?.loss === 'number' && Number.isFinite(row[split]!.loss)).map(row => `${x(row.epoch)},${y(row[split]!.loss)}`).join(' '); }
  const splitCards = $derived(run?.status === 'completed' ? [{ label: 'Training', metrics: run.train }, { label: 'Validation', metrics: run.validation }, { label: 'Held-out test', metrics: run.test }] : []);
</script>

<section class="train-tab" data-train-tab aria-label="Local model training">
  <header class="heading"><div><h2>Train {name}</h2><p>Learn from saved Data splits and publish a checkpoint for Inference.</p></div><span class="badge"><Cpu size={14} />Local only · CPU</span></header>
  {#if dirty}<div class="feedback warning">Save your Build changes before starting training. <button class="text-button" onclick={onBuild}>Review Build <ArrowRight size={12} /></button></div>{/if}
  {#if !connected}<div class="feedback warning">Engine disconnected. Reconnect to view progress or start training.</div>{/if}
  <div class="layout">
    <aside>
      <section class="card">
        <h3>Training settings</h3><p>Runs start with fresh weights and pin the saved architecture and preparation. Only one local job can run at a time.</p>
        <div class="settings">
          <label>Epochs<input class="input" type="number" min="1" max="1000" step="1" bind:value={training.config.epochs} disabled={training.busy || !!training.activeRun} /></label>
          <label>Batch size<input class="input" type="number" min="1" max="256" step="1" bind:value={training.config.batchSize} disabled={training.busy || !!training.activeRun} /></label>
          <label>Learning rate<input class="input" type="number" min="0.000001" max="1" step="any" bind:value={training.config.learningRate} disabled={training.busy || !!training.activeRun} /></label>
          <label>Seed<input class="input" type="number" min="0" max="2147483647" step="1" bind:value={training.config.seed} disabled={training.busy || !!training.activeRun} /></label>
        </div>
        {#if training.configError()}<p class="warning">{training.configError()}</p>{/if}
        <div class="actions"><button class="btn-primary" disabled={!training.canStart} onclick={() => void training.start()}><Play size={14} />Start training</button>{#if training.activeRun}<button class="btn-outline" disabled={!connected || training.busy} onclick={() => void training.stop()}><Square size={13} />Stop</button>{/if}</div>
        <p>AdamW optimizer · numeric and text tasks. Image training is not available in this release.</p>
      </section>
      <section class="card"><h3>Prepare your data</h3><p>Choose a dataset, save the preparation in Data, and keep train, validation, and test rows separate. Evaluation uses only the saved held-out splits.</p><button class="text-button" onclick={onData}>Review Data preparation <ArrowRight size={12} /></button></section>
      <section class="card">
        <h3>Run history</h3><div class="select-row"><select class="select" aria-label="Training run" value={training.runId} onchange={(e) => training.selectRun(e.currentTarget.value)}><option value="">Choose a run</option>{#each training.runs as item}<option value={item.id}>{item.name} · {item.status}</option>{/each}</select><button class="icon-button" aria-label="Refresh training runs" disabled={!connected || training.busy} onclick={() => void training.refresh()}><RefreshCw size={14} /></button></div>
        <p>Runs and checkpoints are saved in this project. Training continues when you switch tabs.</p>
        {#if training.activeRun && training.activeRun.id !== run?.id}<p class="warning">{training.activeRun.name} is still training. <button class="text-button" onclick={() => training.selectRun(training.activeRun!.id)}>View active run</button></p>{/if}
      </section>
    </aside>
    <div class="main-column">
      {#if run}
        <section class="card">
          <div class="section-heading"><h3>{run.name}</h3><span class="badge" class:completed={run.status === 'completed'}>{run.status}</span></div>
          <div class="progress-heading"><strong>Epoch {run.epoch} / {run.config.epochs}</strong><small>{(run.durationMs / 1000).toFixed(1)} s{run.parameterCount !== undefined ? ` · ${run.parameterCount.toLocaleString()} parameters` : ''}</small></div>
          <progress max={run.config.epochs} value={run.epoch} aria-label="Training epochs"></progress>
          <p>Started {new Date(run.createdAt).toLocaleString()} · preparation {run.preparationVersion}</p>
          <p>{run.config.batchSize} batch size · {run.config.learningRate} learning rate · seed {run.config.seed}</p>
          {#if run.error}<div class="feedback error" role="alert">{run.error}</div>{/if}
          {#if run.status === 'cancelled'}<p>Training stopped. Incomplete weights were not published as a checkpoint.</p>{/if}
          {#if run.status === 'completed' && run.checkpointId}<div class="checkpoint"><div><strong>Checkpoint ready</strong><p>Test learned predictions with this run’s saved architecture and preparation.</p></div><button class="btn-primary" onclick={() => onInference(run.checkpointId!)}>Try in Inference <ArrowRight size={14} /></button></div>{/if}
        </section>
        <section class="card loss-card">
          <div class="section-heading"><h3>Epoch loss</h3><div class="legend"><span class="train-key">Training</span><span class="validation-key">Validation</span></div></div>
          {#if losses.length}
            <svg viewBox="0 0 640 220" role="img" aria-label="Training and validation loss by epoch">
              <line x1="48" y1="30" x2="48" y2="185" class="axis" /><line x1="48" y1="185" x2="618" y2="185" class="axis" />
              <text x="40" y="34" text-anchor="end">{maxLoss.toPrecision(3)}</text><text x="40" y="188" text-anchor="end">{minLoss.toPrecision(2)}</text><text x="48" y="209">1</text><text x="618" y="209" text-anchor="end">Epoch {lastEpoch}</text>
              <polyline points={points('train')} class="train-line" /><polyline points={points('validation')} class="validation-line" />
              {#each history as row}{#if Number.isFinite(row.train?.loss)}<circle cx={x(row.epoch)} cy={y(row.train.loss)} r="3" class="train-dot"><title>Epoch {row.epoch}: training loss {row.train.loss}</title></circle>{/if}{#if row.validation && Number.isFinite(row.validation.loss)}<circle cx={x(row.epoch)} cy={y(row.validation.loss)} r="3" class="validation-dot"><title>Epoch {row.epoch}: validation loss {row.validation.loss}</title></circle>{/if}{/each}
            </svg>
            {#if !history.some(row => row.validation)}<p>No validation rows available. Only training loss is shown.</p>{/if}
            <details><summary>Epoch values</summary><div class="table-scroll"><table><thead><tr><th>Epoch</th><th>Training loss</th><th>Validation loss</th></tr></thead><tbody>{#each history as row}<tr><td>{row.epoch}</td><td>{row.train.loss.toFixed(4)}</td><td>{row.validation ? row.validation.loss.toFixed(4) : 'Unavailable'}</td></tr>{/each}</tbody></table></div></details>
          {:else}<div class="empty"><h4>Waiting for the first epoch</h4><p>Live training and validation loss will appear when the local worker reports an epoch.</p></div>{/if}
        </section>
        {#if run.status === 'completed'}
          <section class="card"><h3>Final evaluation</h3><p>Validation and test metrics use separate held-out rows. Lower loss, MAE, RMSE and perplexity are better; higher R², accuracy, macro F1 and token accuracy are better.</p><div class="evaluation">{#each splitCards as split}<section class="split"><h4>{split.label}</h4>{#if split.metrics}<dl>{#each trainingMetricEntries(split.metrics) as metric}<div><dt>{metric.label}</dt><dd>{metric.value}</dd></div>{/each}</dl>{:else}<p>No {split.label.toLowerCase()} evaluation rows available. Metrics are unavailable.</p>{/if}</section>{/each}</div></section>
        {:else if run.status === 'running'}<section class="card"><h3>Held-out evaluation</h3><p>Final validation and test metrics appear after training completes. Test rows are never used to update model weights.</p></section>{/if}
      {:else}<section class="card empty"><Cpu size={30} /><h3>Ready for a first local run</h3><p>Save your Build and Data preparation, choose training settings, then start. Completed runs automatically create an Inference checkpoint.</p><button class="btn-outline" onclick={onData}>Review saved Data <ArrowRight size={14} /></button></section>{/if}
    </div>
  </div>
  <div class="feedback-area" aria-live="polite">{#if training.stage}<div class="feedback"><RefreshCw size={14} />{training.stage}</div>{/if}{#if training.error}<div class="feedback error" role="alert">{training.error}</div>{/if}{#each training.issues as issue}<div class="feedback warning">{issue}</div>{/each}{#if connected && !training.runtimeAvailable && !training.busy}<div class="feedback warning">The local PyTorch CPU runtime is unavailable. Install the configured PyTorch runtime, then refresh training runs.</div>{/if}</div>
</section>

<style>
.train-tab{flex:1;min-height:0;overflow:auto;padding:24px;user-select:text}.heading,.section-heading,.progress-heading,.actions,.badge,.select-row,.checkpoint,.legend{display:flex;align-items:center;gap:10px}.heading,.section-heading,.progress-heading,.checkpoint{justify-content:space-between}.heading{margin-bottom:24px}.heading h2{font-size:22px;font-weight:650}h3{font-size:14px;font-weight:650}h4{font-size:13px;font-weight:600}p{font-size:12px;line-height:1.7;color:var(--color-muted);margin:8px 0}.badge{border:1px solid var(--color-line);border-radius:20px;padding:5px 10px;font-size:11px;color:var(--color-muted);white-space:nowrap}.completed{color:var(--color-sage)}.layout{display:grid;grid-template-columns:minmax(240px,310px) minmax(0,1fr);gap:20px;max-width:1240px;margin:20px auto}aside,.main-column{display:flex;flex-direction:column;gap:18px;min-width:0}.card{border:1px solid var(--color-line);border-radius:12px;padding:20px;background:var(--color-surface);min-width:0}.settings{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px}label{display:block;font-size:12px;margin:10px 0 0}input{width:100%;margin-top:6px}.actions{flex-wrap:wrap;margin-top:20px}.text-button{display:inline-flex;align-items:center;gap:5px;color:var(--color-accent-strong);text-decoration:underline;font-size:12px}.select-row{margin-top:14px}.select-row select{min-width:0;flex:1}.progress-heading{margin:20px 0 10px;font-size:13px}.progress-heading small{font-size:11px;color:var(--color-muted)}progress{width:100%;height:8px;accent-color:var(--color-sage)}.checkpoint{border-top:1px solid var(--color-line);padding-top:18px;margin-top:18px;flex-wrap:wrap}.checkpoint strong{font-size:13px;color:var(--color-sage)}.legend{font-size:11px}.train-key{color:var(--color-sage)}.validation-key{color:var(--color-accent-strong)}.loss-card svg{display:block;width:100%;height:auto;margin:16px 0}.axis{stroke:var(--color-line)}svg text{fill:var(--color-muted);font-size:10px}.train-line,.validation-line{fill:none;stroke-width:2}.train-line{stroke:var(--color-sage)}.validation-line{stroke:var(--color-accent-strong);stroke-dasharray:5 3}.train-dot{fill:var(--color-sage)}.validation-dot{fill:var(--color-accent-strong)}details{font-size:12px;color:var(--color-muted)}summary{cursor:pointer}.table-scroll{overflow:auto}table{width:100%;border-collapse:collapse;margin-top:10px;text-align:left}td,th{padding:8px;border-bottom:1px solid var(--color-line)}.evaluation{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin-top:18px}.split{border:1px solid var(--color-line);border-radius:8px;padding:14px;min-width:0}dl{margin-top:12px}dl>div{display:flex;justify-content:space-between;gap:8px;padding:6px 0;font-size:12px;flex-wrap:wrap}dt{color:var(--color-muted)}dd{font-variant-numeric:tabular-nums}.empty{text-align:center;padding:40px 24px}.empty :global(svg){margin:0 auto 14px;color:var(--color-muted)}.empty p{max-width:440px;margin:12px auto 20px}.feedback-area{margin-top:16px}.feedback{display:flex;gap:8px;flex-wrap:wrap;padding:12px;background:var(--color-paper);border:1px solid var(--color-line);border-radius:8px;margin-top:10px;font-size:12px;line-height:1.7;white-space:pre-wrap;overflow-wrap:anywhere}.error{color:var(--color-rust)}.warning{color:var(--color-amber-warm)}@media(max-width:900px){.train-tab{padding:16px}.layout{grid-template-columns:1fr}.heading{flex-wrap:wrap}.evaluation{grid-template-columns:1fr}}
</style>
