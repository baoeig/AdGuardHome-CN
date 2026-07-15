package dnsforward

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractDomainFromAutoProxy(t *testing.T) {
	testCases := []struct {
		name string
		line string
		want string
	}{{
		name: "domain_match",
		line: "||google.com",
		want: "google.com",
	}, {
		name: "domain_match_with_caret",
		line: "||youtube.com^",
		want: "youtube.com",
	}, {
		name: "domain_suffix",
		line: ".twitter.com",
		want: "twitter.com",
	}, {
		name: "comment",
		line: "! this is a comment",
		want: "",
	}, {
		name: "exception",
		line: "@@||example.com",
		want: "",
	}, {
		name: "header",
		line: "[AutoProxy 0.2.9]",
		want: "",
	}, {
		name: "empty",
		line: "",
		want: "",
	}, {
		name: "regexp",
		line: "/^https?:\\/\\/[a-z]+\\.example\\.com/",
		want: "",
	}, {
		name: "http_url",
		line: "|https://www.facebook.com/path",
		want: "www.facebook.com",
	}, {
		name: "http_url_no_path",
		line: "|http://blocked.site.org",
		want: "blocked.site.org",
	}, {
		name: "domain_with_subdomain",
		line: "||apis.google.com",
		want: "apis.google.com",
	}, {
		name: "domain_with_wildcard",
		line: "||*.example.org",
		want: "",
	}, {
		name: "plain_domain",
		line: "example.org",
		want: "example.org",
	}, {
		name: "domain_with_path",
		line: "||cdn.example.com/script.js",
		want: "cdn.example.com",
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractDomainFromAutoProxy(tc.line)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNormalizeDomain(t *testing.T) {
	testCases := []struct {
		name   string
		domain string
		want   string
	}{{
		name:   "simple",
		domain: "google.com",
		want:   "google.com",
	}, {
		name:   "uppercase",
		domain: "Google.COM",
		want:   "google.com",
	}, {
		name:   "trailing_dot",
		domain: "example.com.",
		want:   "example.com",
	}, {
		name:   "no_dot",
		domain: "localhost",
		want:   "",
	}, {
		name:   "empty",
		domain: "",
		want:   "",
	}, {
		name:   "with_space",
		domain: "exam ple.com",
		want:   "",
	}, {
		name:   "ipv6_like",
		domain: "::1",
		want:   "",
	}, {
		name:   "ipv4_address",
		domain: "1.2.3.4",
		want:   "",
	}, {
		name:   "ipv4_address_with_port",
		domain: "8.8.8.8",
		want:   "",
	}, {
		name:   "subdomain",
		domain: "sub.domain.example.com",
		want:   "sub.domain.example.com",
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeDomain(tc.domain)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNormalizeGFWDomainRule(t *testing.T) {
	testCases := []struct {
		name string
		rule string
		want string
	}{{
		name: "plain",
		rule: "example.org",
		want: "example.org",
	}, {
		name: "wildcard",
		rule: "*.example.org",
		want: "example.org",
	}, {
		name: "adblock",
		rule: "||example.org^",
		want: "example.org",
	}, {
		name: "url",
		rule: "|https://www.example.org/path",
		want: "www.example.org",
	}, {
		name: "hosts",
		rule: "127.0.0.1 example.org",
		want: "example.org",
	}, {
		name: "comment",
		rule: "! example.org",
		want: "",
	}, {
		name: "exception",
		rule: "@@||example.org^",
		want: "",
	}, {
		name: "ip",
		rule: "8.8.8.8",
		want: "",
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, normalizeGFWDomainRule(tc.rule))
		})
	}
}

func TestGFWListManagerCheckDomain(t *testing.T) {
	conf := &GFWListConfig{
		CustomDomains: []string{"custom.example.net"},
	}
	m := newGFWListManager(testLogger, conf, t.TempDir(), nil)
	m.domains["example.org"] = struct{}{}

	matched, source := m.checkDomain("sub.example.org")
	assert.True(t, matched)
	assert.Equal(t, "gfwlist", source)

	matched, source = m.checkDomain("custom.example.net")
	assert.True(t, matched)
	assert.Equal(t, "custom", source)

	matched, source = m.checkDomain("sub.custom.example.net")
	assert.True(t, matched)
	assert.Equal(t, "custom", source)

	matched, source = m.checkDomain("other.example.net")
	assert.False(t, matched)
	assert.Empty(t, source)
}

func TestGFWListManagerDomainCountDeduplicatesCustomDomains(t *testing.T) {
	conf := &GFWListConfig{
		CustomDomains: []string{"example.org", "custom.example.net"},
	}
	m := newGFWListManager(testLogger, conf, t.TempDir(), nil)
	m.domains["example.org"] = struct{}{}

	assert.Equal(t, 2, m.domainCount())
}

func TestGFWListManagerStartUsesMemorySnapshot(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(
		filepath.Join(dir, gfwlistCacheFile),
		[]byte("not base64"),
		0o600,
	)
	require.NoError(t, err)

	m := newGFWListManager(testLogger, &GFWListConfig{}, dir, nil)
	m.start(t.Context(), map[string]struct{}{
		"example.org": {},
	})
	t.Cleanup(m.stop)

	assert.Equal(t, 1, m.domainCount())
}

func TestGFWListManagerApplyToUpstreamConfig(t *testing.T) {
	conf := &GFWListConfig{
		UpstreamDNS:   []string{"8.8.8.8", "1.1.1.1"},
		CustomDomains: []string{"example.org", "custom.example.net"},
	}
	m := newGFWListManager(testLogger, conf, t.TempDir(), nil)
	m.domains["example.org"] = struct{}{}
	m.domains["example.com"] = struct{}{}

	uc := &proxy.UpstreamConfig{}
	err := m.applyToUpstreamConfig(t.Context(), uc, &upstream.Options{
		Logger: testLogger,
	})
	require.NoError(t, err)

	for _, domain := range []string{
		"example.com.",
		"example.org.",
		"custom.example.net.",
	} {
		assert.Len(t, uc.DomainReservedUpstreams[domain], 2)
		assert.Len(t, uc.SpecifiedDomainUpstreams[domain], 2)
	}
}

func TestParseGFWList(t *testing.T) {
	// Simulate a small base64-encoded AutoProxy list.
	rawContent := `[AutoProxy 0.2.9]
! Title: Test GFWList
! Last Modified: Thu, 01 Jan 2026 00:00:00 +0000

||google.com
||youtube.com^
.twitter.com
@@||allowed.example.com
! comment line
||facebook.com
|https://www.instagram.com/path
`
	// Base64 encode it.
	encoded := make([]byte, 0, len(rawContent)*2)
	encoded = append(encoded, []byte(base64Encode(rawContent))...)

	domains, err := parseGFWList(encoded)
	require.NoError(t, err)

	expectedDomains := []string{
		"google.com",
		"youtube.com",
		"twitter.com",
		"facebook.com",
		"www.instagram.com",
	}

	for _, d := range expectedDomains {
		_, exists := domains[d]
		assert.Truef(t, exists, "expected domain %s to be in parsed list", d)
	}

	// Ensure exception rules are NOT included.
	_, exists := domains["allowed.example.com"]
	assert.False(t, exists, "exception domain should not be in parsed list")
}

// base64Encode is a helper for testing.
func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
