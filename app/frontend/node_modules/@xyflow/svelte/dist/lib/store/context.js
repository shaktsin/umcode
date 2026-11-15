import { setContext, getContext, hasContext } from 'svelte';
/**
 * Creates a type-safe context getter and setter pair.
 * Extended from Svelte's official createContext pattern.
 * - When called with an error message string, it throws if the context is not set
 * - When called without arguments, it returns the context value or undefined
 */
function createContext() {
    const key = {};
    return [
        (errorMessage) => {
            if (errorMessage && !hasContext(key)) {
                throw new Error(errorMessage);
            }
            return getContext(key);
        },
        (context) => setContext(key, context)
    ];
}
export const [getNodeIdContext, setNodeIdContext] = createContext();
export const [getNodeConnectableContext, setNodeConnectableContext] = createContext();
export const [getEdgeIdContext, setEdgeIdContext] = createContext();
