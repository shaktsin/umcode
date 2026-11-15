/**
 * When you programmatically add or remove handles to a node or update a node's
 * handle position, you need to let Svelte Flow know about it using this hook. This
 * will update the internal dimensions of the node and properly reposition handles
 * on the canvas if necessary.
 *
 * @public
 * @returns A function for telling Svelte Flow to update the internal state of one or more nodes
 * that you have changed programmatically.
 */
export declare function useUpdateNodeInternals(): (nodeId?: string | string[]) => void;
