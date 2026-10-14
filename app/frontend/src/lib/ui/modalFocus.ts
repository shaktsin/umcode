const modalStack: HTMLElement[] = [];

/** Focus containment for temporary panels; feature popups may portal above them. */
function trapFocus(node:HTMLElement, onclose:()=>void) {
  modalStack.push(node);
  const ownsFocus=()=>modalStack.at(-1)===node;
  const previous=document.activeElement as HTMLElement|null;
  const items=()=>[...node.querySelectorAll<HTMLElement>('button:not(:disabled),input:not(:disabled),textarea:not(:disabled),select:not(:disabled),[tabindex="0"]')].filter(el=>el.getClientRects().length);
  const frame=requestAnimationFrame(()=> {if(ownsFocus()) (items()[0]??node).focus();});
  const key=(e:KeyboardEvent)=>{
    if(!ownsFocus()||document.querySelector('[data-popup-surface]'))return;
    if(e.key==='Escape'){e.preventDefault();e.stopImmediatePropagation();onclose();}
    if(e.key==='Tab'){const list=items();if(!list.length){e.preventDefault();node.focus();return;}const first=list[0],last=list.at(-1)!;if(e.shiftKey&&document.activeElement===first){e.preventDefault();last.focus();}else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first.focus();}}
  };
  const focus=(e:FocusEvent)=>{if(ownsFocus()&&!node.contains(e.target as Node)&&!(e.target as HTMLElement)?.closest('[data-popup-surface]'))(items()[0]??node).focus();};
  document.addEventListener('keydown',key,true);document.addEventListener('focusin',focus);
  return {destroy(){const index=modalStack.indexOf(node);if(index>=0)modalStack.splice(index,1);cancelAnimationFrame(frame);document.removeEventListener('keydown',key,true);document.removeEventListener('focusin',focus);if(previous?.isConnected)previous.focus();}};
}

export function modalFocus(node:HTMLElement, onclose:(()=>void)|undefined) {
  let close=onclose;
  let trap=close?trapFocus(node,()=>close?.()):undefined;
  return { update(next:(()=>void)|undefined){if(!!next!==!!close){trap?.destroy();close=next;trap=close?trapFocus(node,()=>close?.()):undefined;}else close=next;}, destroy(){trap?.destroy();} };
}
