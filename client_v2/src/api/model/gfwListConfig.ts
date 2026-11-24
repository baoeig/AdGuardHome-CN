export interface GfwListConfig {
    enabled?: boolean;
    url?: string;
    /** @nullable */
    upstream_dns?: string[] | null;
    update_interval?: number;
}
