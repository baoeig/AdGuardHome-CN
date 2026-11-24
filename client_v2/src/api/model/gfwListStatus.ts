export interface GfwListStatus {
    enabled: boolean;
    url: string;
    /** @nullable */
    upstream_dns: string[] | null;
    update_interval: number;
    domain_count: number;
    /** @nullable */
    custom_domains: string[] | null;
    custom_domains_total: number;
    custom_domain_page: number;
}
