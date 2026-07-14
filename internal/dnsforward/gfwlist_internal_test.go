package dnsforward

import (
	"encoding/base64"
	"testing"

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
