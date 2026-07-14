import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';

import Card from '../../ui/Card';
import PageTitle from '../../ui/PageTitle';
import Loading from '../../ui/Loading';
import apiClient from '../../../api/Api';

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

const GfwList = () => {
    const { t } = useTranslation();

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
            await fetchStatus();
            showSuccess(t('gfwlist_updated', { count: data?.domain_count ?? 0 }));
        } catch (e: any) {
            showError(t('gfwlist_update_error', { error: e.message }));
        } finally {
            setUpdating(false);
        }
    };

    const handleAddDomain = async () => {
        const domain = newDomain.trim().toLowerCase();
        if (!domain) return;
        if (!/^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/.test(domain)) {
            setDomainError(t('gfwlist_domain_invalid'));
            return;
        }
        setDomainError('');
        try {
            await apiClient.addGfwListDomains([domain]);
            setNewDomain('');
            await fetchStatus();
            showSuccess(t('gfwlist_domain_added'));
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
                    {/* Enable toggle */}
                    <div className="form__group form-group">
                        <label className="form__label" htmlFor="gfwlist-enabled">
                            {t('gfwlist_enable')}
                        </label>
                        <label className="custom-toggle">
                            <input
                                id="gfwlist-enabled"
                                type="checkbox"
                                checked={enabled}
                                onChange={(e) => setEnabled(e.target.checked)}
                            />
                            <span className="custom-toggle__slider" />
                        </label>
                        <div className="form__desc">{t('gfwlist_enable_desc')}</div>
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
                            <strong>{status.domain_count}</strong>
                        </div>
                    </div>

                    <div className="d-flex mt-3">
                        <button
                            id="gfwlist-save"
                            type="submit"
                            className="btn btn-success mr-2"
                            disabled={saving}
                        >
                            {saving ? t('processing_table_filter_of_search') : t('save_btn')}
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

                <div className="input-group mb-3">
                    <input
                        id="gfwlist-new-domain"
                        type="text"
                        className={`form-control ${domainError ? 'is-invalid' : ''}`}
                        value={newDomain}
                        onChange={(e) => {
                            setNewDomain(e.target.value);
                            setDomainError('');
                        }}
                        onKeyDown={(e) => e.key === 'Enter' && handleAddDomain()}
                        placeholder="example.com"
                    />
                    <div className="input-group-append">
                        <button
                            id="gfwlist-add-domain"
                            type="button"
                            className="btn btn-outline-primary"
                            onClick={handleAddDomain}
                        >
                            {t('add_table_action')}
                        </button>
                    </div>
                    {domainError && <div className="invalid-feedback">{domainError}</div>}
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
        </>
    );
};

export default GfwList;
