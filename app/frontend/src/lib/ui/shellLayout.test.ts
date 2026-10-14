import {expect,it} from 'vitest';
import {resolveShellLayout,type ShellInput} from './shellLayout';
const base:ShellInput={width:760,height:520,railCollapsedPreference:false,sideRequested:true,inspectorRequested:true,sidePreferredWidth:720,inspectorPreferredWidth:720,activeAuxiliary:'inspector'};
it('preserves chat and overlays at minimum width',()=>{const p=resolveShellLayout(base);expect(p.railWidth).toBe(64);expect(p.mainWidth).toBeGreaterThanOrEqual(440);expect(p.overlay?.panel).toBe('inspector');expect(p.overlay!.width).toBeLessThanOrEqual(680);});
it('fits both panels on wide desktop and clamps oversized preferences',()=>{const p=resolveShellLayout({...base,width:1920,sidePreferredWidth:380,inspectorPreferredWidth:460});expect(p.inline).toEqual({side:380,inspector:460});expect(p.overlay).toBeNull();expect(p.mainWidth).toBe(798);});
it('restores expanded rail preference above breakpoint without mutating input',()=>{expect(resolveShellLayout({...base,width:959}).railWidth).toBe(64);expect(resolveShellLayout({...base,width:960}).railWidth).toBe(280);expect(base.sidePreferredWidth).toBe(720);});
