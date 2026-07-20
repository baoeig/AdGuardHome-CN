/**
 * Normalizes a GFW custom rule to the domain it represents.  This intentionally
 * mirrors the backend implementation in internal/dnsforward/gfwlist.go:
 * plain domains, wildcard domains, common adblock domain rules, URL rules,
 * and hosts-file lines are accepted.
 *
 * Note: the backend re-normalizes on the server side, so this is only a
 * pre-validation convenience for the UI.
 */
export const normalizeGfwDomainRule = (rule: string): string => {
    let value = rule.trim().toLowerCase();
    if (!value) {
        return '';
    }

    if (value.startsWith('!') || value.startsWith('#') || value.startsWith('@@') || value.startsWith('[')) {
        return '';
    }

    const fields = value.split(/\s+/);
    if (fields.length > 1) {
        value = fields[fields.length - 1];
    }

    if (value.startsWith('||')) {
        value = value.slice(2);
        const index = value.search(/[/^*]/);
        if (index >= 0) {
            value = value.slice(0, index);
        }
    } else if (value.startsWith('*.')) {
        value = value.slice(2);
    } else if (value.startsWith('.')) {
        value = value.slice(1);
    } else if (value.startsWith('|http://') || value.startsWith('|https://')) {
        value = value.replace(/^\|https?:\/\//, '');
        const index = value.search(/[/?:]/);
        if (index >= 0) {
            value = value.slice(0, index);
        }
    }

    value = value.replace(/\.$/, '');

    return value;
};

/**
 * Returns true when the error came from the backend rejecting a manual
 * update because another update is already in flight.  The API client
 * flattens errors into "<url> | <body> | <status>" strings, so the status
 * code suffix is the reliable signal.
 */
export const isUpdateInProgressError = (e: Error): boolean => {
    return typeof e?.message === 'string' && e.message.trimEnd().endsWith('| 409');
};
