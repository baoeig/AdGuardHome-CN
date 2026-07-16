package dnsforward

import (
	"strings"
	"testing"

	"github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGFWListManagerApplyToUpstreamConfig_InvalidDomainSkipped verifies that a
// single invalid domain in the GFW list does not prevent valid domains from
// being applied to the upstream configuration, and does not leave the
// upstream config in a partially-populated state on error.
func TestGFWListManagerApplyToUpstreamConfig_InvalidDomainSkipped(t *testing.T) {
	conf := &GFWListConfig{
		UpstreamDNS: []string{"8.8.8.8"},
	}
	m := newGFWListManager(testLogger, conf, t.TempDir(), nil)
	// A label longer than 63 octets is accepted by normalizeDomain (which
	// only checks character classes) but rejected by
	// netutil.ValidateDomainName (RFC 1035 label length limit), exercising
	// the appendDomain error path inside appendToUpstreamConfig without
	// needing to fake validation.
	overlongLabel := strings.Repeat("a", 64)
	badDomain := overlongLabel + ".example.com"
	m.setDomains(map[string]struct{}{
		"good.example.com": {},
		badDomain:          {},
	})

	uc := &proxy.UpstreamConfig{}
	err := m.applyToUpstreamConfig(t.Context(), uc, &upstream.Options{
		Logger: testLogger,
	})
	require.NoError(t, err)

	// The valid domain must still be present.
	assert.Contains(t, uc.DomainReservedUpstreams, "good.example.com.")
	assert.Contains(t, uc.SpecifiedDomainUpstreams, "good.example.com.")

	// The invalid domain must not have been added.
	assert.NotContains(t, uc.DomainReservedUpstreams, badDomain+".")
	assert.NotContains(t, uc.SpecifiedDomainUpstreams, badDomain+".")
}
