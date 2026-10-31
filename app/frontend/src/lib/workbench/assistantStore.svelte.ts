import { workbench } from './store.svelte';
import { WorkbenchAssistantState } from './assistant.svelte';

export const workbenchAssistant = new WorkbenchAssistantState(() => {
  const model = workbench.selected?.model;
  return model && workbench.projectId ? { projectId: workbench.projectId, modelId: model.id, modelName: model.name, text: workbench.draft } : null;
});
