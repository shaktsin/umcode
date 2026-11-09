import { analyzeGraph, readGraph } from './graph';
import { isImportedGraph, parseImportedGraph } from './modelImport';
import type { DataConfig, DataSelection, Dataset, DataSource } from './dataTypes';
import type { WorkbenchTask } from './types';

export const dataExamples: { name: string; description: string; source: DataSource }[] = [
  { name: 'Insurance campaign', description: 'Classification · customer features and campaign engagement labels.', source: { kind: 'example', name: 'Insurance campaign', repo: 'nprak26/insurance-campaign-data', revision: 'fa455cd3d5b7a65c5cdd55cd5480eca230889af1', files: ['insurance_campaign_data_original_20250720_140142.csv'] } },
  { name: 'DMV practice questions', description: 'Text generation · instruction and response pairs for licensing practice.', source: { kind: 'example', name: 'DMV practice questions', repo: 'nprak26/MultiState-DMV-Licensing-Practice-Set', revision: '0788b0074a4fedf803c1a4b9440148e99a8c58d7', files: ['dmv_practice_QA_1.csv', 'dmv_practice_QA_2.csv', 'dmv_practice_QA_3.csv', 'dmv_practice_QA_4.csv'] } },
  { name: 'Remote worker productivity', description: 'Regression · numeric productivity scores. Use a Custom model for regression.', source: { kind: 'example', name: 'Remote worker productivity', repo: 'nprak26/remote-worker-productivity', revision: 'ffca2152b8313c503f460b04d804e904ca3e6734', files: ['remote_work_productivity.csv'] } },
];
const isId = (name: string) => /(^|[_\s-])(id|uuid|index)$|^(id|uuid|index)$|[a-z](ID|Id)$/.test(name) || /(^|[_\s-])(id|uuid|index)$/i.test(name);
const normalized = (name: string) => name.toLowerCase().replace(/[_\s-]/g, '');
function targetAlias(dataset: Dataset, column: string, target: string) {
  const base = (value: string) => normalized(value).replace(/encoded$/, '');
  if (base(column) === base(target)) return true;
  return dataset.source.repo === 'nprak26/insurance-campaign-data' && /^(campaignengagement|engagementlevel)$/.test(base(target)) && /^(campaignengagement|engagementlevel)$/.test(base(column));
}

export function recommendConfig(dataset: Dataset, task: WorkbenchTask, graphText = ''): DataConfig {
  let graphInput;
  try { graphInput = readGraph(graphText).nodes.find(n => n.kind === 'input'); } catch { /* Mapping also works before an architecture is created. */ }
  const remote = dataset.source.repo === 'nprak26/remote-worker-productivity';
  const purpose = task === 'text-generation' ? 'text-generation' : task === 'custom' && remote ? 'regression' : 'classification';
  const importedFamily = isImportedGraph(graphText) ? parseImportedGraph(graphText).source.family : '';
  const mode = importedFamily ? (['vit','resnet','convnext'].includes(importedFamily) ? 'image' : 'text') : dataset.format === 'images' ? 'image' : task === 'text-generation' || dataset.format === 'text' || !dataset.columns.some(c => c.kind === 'number' && !isId(c.name)) ? 'text' : 'features';
  const knownTarget = dataset.source.repo === 'nprak26/insurance-campaign-data' ? 'campaignengagement' : remote ? 'productivityscore' : dataset.source.repo === 'nprak26/MultiState-DMV-Licensing-Practice-Set' ? 'response' : '';
  const target = dataset.columns.find(c => knownTarget && normalized(c.name) === knownTarget) ?? dataset.columns.find(c => /^(label|target|class|category|y)$/i.test(c.name)) ?? (purpose === 'text-generation' ? dataset.columns.find(c => /^(answer|response|completion)$/i.test(c.name)) : [...dataset.columns].reverse().find(c => c.kind !== 'image' && !isId(c.name)));
  const inputs = dataset.columns.filter(c => (!target || !targetAlias(dataset, c.name, target.name)) && !isId(c.name) && c.kind === (mode === 'features' ? 'number' : mode === 'image' ? 'image' : 'text'));
  const text = inputs.find(c => /^(instruction|prompt|question|text|content|input)$/i.test(c.name)) ?? inputs[0];
  const features = [...inputs].sort((a, b) => a.missing - b.missing).map(c => c.name);
  const featureCount = graphInput?.config.mode === 'features' ? Number(graphInput.config.features) : features.length;
  return {
    datasetId: dataset.id, mode, purpose, inputColumns: mode === 'features' ? features.slice(0, featureCount) : (mode === 'text' ? text : inputs[0]) ? [(mode === 'text' ? text : inputs[0])!.name] : [],
    targetColumn: target?.name ?? '', scale: 'none', trainPercent: 80, validationPercent: 10, testPercent: 10, seed: 42, stratify: purpose === 'classification',
    sequenceLength: dataset.source.repo === 'nprak26/MultiState-DMV-Licensing-Practice-Set' ? Math.max(256, Math.min(8192, Number(graphInput?.config.sequence) || 256)) : Math.min(8192, Math.max(3, Number(graphInput?.config.sequence) || 128)), imageWidth: Number(graphInput?.config.width) || 32, imageHeight: Number(graphInput?.config.height) || 32, imageChannels: Number(graphInput?.config.channels) || 3,
  };
}

/** Suggestions do not silently drop user-selected fields or alter their architecture. */
export function dataMappingWarnings(dataset: Dataset, config: DataConfig): string[] {
  const warnings: string[] = [];
  for (const name of config.inputColumns) {
    const column = dataset.columns.find(c => c.name === name);
    if (column?.kind === 'number' && column.missing) warnings.push(`${name} has ${column.missing} missing numeric values. Choose complete inputs or clean and reimport the data.`);
    if (isId(name)) warnings.push(`${name} looks like an identifier. Exclude IDs unless they are meaningful prediction features.`);
    if (config.targetColumn && targetAlias(dataset, name, config.targetColumn)) warnings.push(`${name} is an alternative target encoding and could leak the label into the inputs.`);
  }
  if (dataset.source.repo === 'nprak26/remote-worker-productivity' && config.purpose === 'classification') warnings.push('Productivity_Score is a numeric regression target. Create a Custom model for regression, or explicitly choose a categorical classification target.');
  return warnings;
}

export function validateUploads(files: { name: string; size: number }[]): string {
  if (!files.length) return 'Choose at least one supported file.';
  if (files.length > 1000) return 'Import at most 1,000 files at once.';
  if (files.reduce((total, file) => total + file.size, 0) > 16 * 1024 * 1024) return 'The combined upload exceeds 16 MiB.';
  if (files.some(file => !/\.(csv|tsv|jsonl|txt|png|jpe?g)$/i.test(file.name))) return 'Choose supported CSV, TSV, JSONL, TXT, PNG or JPEG files. Archives, Parquet and dataset scripts are unsupported.';
  const images = files.filter(file => /\.(png|jpe?g)$/i.test(file.name)).length;
  if (images && images !== files.length) return 'Import image folders separately from tables and text.';
  return '';
}

export function validateConfig(dataset: Dataset, config: DataConfig): string[] {
  const issues: string[] = [];
  if (config.datasetId !== dataset.id) issues.push('Select the imported dataset again.');
  if (!config.inputColumns.length) issues.push('Choose at least one input column.');
  if (new Set(config.inputColumns).size !== config.inputColumns.length) issues.push('Input columns must be distinct.');
  if (config.inputColumns.includes(config.targetColumn)) issues.push('The target must be excluded from input columns.');
  const expected = config.mode === 'features' ? 'number' : config.mode === 'image' ? 'image' : 'text';
  if (config.inputColumns.some(name => dataset.columns.find(c => c.name === name)?.kind !== expected)) issues.push(`Choose ${config.mode === 'features' ? 'numeric' : expected} input columns.`);
  if (config.mode !== 'features' && config.inputColumns.length > 1) issues.push('Choose one input column for text or images.');
  const target = dataset.columns.find(c => c.name === config.targetColumn);
  if (config.purpose !== 'text-generation' && !target) issues.push('Choose a target column.');
  if (config.targetColumn && !target) issues.push('The target column is missing from this dataset.');
  if (config.purpose === 'regression' && target?.kind !== 'number') issues.push('Regression needs a numeric target column.');
  if (config.purpose === 'text-generation' && config.mode !== 'text') issues.push('Text generation needs a text input.');
  const splits = [config.trainPercent, config.validationPercent, config.testPercent];
  if (splits.some(n => !Number.isInteger(n) || n < 0 || n > 100) || splits.reduce((a, b) => a + b, 0) !== 100 || config.trainPercent === 0) issues.push('Split percentages must total 100 with a nonzero training split.');
  if (!Number.isSafeInteger(config.seed) || config.seed < 0) issues.push('Use a nonnegative whole number for the split seed.');
  if (config.mode === 'text' && (!Number.isInteger(config.sequenceLength) || config.sequenceLength < 3 || config.sequenceLength > 8192)) issues.push('Sequence length must be a whole number between 3 and 8,192.');
  if (config.mode === 'image' && (![config.imageWidth, config.imageHeight].every(n => Number.isInteger(n) && n > 0 && n <= 4096) || ![1, 3].includes(config.imageChannels))) issues.push('Images need dimensions from 1 to 4,096 and 1 or 3 channels.');
  return issues;
}

/** Compare the current graph only. Never edits the graph or claims training is implemented. */
export function dataCompatibility(graphText: string, dataset: Dataset, config: DataConfig, selection: DataSelection | null = null): string[] {
  const issues: string[] = [];
  try {
    if (isImportedGraph(graphText)) {
      const source = parseImportedGraph(graphText).source;
      const image = ['resnet','convnext','vit'].includes(source.family);
      if (config.mode !== (image ? 'image' : 'text')) issues.push(image ? 'This imported model needs image inputs.' : 'This imported model needs text inputs.');
      if (config.mode === 'text' && config.sequenceLength > 2048) issues.push('Local imported-model fine-tuning supports sequences up to 2,048 tokens.');
      return issues;
    }
    const graph = readGraph(graphText), analysis = analyzeGraph(graph);
    if (analysis.errors.length) issues.push(...analysis.errors.slice(0, 3).map(e => `Build: ${e.message}`));
    const input = analysis.order.find(n => n.kind === 'input');
    if (!input) return issues.length ? issues : ['Add an Input layer in Build.'];
    const expectedMode = config.mode === 'text' ? 'tokens' : config.mode;
    if (input.config.mode !== expectedMode) issues.push(`Build Input uses ${input.config.mode}; this preparation needs ${expectedMode}.`);
    if (config.mode === 'features' && input.config.features !== config.inputColumns.length) issues.push(`Build Input needs ${config.inputColumns.length} numeric features; it currently has ${input.config.features}.`);
    if (config.mode === 'image' && (input.config.width !== config.imageWidth || input.config.height !== config.imageHeight || input.config.channels !== config.imageChannels)) issues.push(`Build Input needs image dimensions ${config.imageChannels} × ${config.imageHeight} × ${config.imageWidth} (channels × height × width).`);
    if (config.mode === 'text') {
      if (input.config.sequence !== config.sequenceLength) issues.push(`Build Input sequence length is ${input.config.sequence}; preparation uses ${config.sequenceLength}.`);
      const embedding = analysis.order.find(n => n.kind === 'embedding');
      if (!embedding || embedding.config.vocab !== 259) issues.push('The UTF-8 byte tokenizer needs an Embedding vocabulary of 259 (PAD, BOS, EOS and 256 bytes).');
    }
    const output = analysis.order.find(n => n.kind === 'output'), dims = output && analysis.shapes[output.id]?.dims;
    const classes = selection?.datasetId === dataset.id && selection.config.targetColumn === config.targetColumn ? selection.classes?.length : dataset.columns.find(c => c.name === config.targetColumn)?.uniqueCount;
    if (dims) {
      if (config.purpose === 'classification' && classes && (dims.length !== 2 || dims.at(-1) !== classes)) issues.push(`Build Output needs [batch, ${classes}] for ${classes} classes; it currently has [${dims.join(', ')}].`);
      if (config.purpose === 'regression' && (dims.length !== 2 || dims.at(-1) !== 1)) issues.push('Regression needs Build Output shape [batch, 1].');
      if (config.purpose === 'text-generation' && (dims.length !== 3 || dims.at(-1) !== 259)) issues.push('Text generation with the byte tokenizer needs Build Output shape [batch, sequence, 259].');
    }
    return issues;
  } catch { return ['Open Build to review this architecture before checking data compatibility.']; }
}

export const dataSize = (bytes: number) => bytes >= 1024 * 1024 ? `${(bytes / (1024 * 1024)).toFixed(1)} MiB` : bytes >= 1024 ? `${(bytes / 1024).toFixed(1)} KiB` : `${bytes} bytes`;
