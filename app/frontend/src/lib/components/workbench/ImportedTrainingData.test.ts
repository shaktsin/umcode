import {describe, it, expect} from 'vitest';
import {render} from 'svelte/server';
import ImportedTrainingData from './ImportedTrainingData.svelte';
import type {DataSelection} from '$lib/workbench/dataTypes';

describe('imported training dataset summary', () => {
 it('shows saved mappings and split counts, not a browsing selection', () => {
  const selection = {datasetId:'saved', version:'v1', createdAt:'2026-11-11', counts:{train:80,validation:10,test:10}, config:{mode:'text',purpose:'text-generation',inputColumns:['question'],targetColumn:'answer',sequenceLength:128}} as DataSelection;
  const html=render(ImportedTrainingData,{props:{selection,datasetName:'DMV.csv',onData(){}}}).body;
  for(const value of ['DMV.csv','question','answer','80 train','10 validation','10 test','128']) expect(html).toContain(value);
 });
 it('explains why there is no training dataset before preparation is saved', () => {
  const html=render(ImportedTrainingData,{props:{selection:null,datasetName:'',onData(){}}}).body;
  expect(html).toContain('No saved training dataset');
  expect(html).toContain('Review Data');
 });
});
