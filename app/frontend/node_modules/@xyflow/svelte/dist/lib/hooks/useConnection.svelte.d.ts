import type { ConnectionState } from '@xyflow/system';
/**
 * Hook for receiving the current connection.
 *
 * @public
 * @returns Current connection as a signal
 */
export declare function useConnection(): {
    current: ConnectionState;
};
