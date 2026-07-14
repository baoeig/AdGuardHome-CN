import React, { useEffect, useState, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useDispatch } from 'react-redux';

import Card from '../../ui/Card';
import PageTitle from '../../ui/PageTitle';
import Loading from '../../ui/Loading';
import { Checkbox } from '../../ui/Controls/Checkbox';
import Check, { FilteringCheckFormValues } from '../../Filters/Check';
import apiClient from '../../../api/Api';
import { checkHost } from '../../../actions/filtering';

interface GfwListStatus {
    enabled: boolean;
    url: string;
    upstream_dns: string[];
    custom_domains: string[];
    domain_count: number;
    update_interval: number;
}

const DEFAULT_STATUS: GfwListStatus = {
    enabled: false,
    url: '',
    upstream_dns: [],
    custom_domains: [],
    domain_count: 0,
    update_interval: 86400,
};

/**
 * Validates a single domain string.  Returns an error i18n key or empty string
 * if valid.
 */
const validateDomain = (
    domain: string,
    existingDomains: string[],
): string => {
    if (!domain) {
        return 'gfwlist_domain_empty';
    }

    // Must contain at least one dot.
    if (!domain.includes('.')) {
        return 'gfwlist_domain_need_dot';
    }

    // Only allow valid domain characters.
    if (!/^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$/.test(domain)) {
        return 'gfwlist_domain_invalid';
    }

    // Must not be an IP address (simple check).
    if (/^\d{1,3}(\.\d{1,3}){3}$/.test(domain)) {
        return 'gfwlist_domain_is_ip';
    }

    // Duplicate check.
    if (existingDomains.includes(domain)) {
        return 'gfwlist_domain_duplicate';
    }

    return '';
};

const GfwList = () => {
    const { t } = useTranslation();
    const dispatch = useDispatch();

    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [updating, setUpdating] = useState(false);
    const [status, setStatus] = useState<GfwListStatus>(DEFAULT_STATUS);
    const [successMsg, setSuccessMsg] = useState('');
    const [errorMsg, setErrorMsg] = useState('');

    // form state
    const [enabled, setEnabled] = useState(false);
    const [url, setUrl] = useState('');
    const [upstreamDns, setUpstreamDns] = useState('');
    const [updateInterval, setUpdateInterval] = useState(86400);

    // custom domain management
    const [newDomain, setNewDomain] = useState('');
    const [domainError, setDomainError] = useState('');

    // Track live domain count separately so it updates immediately after
    // operations like "update list now" without waiting for a full status
    // fetch.
    const [liveDomainCount, setLiveDomainCount] = useState(0);

    const domainInputRef = useRef<HTMLInputElement>(null);

    const showSuccess = (msg: string) => {
        setSuccessMsg(msg);
        setErrorMsg('');
        setTimeout(() => setSuccessMsg(''), 3000);
    };

    const showError = (msg: string) => {
        setErrorMsg(msg);
        setSuccessMsg('');
    };

    const fetchStatus = async () => {
        try {
            const data: GfwListStatus = await apiClient.getGfwListStatus();
            setStatus(data);
            setLiveDomainCount(data.domain_count);
            setEnabled(data.enabled ?? false);
            setUrl(data.url || '');
            setUpstreamDns((data.upstream_dns || []).join('\n'));
            setUpdateInterval(data.update_interval || 86400);
        } catch (e: any) {
            showError(t('gfwlist_load_error', { error: e.message }));
        } finally {
            setLoading(false);
        }
    };

    useEffect(() => {
        fetchStatus();
    }, []);

    const handleSave = async (e: React.FormEvent) => {
        e.preventDefault();
        setSaving(true);
        try {
            const upstreams = upstreamDns
                .split('\n')
                .map((s) => s.trim())
                .filter(Boolean);

            await apiClient.setGfwListConfig({
                enabled,
                url,
                upstream_dns: upstreams,
                update_interval: updateInterval,
            });

            await fetchStatus();
            showSuccess(t('gfwlist_config_saved'));
        } catch (e: any) {
            showError(t('gfwlist_save_error', { error: e.message }));
        } finally {
            setSaving(false);
        }
    };

    const handleUpdate = async () => {
        setUpdating(true);
        try {
            const data = await apiClient.updateGfwList();
            const newCount = data?.domain_count ?? 0;

            // Update domain count immediately from the response.
            setLiveDomainCount(newCount);

            // Also refresh full status to keep everything in sync.
            await fetchStatus();

            showSuccess(t('gfwlist_updated', { count: newCount }));
        } catch (e: any) {
            showError(t('gfwlist_update_error', { error: e.message }));
        } finally {
            setUpdating(false);
        }
    };

    const handleAddDomain = async () => {
        const raw = newDomain.trim();
        if (!raw) return;

        // Support batch add: split by newlines, commas, spaces.
        const candidates = raw
            .split(/[\n,\s]+/)
            .map((d) => d.trim().toLowerCase())
            .filter(Boolean);

        if (candidates.length === 0) return;

        // Validate each domain.
        const validDomains: string[] = [];
        const errors: string[] = [];
        const currentDomains = status.custom_domains || [];

        candidates.forEach((domain) => {
            const err = validateDomain(domain, [...currentDomains, ...validDomains]);
            if (err) {
                errors.push(`${domain}: ${t(err)}`);
            } else {
                validDomains.push(domain);
            }
        });

        if (errors.length > 0 && validDomains.length === 0) {
            setDomainError(errors.join('; '));
            return;
        }

        if (validDomains.length === 0) return;

        setDomainError('');
        try {
            await apiClient.addGfwListDomains(validDomains);
            setNewDomain('');
            await fetchStatus();

            if (errors.length > 0) {
                showSuccess(
                    t('gfwlist_domains_partial', {
                        added: validDomains.length,
                        skipped: errors.length,
                    }),
                );
            } else {
                showSuccess(
                    validDomains.length > 1
                        ? t('gfwlist_domains_added', { count: validDomains.length })
                        : t('gfwlist_domain_added'),
                );
            }
        } catch (e: any) {
            showError(t('gfwlist_save_error', { error: e.message }));
        }
    };

    const handleRemoveDomain = async (domain: string) => {
        try {
            await apiClient.removeGfwListDomains([domain]);
            await fetchStatus();
            showSuccess(t('gfwlist_domain_removed'));
        } catch (e: any) {
            showError(t('gfwlist_save_error', { error: e.message }));
        }
    };

    const handleCheck = (values: FilteringCheckFormValues) => {
        const params: FilteringCheckFormValues = { name: values.name };

        if (values.client) {
            params.client = values.client;
        }

        if (values.qtype) {
            params.qtype = values.qtype;
        }

        dispatch(checkHost(params));
    };

    if (loading) {
        return <Loading />;
    }

    return (
        <>
            <PageTitle title={t('gfwlist_title')} />

            {successMsg && (
                <div className="alert alert-success" role="alert">
                    {successMsg}
                </div>
            )}
            {errorMsg && (
                <div className="alert alert-danger" role="alert">
                    {errorMsg}
                </div>
            )}

            {/* Config card */}
            <Card title={t('gfwlist_config')} bodyType="card-body box-body--settings">
                <form onSubmit={handleSave}>
                    {/* Enable toggle — uses project-standard Checkbox */}
                    <div className="form__group form__group--checkbox">
                        <Checkbox
                            value={enabled}
                            title={t('gfwlist_enable')}
                            subtitle={t('gfwlist_enable_desc')}
                            onChange={(checked) => setEnabled(checked)}
                        />
                    </div>

                    {/* URL */}
                    <div className="form__group form-group">
                        <label className="form__label" htmlFor="gfwlist-url">
                            {t('gfwlist_url')}
                        </label>
                        <input
                            id="gfwlist-url"
                            className="form-control"
                            type="url"
                            value={url}
                            onChange={(e) => setUrl(e.target.value)}
                            placeholder="https://raw.githubusercontent.com/gfwlist/gfwlist/master/gfwlist.txt"
                        />
                        <div className="form__desc">{t('gfwlist_url_desc')}</div>
                    </div>

                    {/* Upstream DNS */}
                    <div className="form__group form-group">
                        <label className="form__label" htmlFor="gfwlist-upstream">
                            {t('gfwlist_upstream_dns')}
                        </label>
                        <textarea
                            id="gfwlist-upstream"
                            className="form-control"
                            rows={4}
                            value={upstreamDns}
                            onChange={(e) => setUpstreamDns(e.target.value)}
                            placeholder={'8.8.8.8\n8.8.4.4'}
                        />
                        <div className="form__desc">{t('gfwlist_upstream_dns_desc')}</div>
                    </div>

                    {/* Update interval */}
                    <div className="form__group form-group">
                        <label className="form__label" htmlFor="gfwlist-interval">
                            {t('gfwlist_update_interval')}
                        </label>
                        <select
                            id="gfwlist-interval"
                            className="form-control custom-select"
                            value={updateInterval}
                            onChange={(e) => setUpdateInterval(Number(e.target.value))}
                        >
                            <option value={3600}>{t('interval_hours', { count: 1 })}</option>
                            <option value={21600}>{t('interval_hours', { count: 6 })}</option>
                            <option value={43200}>{t('interval_hours', { count: 12 })}</option>
                            <option value={86400}>{t('interval_24_hour')}</option>
                            <option value={604800}>{t('interval_days', { count: 7 })}</option>
                        </select>
                    </div>

                    {/* Domain count */}
                    <div className="form__group form-group">
                        <label className="form__label">{t('gfwlist_domain_count')}</label>
                        <div className="form-control-plaintext">
                            <strong>{liveDomainCount}</strong>
                        </div>
                    </div>

                    <div className="d-flex mt-3">
                        <button
                            id="gfwlist-save"
                            type="submit"
                            className="btn btn-success mr-2"
                            disabled={saving}
                        >
                            {saving ? t('gfwlist_saving') : t('gfwlist_save')}
                        </button>

                        <button
                            id="gfwlist-update-now"
                            type="button"
                            className="btn btn-outline-primary"
                            onClick={handleUpdate}
                            disabled={updating || !status.enabled}
                        >
                            {updating ? t('gfwlist_updating') : t('gfwlist_update_now')}
                        </button>
                    </div>
                </form>
            </Card>

            {/* Custom domains card */}
            <Card title={t('gfwlist_custom_domains')} bodyType="card-body box-body--settings">
                <div className="form__desc mb-3">{t('gfwlist_custom_domains_desc')}</div>

                <div className="form__desc form__desc--small mb-2">
                    {t('gfwlist_custom_domains_hint')}
                </div>

                <div className="input-group mb-3">
                    <input
                        ref={domainInputRef}
                        id="gfwlist-new-domain"
                        type="text"
                        className={`form-control ${domainError ? 'is-invalid' : ''}`}
                        value={newDomain}
                        onChange={(e) => {
                            setNewDomain(e.target.value);
                            setDomainError('');
                        }}
                        onKeyDown={(e) => {
                            if (e.key === 'Enter') {
                                e.preventDefault();
                                handleAddDomain();
                            }
                        }}
                        placeholder="example.com"
                    />
                    <div className="input-group-append">
                        <button
                            id="gfwlist-add-domain"
                            type="button"
                            className="btn btn-outline-primary"
                            onClick={handleAddDomain}
                        >
                            {t('gfwlist_add_domain')}
                        </button>
                    </div>
                    {domainError && <div className="invalid-feedback d-block">{domainError}</div>}
                </div>

                {status.custom_domains && status.custom_domains.length > 0 ? (
                    <table className="table table-striped table-hover">
                        <thead>
                            <tr>
                                <th>{t('domain')}</th>
                                <th className="text-right">{t('actions_table_header')}</th>
                            </tr>
                        </thead>
                        <tbody>
                            {status.custom_domains.map((d) => (
                                <tr key={d}>
                                    <td>{d}</td>
                                    <td className="text-right">
                                        <button
                                            type="button"
                                            className="btn btn-sm btn-outline-danger"
                                            onClick={() => handleRemoveDomain(d)}
                                        >
                                            {t('delete_table_action')}
                                        </button>
                                    </td>
                                </tr>
                            ))}
                        </tbody>
                    </table>
                ) : (
                    <div className="text-muted">{t('gfwlist_no_custom_domains')}</div>
                )}
            </Card>

            <Check onSubmit={handleCheck} />
        </>
    );
};

export default GfwList;
