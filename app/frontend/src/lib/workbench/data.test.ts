import { describe, expect, it } from 'vitest';
import { dataCompatibility, recommendConfig, validateConfig, validateUploads } from './data';
import { starterGraph } from './graph';
import type { Dataset } from './dataTypes';

const table: Dataset = {
  id: 'table-one', name: 'Campaign', source: { kind: 'local', name: 'campaign.csv' }, format: 'table',
  rowCount: 10, byteCount: 100, createdAt: '2026-10-31T00:00:00Z', preview: [{ customer_id: '1', age: '40', income: '100', purchased: 'yes' }],
  columns: [
    { name: 'customer_id', kind: 'number', missing: 0, uniqueCount: 10, examples: ['1'] },
    { name: 'age', kind: 'number', missing: 0, uniqueCount: 5, examples: ['40'] },
    { name: 'income', kind: 'number', missing: 0, uniqueCount: 10, examples: ['100'] },
    { name: 'purchased', kind: 'text', missing: 0, uniqueCount: 2, examples: ['yes', 'no'] },
  ],
};

describe('real dataset mapping', () => {
  it('recommends numeric features while excluding ID columns and the target', () => {
    const config = recommendConfig(table, 'classification');
    expect(config.inputColumns).toEqual(['age', 'income']);
    expect(config.targetColumn).toBe('purchased');
    expect(config.mode).toBe('features');
    expect(validateConfig(table, config)).toEqual([]);
    expect(validateConfig(table, { ...config, inputColumns: ['age', 'purchased'] }).join(' ')).toContain('numeric');
    expect(validateConfig(table, { ...config, inputColumns: ['age', 'purchased'] }).join(' ')).toContain('target');
  });
  it('uses real text columns and allows an unlabelled text generation corpus', () => {
    const dataset: Dataset = { ...table, format: 'text', columns: [{ name: 'text', kind: 'text', missing: 0, uniqueCount: 10, examples: ['hello'] }] };
    const config = recommendConfig(dataset, 'text-generation');
    expect(config.inputColumns).toEqual(['text']); expect(config.targetColumn).toBe('');
    expect(validateConfig(dataset, config)).toEqual([]);
    expect(validateConfig(dataset, { ...config, trainPercent: 90 }).join(' ')).toContain('100');
  });
  it('rejects sequence lengths that the local preparation engine cannot accept', () => {
    const text: Dataset = { ...table, format: 'text', columns: [{ name: 'text', kind: 'text', missing: 0, uniqueCount: 10, examples: ['hello'] }] };
    const config = recommendConfig(text, 'text-generation');
    expect(validateConfig(text, { ...config, sequenceLength: 2 }).join(' ')).toContain('Sequence');
    expect(validateConfig(text, { ...config, sequenceLength: 8193 }).join(' ')).toContain('Sequence');
    expect(validateConfig(text, { ...config, sequenceLength: 8192 })).toEqual([]);
  });
  it('validates real upload limits before reading any file bytes', () => {
    expect(validateUploads([{ name: 'data.csv', size: 16 * 1024 * 1024 + 1 }])).toContain('16 MiB');
    expect(validateUploads(Array.from({ length: 1001 }, () => ({ name: 'x.csv', size: 1 })))).toContain('1,000');
    expect(validateUploads([{ name: 'data.parquet', size: 1 }])).toContain('supported');
    expect(validateUploads([{ name: 'data.csv', size: 1 }, { name: 'cat/a.png', size: 1 }])).toContain('separately');
    expect(validateUploads([{ name: 'cat/a.png', size: 1 }, { name: 'dog/b.jpg', size: 1 }])).toBe('');
  });
});

describe('current graph and data compatibility', () => {
  it('checks numeric width and class width against the current graph', () => {
    const config = recommendConfig(table, 'classification'); const graph = starterGraph('mlp');
    const issues = dataCompatibility(JSON.stringify(graph), table, config);
    expect(issues.join(' ')).toContain('2 numeric'); expect(issues.join(' ')).toContain('2 classes');
    graph.nodes[0].config.features = 2; graph.nodes.at(-2)!.config.outFeatures = 2;
    expect(dataCompatibility(JSON.stringify(graph), table, config)).toEqual([]);
  });
  it('detects byte-tokenizer, sequence and output mismatches without modifying the graph', () => {
    const graph = starterGraph('text'); const original = JSON.stringify(graph);
    const config = { ...recommendConfig(table, 'text-generation'), sequenceLength: 16 };
    const issues = dataCompatibility(original, table, config).join(' ');
    expect(issues).toContain('259'); expect(issues).toContain('sequence'); expect(JSON.stringify(graph)).toBe(original);
  });
  it('checks image dimensions and graph validity before showing compatibility', () => {
    const image: Dataset = { ...table, format: 'images', columns: [{ name: 'image', kind: 'image', missing: 0, uniqueCount: 10, examples: ['cats/a.png'] }, { name: 'label', kind: 'text', missing: 0, uniqueCount: 2, examples: ['cats', 'dogs'] }] };
    const config = { ...recommendConfig(image, 'classification'), imageWidth: 64 };
    expect(dataCompatibility(JSON.stringify(starterGraph('cnn')), image, config).join(' ')).toContain('64');
    expect(dataCompatibility('invalid', image, config).join(' ')).toContain('Build');
  });
});

describe('provided example mappings', () => {
  it('uses campaign engagement without leaking alternate target encodings and respects graph width', () => {
    const numeric = (name: string) => ({ name, kind: 'number' as const, missing: 0, uniqueCount: 2, examples: ['0', '1'] });
    const campaign: Dataset = { ...table, source: { kind: 'example', name: 'Campaign', repo: 'nprak26/insurance-campaign-data' }, columns: [numeric('customer_id'), numeric('age'), numeric('income'), numeric('visits'), numeric('campaign_engagement'), numeric('engagement_level_encoded')] };
    const graph = starterGraph('mlp'); graph.nodes[0].config.features = 2;
    const config = recommendConfig(campaign, 'classification', JSON.stringify(graph));
    expect(config.targetColumn).toBe('campaign_engagement'); expect(config.inputColumns).toEqual(['age', 'income']);
  });
  it('prefers DMV instruction over its often empty context field', () => {
    const text = (name: string) => ({ name, kind: 'text' as const, missing: 0, uniqueCount: 4, examples: ['hello'] });
    const dmv: Dataset = { ...table, source: { kind: 'example', name: 'DMV', repo: 'nprak26/MultiState-DMV-Licensing-Practice-Set' }, columns: [text('context'), text('instruction'), text('response')] };
    const config = recommendConfig(dmv, 'text-generation', JSON.stringify(starterGraph('text')));
    expect(config.targetColumn).toBe('response'); expect(config.inputColumns).toEqual(['instruction']);
    expect(config.sequenceLength).toBe(256);
  });
  it('recommends remote-worker regression only for custom models and surfaces incomplete numeric data', async () => {
    const { dataMappingWarnings } = await import('./data');
    const remote: Dataset = { ...table, source: { kind: 'example', name: 'Remote', repo: 'nprak26/remote-worker-productivity' }, columns: [...table.columns.slice(0, 3), { name: 'Productivity_Score', kind: 'number', missing: 0, uniqueCount: 8, examples: ['40'] }] };
    expect(recommendConfig(remote, 'custom').purpose).toBe('regression');
    expect(recommendConfig(remote, 'classification').purpose).toBe('classification');
    const config = recommendConfig(remote, 'custom'); expect(config.targetColumn).toBe('Productivity_Score');
    remote.columns[1].missing = 2;
    expect(dataMappingWarnings(remote, config).join(' ')).toContain('age');
    expect(dataMappingWarnings(remote, config).join(' ')).toContain('missing');
  });
});
