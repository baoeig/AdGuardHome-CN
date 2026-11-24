import { createSignal, onCleanup, onMount, untrack } from 'solid-js';
import { createStore } from 'solid-js/store';

import {
    addGfwListDomains,
    checkGfwListDomain,
    getGfwListStatus,
    removeGfwListDomains,
    setGfwListConfig,
    updateGfwList,
} from 'panel/api/generated';
import type { GfwListCheckResult, GfwListStatus } from 'panel/api/model';
import intl from 'panel/common/intl';
import { addErrorToast, addNoticeToast, addSuccessToast } from 'panel/stores/toasts';

export const PAGE_SIZE = 50;

const DEFAULT_INTERVAL = 86400;
const SECONDS_PER_HOUR = 3600;
type Action = 'load' | 'page' | 'save' | 'update' | 'domains' | 'check';

export const useGfwList = () => {
    const controller = new AbortController();
    const options = { signal: controller.signal };
    onCleanup(() => controller.abort());

    const [status, setStatus] = createSignal<GfwListStatus>();
    const [action, setAction] = createSignal<Action>();
    const [draft, setDraft] = createStore({
        enabled: false,
        url: '',
        upstreams: '',
        intervalHours: String(DEFAULT_INTERVAL / SECONDS_PER_HOUR),
    });
    const [rules, setRules] = createSignal('');
    const [hostname, setHostname] = createSignal('');
    const [result, setResult] = createSignal<GfwListCheckResult>();

    const page = () => status()?.custom_domain_page ?? 0;
    const total = () => status()?.custom_domains_total ?? 0;
    const pageCount = () => Math.max(1, Math.ceil(total() / PAGE_SIZE));

    const loadPage = async (target: number) => {
        const data = await getGfwListStatus(
            {
                custom_domain_page: target,
                custom_domain_page_size: PAGE_SIZE,
            },
            options,
        );
        setStatus(data);
        return data;
    };

    const perform = async (
        task: Action,
        operation: () => Promise<void>,
        explainError: (error: string) => string,
    ) => {
        if (action()) return;

        setAction(task);
        try {
            await operation();
        } catch (error: unknown) {
            if (controller.signal.aborted) return;

            const message = error instanceof Error ? error.message : String(error);
            if (task === 'update' && message.trimEnd().endsWith('| 409')) {
                addNoticeToast(intl.getMessage('gfwlist_update_in_progress'));
            } else {
                addErrorToast({ error: explainError(message) });
            }
        } finally {
            setAction(undefined);
        }
    };

    const loadError = (error: string) => intl.getMessage('gfwlist_load_error', { error });
    const saveError = (error: string) => intl.getMessage('gfwlist_save_error', { error });

    const load = () =>
        perform(
            'load',
            async () => {
                const data = await loadPage(0);
                setDraft({
                    enabled: data.enabled,
                    url: data.url,
                    upstreams: (data.upstream_dns ?? []).join('\n'),
                    intervalHours: String(
                        (data.update_interval || DEFAULT_INTERVAL) / SECONDS_PER_HOUR,
                    ),
                });
            },
            loadError,
        );

    const changePage = (target: number) =>
        perform(
            'page',
            async () => {
                await loadPage(target);
            },
            loadError,
        );

    const save = () =>
        perform(
            'save',
            async () => {
                const config = untrack(() => ({
                    enabled: draft.enabled,
                    url: draft.url.trim(),
                    upstream_dns: draft.upstreams
                        .split('\n')
                        .map((line) => line.trim())
                        .filter(Boolean),
                    update_interval: Math.round(Number(draft.intervalHours) * SECONDS_PER_HOUR),
                }));
                await setGfwListConfig(config, options);
                setResult(undefined);
                await loadPage(untrack(page));
                addSuccessToast(intl.getMessage('gfwlist_config_saved'));
            },
            saveError,
        );

    const update = () =>
        perform(
            'update',
            async () => {
                const updated = await updateGfwList(options);
                setResult(undefined);
                // The update can succeed even if the status refresh fails.
            const data = await loadPage(untrack(page)).catch((): undefined => undefined);
                const count = data?.domain_count ?? updated.domain_count;
                if (!data)
                    setStatus((previous) => previous && { ...previous, domain_count: count });
                if (!controller.signal.aborted)
                    addSuccessToast(intl.getMessage('gfwlist_updated', { count }));
            },
            (error) => intl.getMessage('gfwlist_update_error', { error }),
        );

    const add = () =>
        perform(
            'domains',
            async () => {
                // The API normalizes supported rule formats and deduplicates across all pages.
                const domains = untrack(rules)
                    .split(/[\n,]+/)
                    .map((line) => line.trim())
                    .filter(Boolean);
                if (!domains.length) return;

                const response = await addGfwListDomains({ domains }, options);
                const added = response.added_count ?? 0;
                const skipped = domains.length - added;
                const lastPage = Math.max(
                    0,
                    Math.ceil(response.custom_domains_total / PAGE_SIZE) - 1,
                );

                if (added > 0) {
                    setRules('');
                    setResult(undefined);
                    await loadPage(lastPage);
                }

                if (skipped > 0) {
                    addNoticeToast(intl.getMessage('gfwlist_domains_partial', { added, skipped }));
                } else {
                    addSuccessToast(intl.getPlural('gfwlist_domains_added', added));
                }
            },
            saveError,
        );

    const remove = (domain: string) =>
        perform(
            'domains',
            async () => {
                const response = await removeGfwListDomains({ domains: [domain] }, options);
                const lastPage = Math.max(
                    0,
                    Math.ceil(response.custom_domains_total / PAGE_SIZE) - 1,
                );
                setResult(undefined);
                await loadPage(Math.min(untrack(page), lastPage));
                if (response.removed_count)
                    addSuccessToast(intl.getMessage('gfwlist_domain_removed'));
            },
            saveError,
        );

    const check = () =>
        perform(
            'check',
            async () => {
                setResult(undefined);
                const response = await checkGfwListDomain(
                    { domain: untrack(hostname).trim() },
                    options,
                );
                setResult(response);
            },
            (error) => intl.getMessage('gfwlist_check_error', { error }),
        );

    onMount(() => load());

    return {
        status,
        action,
        draft,
        setDraft,
        rules,
        setRules,
        hostname,
        setHostname,
        result,
        setResult,
        page,
        total,
        pageCount,
        load,
        changePage,
        save,
        update,
        add,
        remove,
        check,
    };
};
