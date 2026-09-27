// Tracks just enough state for the mini overlay: the single most recently
// active turn (across every thread — the overlay is global, not per-chat)
// and a small cache of thread titles. The overlay leads with pending
// approvals when there are any; this fills the moments before or after one.
import { app } from '$lib/stores/app.svelte';
import type { Item, Turn } from '$lib/types';

class MiniState {
  activeTurnId = $state('');
  activeThreadId = $state('');
  threadTitle = $state('');
  latestText = $state('');
  running = $state(false);

  private titles = new Map<string, string>();

  constructor() {
    app.rpc.on('thread/updated', (p: { thread: { id: string; title: string } }) => {
      this.titles.set(p.thread.id, p.thread.title);
      if (p.thread.id === this.activeThreadId) this.threadTitle = p.thread.title || 'Untitled chat';
    });
    app.rpc.on('turn/started', (p: { turn: Turn }) => this.onTurn(p.turn, true));
    app.rpc.on('turn/completed', (p: { turn: Turn }) => {
      if (p.turn.id === this.activeTurnId) this.onTurn(p.turn, false);
    });
    app.rpc.on('item/delta', (p: { threadId: string; turnId: string; itemId: string; text?: string }) => {
      if (p.turnId !== this.activeTurnId) return;
      if (p.text) this.latestText = (this.latestText + p.text).slice(-400);
    });
    app.rpc.on('item/completed', (p: { item: Item }) => {
      if (p.item.turnId !== this.activeTurnId) return;
      // A completed step (a tool call, a reasoning block) replaces the
      // streamed preview with a short label until the next delta arrives.
      if (p.item.kind !== 'agentMessage' && p.item.tool?.name) this.latestText = p.item.tool.name;
    });
  }

  private onTurn(turn: Turn, starting: boolean) {
    this.activeTurnId = starting ? turn.id : '';
    this.activeThreadId = starting ? turn.threadId : this.activeThreadId;
    this.running = starting;
    if (starting) {
      this.threadTitle = this.titles.get(turn.threadId) || 'Untitled chat';
      this.latestText = '';
    }
  }

  async stop() {
    if (this.activeTurnId) await app.try('turn/interrupt', { turnId: this.activeTurnId });
  }
}

export const mini = new MiniState();
