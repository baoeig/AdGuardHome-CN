import { For, Show } from 'solid-js';
import cn from 'clsx';

import { Input } from 'panel/common/controls/Input';
import { Switch } from 'panel/common/controls/Switch';
import { Textarea } from 'panel/common/controls/Textarea';
import { Button } from 'panel/common/ui/Button';
import { PageLoader } from 'panel/common/ui/Loader';
import intl from 'panel/common/intl';
import theme from 'panel/lib/theme';

import { PAGE_SIZE, useGfwList } from './useGfwList';
import s from './GfwList.module.pcss';

export const GfwList = () => {
    const model = useGfwList();
    const busy = () => !!model.action();
    const domains = () => model.status()?.custom_domains ?? [];
    const intervalValid = () => {
        const hours = Number(model.draft.intervalHours);
        return hours >= 1 && hours <= 168;
    };

    return (
        <div class={theme.layout.container}>
            <div class={cn(theme.layout.containerIn, theme.layout.containerIn_one_col)}>
                <h1 class={cn(theme.layout.title, theme.title.h4, theme.title.h3_tablet)}>
                    {intl.getMessage('gfwlist_title')}
                </h1>

                <Show
                    when={model.status()}
                    fallback={
                        <Show
                            when={model.action() === 'load'}
                            fallback={
                                <div class={s.card}>
                                    <Button onClick={model.load}>
                                        {intl.getMessage('try_again')}
                                    </Button>
                                </div>
                            }
                        >
                            <PageLoader />
                        </Show>
                    }
                >
                    {(status) => (
                        <>
                            <form
                                class={s.card}
                                onSubmit={(event) => {
                                    event.preventDefault();
                                    if (intervalValid()) void model.save();
                                }}
                            >
                                <h2 class={theme.title.h5}>{intl.getMessage('gfwlist_config')}</h2>
                                <Switch
                                    id="gfwlist-enabled"
                                    checked={model.draft.enabled}
                                    disabled={busy()}
                                    onChange={() => model.setDraft('enabled', !model.draft.enabled)}
                                >
                                    {intl.getMessage('gfwlist_enable')}
                                </Switch>
                                <p>{intl.getMessage('gfwlist_enable_desc')}</p>
                                <Input
                                    id="gfwlist-url"
                                    type="url"
                                    size="medium"
                                    onCard
                                    label={intl.getMessage('gfwlist_url')}
                                    value={model.draft.url}
                                    disabled={busy()}
                                    onInput={(event) =>
                                        model.setDraft('url', event.currentTarget.value)
                                    }
                                />
                                <Textarea
                                    id="gfwlist-upstream"
                                    onCard
                                    rows={4}
                                    label={intl.getMessage('gfwlist_upstream_dns')}
                                    value={model.draft.upstreams}
                                    disabled={busy()}
                                    onInput={(event) =>
                                        model.setDraft('upstreams', event.currentTarget.value)
                                    }
                                />
                                <p>{intl.getMessage('gfwlist_upstream_dns_desc')}</p>
                                <Input
                                    id="gfwlist-interval"
                                    type="number"
                                    size="medium"
                                    onCard
                                    label={intl.getMessage('gfwlist_update_interval')}
                                    errorMessage={
                                        intervalValid()
                                            ? undefined
                                            : intl.getMessage('gfwlist_interval_range')
                                    }
                                    value={model.draft.intervalHours}
                                    disabled={busy()}
                                    onInput={(event) =>
                                        model.setDraft('intervalHours', event.currentTarget.value)
                                    }
                                />
                                <p>
                                    {intl.getMessage('gfwlist_domain_count')}{' '}
                                    <strong>{status().domain_count}</strong>
                                </p>
                                <div class={s.actions}>
                                    <Button
                                        id="gfwlist-save"
                                        type="submit"
                                        disabled={busy() || !intervalValid()}
                                    >
                                        {intl.getMessage('save')}
                                    </Button>
                                    <Button
                                        id="gfwlist-update-now"
                                        variant="secondary"
                                        disabled={busy() || !status().enabled}
                                        onClick={model.update}
                                    >
                                        {model.action() === 'update'
                                            ? intl.getMessage('gfwlist_updating')
                                            : intl.getMessage('gfwlist_update_now')}
                                    </Button>
                                </div>
                            </form>
                            <section class={s.card}>
                                <h2 class={theme.title.h5}>
                                    {intl.getMessage('gfwlist_custom_domains')}
                                </h2>
                                <p>{intl.getMessage('gfwlist_custom_domains_hint')}</p>
                                <Textarea
                                    id="gfwlist-new-domain"
                                    onCard
                                    rows={3}
                                    label={intl.getMessage('gfwlist_domain_rules')}
                                    value={model.rules()}
                                    disabled={busy()}
                                    placeholder="example.org"
                                    onInput={(event) => model.setRules(event.currentTarget.value)}
                                />
                                <div class={s.actions}>
                                    <Button
                                        id="gfwlist-add-domain"
                                        variant="secondary"
                                        disabled={busy() || !model.rules().trim()}
                                        onClick={model.add}
                                    >
                                        {intl.getMessage('gfwlist_add_domain')}
                                    </Button>
                                </div>
                                <Show
                                    when={domains().length > 0}
                                    fallback={<p>{intl.getMessage('gfwlist_no_custom_domains')}</p>}
                                >
                                    <ul class={s.domains}>
                                        <For each={domains()}>
                                            {(domain) => (
                                                <li class={s.domain}>
                                                    <span>{domain}</span>
                                                    <Button
                                                        size="small"
                                                        variant="secondary-danger"
                                                        aria-label={intl.getMessage(
                                                            'gfwlist_aria_remove_domain',
                                                            { domain },
                                                        )}
                                                        disabled={busy()}
                                                        onClick={() => model.remove(domain)}
                                                    >
                                                        {intl.getMessage('delete_table_action')}
                                                    </Button>
                                                </li>
                                            )}
                                        </For>
                                    </ul>
                                    <div class={s.actions}>
                                        <p>
                                            {intl.getMessage('gfwlist_custom_domains_page_info', {
                                                from: model.page() * PAGE_SIZE + 1,
                                                to: model.page() * PAGE_SIZE + domains().length,
                                                total: model.total(),
                                            })}
                                        </p>
                                        <Show when={model.pageCount() > 1}>
                                            <Button
                                                variant="secondary"
                                                disabled={busy() || model.page() === 0}
                                                onClick={() => model.changePage(model.page() - 1)}
                                            >
                                                {intl.getMessage('aria_previous_page')}
                                            </Button>
                                            <Button
                                                variant="secondary"
                                                disabled={
                                                    busy() || model.page() + 1 >= model.pageCount()
                                                }
                                                onClick={() => model.changePage(model.page() + 1)}
                                            >
                                                {intl.getMessage('aria_next_page')}
                                            </Button>
                                        </Show>
                                    </div>
                                </Show>
                            </section>
                            <form
                                class={s.card}
                                onSubmit={(event) => {
                                    event.preventDefault();
                                    void model.check();
                                }}
                            >
                                <h2 class={theme.title.h5}>
                                    {intl.getMessage('gfwlist_check_title')}
                                </h2>
                                <Input
                                    id="gfwlist-check-domain"
                                    size="medium"
                                    onCard
                                    label={intl.getMessage('domain')}
                                    value={model.hostname()}
                                    disabled={busy()}
                                    placeholder="example.org"
                                    onInput={(event) => {
                                        model.setHostname(event.currentTarget.value);
                                        model.setResult(undefined);
                                    }}
                                />
                                <div class={s.actions}>
                                    <Button
                                        type="submit"
                                        disabled={busy() || !model.hostname().trim()}
                                    >
                                        {intl.getMessage('gfwlist_check_domain')}
                                    </Button>
                                </div>
                                <Show when={model.result()}>
                                    {(result) => (
                                        <p role="status">
                                            {result().matched
                                                ? intl.getMessage('gfwlist_check_matched', {
                                                      domain: result().domain,
                                                      source:
                                                          result().source === 'custom'
                                                              ? intl.getMessage(
                                                                    'gfwlist_check_source_custom',
                                                                )
                                                              : intl.getMessage(
                                                                    'gfwlist_check_source_gfwlist',
                                                                ),
                                                  })
                                                : intl.getMessage('gfwlist_check_not_matched', {
                                                      domain: result().domain,
                                                  })}
                                        </p>
                                    )}
                                </Show>
                            </form>
                        </>
                    )}
                </Show>
            </div>
        </div>
    );
};
