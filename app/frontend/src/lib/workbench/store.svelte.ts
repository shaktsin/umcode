import { app } from '$lib/stores/app.svelte';
import { WorkbenchState } from './state.svelte';

// Access lazily: some WebViews and private sessions deny localStorage entirely.
export const workbench = new WorkbenchState((method, params) => app.call(method, params), {
  getItem: key => localStorage.getItem(key),
  setItem: (key, value) => localStorage.setItem(key, value),
  removeItem: key => localStorage.removeItem(key),
});
