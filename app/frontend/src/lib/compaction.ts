import type {CompactionState} from './types';
export const compactionActive=(state:CompactionState|null):boolean=>!!state&&['preparing','summarizing','restoring'].includes(state.status);
export function applyCompactionUpdate(current:CompactionState|null,incoming:CompactionState):CompactionState {
 if(!current)return incoming;
 if(current.threadId!==incoming.threadId)return current;
 if(current.id===incoming.id) {
  if(incoming.sequence<=current.sequence)return current;
  if(!compactionActive(current)&&compactionActive(incoming))return current;
 } else if(Date.parse(incoming.startedAt)<=Date.parse(current.startedAt))return current;
 return incoming;
}
export function compactionLabel(state:CompactionState):string {
 return ({preparing:'Preparing working context',summarizing:'Summarizing earlier context',restoring:'Restoring working context'} as Record<string,string>)[state.status]??'';
}
