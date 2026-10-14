<script lang="ts">
  import { tick, type Snippet } from 'svelte';
  import { portal } from '$lib/ui/portal';
  import { placePopup, type PopupGeometry } from '$lib/ui/popupGeometry';
  let {open,anchor,kind,preferredSide='below',onclose,children}:{open:boolean;anchor:HTMLElement|undefined;kind:'menu'|'skills';preferredSide?:'above'|'below';onclose:(reason:'escape'|'outside')=>void;children:Snippet}=$props();
  let surface:HTMLDivElement|undefined=$state();
  let geometry:PopupGeometry|undefined=$state();
  $effect(()=>{
    if(!open||!anchor||!surface)return;
    const trigger=anchor,node=surface;
    let frame=0;
    const measure=()=>{
      const header=node.querySelector<HTMLElement>('.popup-header');
      const list=node.querySelector<HTMLElement>('.popup-list');
      const rect=trigger.getBoundingClientRect();
      geometry=placePopup({trigger:rect,viewport:{width:window.innerWidth,height:window.innerHeight},preferredWidth:kind==='skills'?trigger.closest('[data-composer]')?.getBoundingClientRect().width??rect.width:320,headerHeight:header?.getBoundingClientRect().height??0,contentHeight:list?.scrollHeight??node.scrollHeight,kind,preferredSide});
    };
    const schedule=()=>{cancelAnimationFrame(frame);frame=requestAnimationFrame(measure);};
    const outside=(e:PointerEvent)=>{if(!node.contains(e.target as Node)&&!trigger.contains(e.target as Node))onclose('outside');};
    const key=(e:KeyboardEvent)=>{
      if(e.key==='Escape'){e.preventDefault();e.stopPropagation();onclose('escape');void tick().then(()=>trigger.focus());return;}
      if(kind==='menu'&&['ArrowDown','ArrowUp','Home','End'].includes(e.key)&&node.contains(document.activeElement)){
        const items=[...node.querySelectorAll<HTMLElement>('button:not(:disabled),input:not(:disabled),[tabindex="0"]')];
        if(!items.length)return;
        e.preventDefault();const index=items.indexOf(document.activeElement as HTMLElement);
        const next=e.key==='Home'?0:e.key==='End'?items.length-1:(index+(e.key==='ArrowDown'?1:-1)+items.length)%items.length;
        items[next].focus();items[next].scrollIntoView({block:'nearest'});
      }
    };
    const focusout=(e:FocusEvent)=>{const target=e.relatedTarget as Node|null;if(target&&!node.contains(target)&&!trigger.contains(target))onclose('outside');};
    const observer=new ResizeObserver(schedule);observer.observe(trigger);observer.observe(node);
    window.addEventListener('resize',schedule);window.addEventListener('scroll',schedule,true);
    document.addEventListener('pointerdown',outside,true);document.addEventListener('keydown',key,true);node.addEventListener('focusout',focusout);
    schedule();if(kind==='menu')void tick().then(()=>node.querySelector<HTMLElement>('button:not(:disabled),input:not(:disabled)')?.focus());
    return()=>{cancelAnimationFrame(frame);observer.disconnect();window.removeEventListener('resize',schedule);window.removeEventListener('scroll',schedule,true);document.removeEventListener('pointerdown',outside,true);document.removeEventListener('keydown',key,true);node.removeEventListener('focusout',focusout);};
  });
</script>
{#if open}
  <div use:portal bind:this={surface} data-popup-surface={kind} class="popup-surface" style:left={`${geometry?.left??8}px`} style:top={`${geometry?.top??8}px`} style:width={`${geometry?.width??200}px`} style:--popup-list-height={`${geometry?.maxListHeight??0}px`} style:visibility={geometry?'visible':'hidden'}>
    {@render children()}
  </div>
{/if}
