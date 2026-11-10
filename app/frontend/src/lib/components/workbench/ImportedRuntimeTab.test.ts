import { describe, expect, it } from 'vitest';
import { render } from 'svelte/server';
import ImportedRuntimeTab from './ImportedRuntimeTab.svelte';

const props = {
  projectId: 'project', modelId: 'model', modelCreatedAt: 'created',
  graphText: '', task: 'classification' as const, connected: true, dirty: false,
  onBuild() {}, onData() {}, onInference() {},
};

describe('imported model setup ownership', () => {
  it('offers model-folder setup in Build without blocking architecture inspection', () => {
    const { body } = render(ImportedRuntimeTab, { props: { ...props, tab: 'Build' } });
    expect(body).toContain('Add complete model folder');
    expect(body).toContain('Validate local model');
    expect(body).toContain('webkitdirectory');
    expect(body).not.toContain('Build is not ready yet');
  });

  it.each(['Data', 'Train', 'Inference'])('%s links to Build instead of accepting another model folder', (tab) => {
    const { body } = render(ImportedRuntimeTab, { props: { ...props, tab } });
    expect(body).toContain('Go to Build');
    expect(body).not.toContain('Add complete model folder');
    expect(body).not.toContain('Validate local model');
    expect(body).not.toContain('webkitdirectory');
  });
});
