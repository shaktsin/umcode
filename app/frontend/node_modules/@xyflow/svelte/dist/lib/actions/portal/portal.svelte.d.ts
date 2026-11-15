type Portal = 'viewport-back' | 'viewport-front' | 'root' | 'edge-labels';
export declare function portal(node: Element, target: Portal | undefined): {
    update(target: Portal | undefined): Promise<void>;
    destroy(): void;
};
export {};
