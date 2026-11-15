<script lang="ts">
 import type {DataSelection} from '$lib/workbench/dataTypes';
 let {selection,datasetName,loading=false,error='',title='Training dataset · saved Data',onData}:{selection:DataSelection|null;datasetName:string;loading?:boolean;error?:string;title?:string;onData:()=>void}=$props();
</script>
<section aria-label="Training dataset" class="training-data">
 <h3>{title}</h3>
 {#if error}<p role="alert">{error}</p>
 {:else if loading}<p role="status">Loading saved preparation…</p>
 {:else if selection}
  <strong>{datasetName || selection.datasetId}</strong>
  <p>{selection.counts.train.toLocaleString()} train · {selection.counts.validation.toLocaleString()} validation · {selection.counts.test.toLocaleString()} test rows</p>
  <p>Input: <b>{selection.config.inputColumns.join(', ')}</b> · {selection.config.purpose==='text-generation'?'Response':'Target'}: <b>{selection.config.targetColumn || 'None · text corpus'}</b></p>
  {#if selection.config.mode==='text'}<p>Sequence length: {selection.config.sequenceLength} tokens · {selection.config.purpose==='text-generation'?(selection.config.targetColumn?'Question / answer training':'Text continuation training'):'Text classification'}</p>{/if}
  <p class="note">Preparation {selection.version.slice(0,12)} · Only saved preparation is used; unsaved Data changes are not included.</p>
 {:else}<p>No saved training dataset. Choose a dataset in Data and click Save preparation first.</p>{/if}
 <button class="btn-outline btn-sm" onclick={onData}>Review Data</button>
</section>
<style>
 .training-data{padding:16px;margin:16px 0;border:1px solid var(--color-line);border-radius:8px;overflow-wrap:anywhere}h3{font-size:14px;font-weight:600;margin-bottom:8px}strong{font-size:14px}p{font-size:12px;line-height:1.6;color:var(--color-muted);margin:8px 0}.note{font-size:11px}button{margin-top:4px}
</style>
