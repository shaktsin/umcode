/**
 * Hook for seeing if nodes are initialized
 * @returns A boolean that indicates if nodes are initialized
 * @public
 */
export declare function useNodesInitialized(): {
    readonly current: boolean;
};
/**
 * Hook for seeing if the viewport is initialized
 * @returns - reactive viewportInitialized
 */
export declare function useViewportInitialized(): {
    readonly current: boolean;
};
