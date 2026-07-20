import { describe, expect, test } from 'vitest';

import { isUpdateInProgressError, normalizeGfwDomainRule } from '../helpers/gfwlist';

/**
 * Test cases mirror internal/dnsforward/gfwlist_internal_test.go
 * (TestNormalizeGFWDomainRule and TestExtractDomainFromAutoProxy).
 *
 * The frontend normalizeGfwDomainRule is a pure normalizer — it does NOT
 * reject IPs, missing dots, or invalid characters.  Those are caught by
 * validateDomainRule in the component, and by the backend's normalizeDomain
 * on submission.  Cases where the frontend normalize diverges from the
 * backend's normalizeGFWDomainRule are marked explicitly.
 */
describe('normalizeGfwDomainRule', () => {
    // Cases kept in exact parity with the backend test table.
    test.each([
        { name: 'plain', rule: 'example.org', want: 'example.org' },
        { name: 'wildcard', rule: '*.example.org', want: 'example.org' },
        { name: 'adblock', rule: '||example.org^', want: 'example.org' },
        { name: 'url', rule: '|https://www.example.org/path', want: 'www.example.org' },
        { name: 'hosts', rule: '127.0.0.1 example.org', want: 'example.org' },
        { name: 'comment', rule: '! example.org', want: '' },
        { name: 'exception', rule: '@@||example.org^', want: '' },
    ])('$name: $rule → $want', ({ rule, want }) => {
        expect(normalizeGfwDomainRule(rule)).toBe(want);
    });

    // Cases mirrored from backend TestExtractDomainFromAutoProxy.
    test.each([
        { name: 'domain_match', rule: '||google.com', want: 'google.com' },
        { name: 'domain_match_with_caret', rule: '||youtube.com^', want: 'youtube.com' },
        { name: 'domain_suffix', rule: '.twitter.com', want: 'twitter.com' },
        { name: 'header', rule: '[AutoProxy 0.2.9]', want: '' },
        { name: 'empty', rule: '', want: '' },
        { name: 'http_url', rule: '|https://www.facebook.com/path', want: 'www.facebook.com' },
        { name: 'http_url_no_path', rule: '|http://blocked.site.org', want: 'blocked.site.org' },
        { name: 'domain_with_subdomain', rule: '||apis.google.com', want: 'apis.google.com' },
        { name: 'domain_with_wildcard', rule: '||*.example.org', want: '' },
        { name: 'domain_with_path', rule: '||cdn.example.com/script.js', want: 'cdn.example.com' },
    ])('$name: $rule → $want', ({ rule, want }) => {
        expect(normalizeGfwDomainRule(rule)).toBe(want);
    });

    // Cases where the frontend intentionally diverges from the backend:
    // the frontend normalize is a pure extractor; validation happens in
    // validateDomainRule.  Document the contract so future readers know
    // these results are expected.
    describe('frontend divergences from backend (validated downstream)', () => {
        test('ip address: backend rejects, frontend normalize returns as-is', () => {
            // Backend normalizeGFWDomainRule returns "" via normalizeDomain's
            // net.ParseIP check.  Frontend returns the IP; validateDomainRule
            // rejects with gfwlist_domain_is_ip.
            expect(normalizeGfwDomainRule('8.8.8.8')).toBe('8.8.8.8');
        });

        test('no dot: backend rejects, frontend normalize returns as-is', () => {
            expect(normalizeGfwDomainRule('localhost')).toBe('localhost');
        });

        test('wildcard then dot: backend strips twice, frontend strips once', () => {
            // Backend: "*. .example.org" → strip "*." → ".example.org" →
            // extractDomainFromAutoProxy sees "." prefix → strips again →
            // "example.org".  Frontend if/else-if chain only strips the
            // first matching prefix, returning ".example.org"; the leading
            // dot then fails validateDomainRule's character regex.
            expect(normalizeGfwDomainRule('*..example.org')).toBe('.example.org');
        });
    });

    describe('additional edge cases', () => {
        test('uppercase is lowercased', () => {
            expect(normalizeGfwDomainRule('Example.ORG')).toBe('example.org');
        });

        test('trailing dot stripped', () => {
            expect(normalizeGfwDomainRule('example.org.')).toBe('example.org');
        });

        test('hash comment rejected', () => {
            expect(normalizeGfwDomainRule('# comment')).toBe('');
        });

        test('regexp line falls through unchanged (validator rejects)', () => {
            // Backend rejects "/.../" via isIgnoredAutoProxyLine.  Frontend
            // has no special-case for it; the leading "/" fails the
            // validator's character regex.
            expect(normalizeGfwDomainRule('/^https?/')).toBe('/^https?/');
        });

        test('whitespace-only returns empty', () => {
            expect(normalizeGfwDomainRule('   ')).toBe('');
        });

        test('extra fields: last field wins', () => {
            expect(normalizeGfwDomainRule('0.0.0.0 tracker.example.com extra')).toBe('extra');
        });
    });
});

describe('isUpdateInProgressError', () => {
    // The API client (Api.makeRequest) flattens server errors into
    // "<url> | <body> | <status>" strings, so detection relies on the
    // trailing status code.
    test('detects 409 conflict suffix', () => {
        const err = new Error('http://localhost/control/gfwlist/update | [object Object] | 409');
        expect(isUpdateInProgressError(err)).toBe(true);
    });

    test('tolerates trailing whitespace', () => {
        const err = new Error('http://localhost/control/gfwlist/update | [object Object] | 409\n');
        expect(isUpdateInProgressError(err)).toBe(true);
    });

    test('rejects other status codes', () => {
        expect(isUpdateInProgressError(new Error('http://x | body | 422'))).toBe(false);
        expect(isUpdateInProgressError(new Error('http://x | body | 500'))).toBe(false);
    });

    test('does not false-positive on 409 inside the URL', () => {
        expect(isUpdateInProgressError(new Error('http://x:4090/control | body | 422'))).toBe(false);
    });

    test('handles missing or non-string message', () => {
        expect(isUpdateInProgressError(new Error())).toBe(false);
        expect(isUpdateInProgressError({} as Error)).toBe(false);
        expect(isUpdateInProgressError(null as unknown as Error)).toBe(false);
    });
});
