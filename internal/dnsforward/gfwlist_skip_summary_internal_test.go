package dnsforward

import (
	"strings"
	"testing"

	"github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGFWListManagerAppendToUpstreamConfig_SkippedCount verifies that
// appendToUpstreamConfig reports how many domains were skipped due to
// validation failure, instead of only logging one warning per invalid
// domain.  This lets applyToUpstreamConfig emit a single summary log line
// even when the upstream GFW list contains many invalid entries.
func TestGFWListManagerAppendToUpstreamConfig_SkippedCount(t *testing.T) {
	conf := &GFWListConfig{
		UpstreamDNS: []string{"8.8.8.8"},
	}
	m := newGFWListManager(testLogger, conf, t.TempDir(), nil)

	overlong := strings.Repeat("a", 64)
	m.setDomains(map[string]struct{}{
		"good1.example.com":         {},
		"good2.example.com":         {},
		overlong + ".example.com":   {},
		overlong + ".2.example.com": {},
		overlong + ".3.example.com": {},
	})

	uc := &proxy.UpstreamConfig{
		DomainReservedUpstreams:  map[string][]upstream.Upstream{},
		SpecifiedDomainUpstreams: map[string][]upstream.Upstream{},
	}
	ups, err := proxy.ParseUpstreamsConfig(conf.UpstreamDNS, &upstream.Options{Logger: testLogger})
	require.NoError(t, err)

	domainCount, skippedCount := m.appendToUpstreamConfig(uc, ups.Upstreams)

	assert.Equal(t, 2, domainCount)
	assert.Equal(t, 3, skippedCount)
}
