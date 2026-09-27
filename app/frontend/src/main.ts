import './app.css';
import { mount } from 'svelte';
import App from './App.svelte';
import MiniOverlay from '$lib/mini/MiniOverlay.svelte';
import { app } from '$lib/stores/app.svelte';
import { chat } from '$lib/stores/chat.svelte';
import { projects } from '$lib/stores/projects.svelte';
import { inspector } from '$lib/stores/inspector.svelte';

// The mini overlay is the same bundle loaded in a second, small Wails window
// (see app/mini.go), told apart only by this query flag.
const isMini = new URLSearchParams(location.search).get('mini') === '1';
if (isMini) document.body.classList.add('mini');

// Hooks the Go shell calls (window.ExecJS) from the menu bar and notifications.
declare global {
  interface Window {
    umcode: {
      newChat: () => void;
      show: (view: string) => void;
      openThread: (id: string) => void;
      openProject: (id: string) => void;
      openFile: (path: string) => void;
      reconnect: () => void;
    };
  }
}
window.umcode = {
  newChat: () => chat.newChat(),
  show: (view) => (app.view = view as typeof app.view),
  openThread: (id) => void chat.open(id),
  openProject: (id) => void projects.open(id).then(() => chat.loadThreads()),
  openFile: (path) => void inspector.openFile(path),
  reconnect: () => app.rpc.retry(),
};

app.start();
export default mount(isMini ? MiniOverlay : App, { target: document.getElementById('app')! });
