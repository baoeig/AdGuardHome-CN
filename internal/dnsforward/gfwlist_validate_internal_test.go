package dnsforward

import (
	"testing"
	"time"

	"github.com/AdguardTeam/golibs/timeutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGFWListConfig_validate_UpstreamDNS(t *testing.T) {
	testCases := []struct {
		name       string
		upstreams  []string
		wantErrMsg string
	}{{
		name:       "no_upstreams_allowed",
		upstreams:  nil,
		wantErrMsg: "",
	}, {
		name:       "valid_plain_ip",
		upstreams:  []string{"8.8.8.8"},
		wantErrMsg: "",
	}, {
		name:       "valid_multiple",
		upstreams:  []string{"8.8.8.8", "1.1.1.1"},
		wantErrMsg: "",
	}, {
		name:      "invalid_upstream",
		upstreams: []string{"not-a-valid-upstream::::"},
		// The exact message comes from dnsproxy; just check our prefix and
		// that the underlying cause is surfaced.
		wantErrMsg: "gfwlist: invalid upstream_dns",
	}, {
		name:       "comment_only_upstream",
		upstreams:  []string{"# no upstreams"},
		wantErrMsg: "gfwlist: upstream_dns must contain at least one plain upstream address",
	}, {
		name:       "domain_specific_upstream",
		upstreams:  []string{"[/example.com/]8.8.8.8"},
		wantErrMsg: "gfwlist: upstream_dns must contain plain upstream addresses only",
	}, {
		name:       "mixed_plain_and_domain_specific_upstreams",
		upstreams:  []string{"8.8.8.8", "[/example.com/]1.1.1.1"},
		wantErrMsg: "gfwlist: upstream_dns must contain plain upstream addresses only",
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			conf := &GFWListConfig{
				Enabled:     true,
				URL:         "https://example.com/list",
				UpstreamDNS: tc.upstreams,
			}
			err := conf.validate()
			if tc.wantErrMsg == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrMsg)
			}
		})
	}
}

func TestGFWListConfig_validate(t *testing.T) {
	testCases := []struct {
		name       string
		conf       *GFWListConfig
		wantErrMsg string
	}{{
		name:       "disabled_skips_validation",
		conf:       &GFWListConfig{Enabled: false, URL: "not a url"},
		wantErrMsg: "",
	}, {
		name: "valid_https",
		conf: &GFWListConfig{
			Enabled:        true,
			URL:            "https://example.com/gfwlist.txt",
			UpdateInterval: timeutil.Duration(24 * time.Hour),
			UpstreamDNS:    []string{"8.8.8.8"},
		},
		wantErrMsg: "",
	}, {
		name:       "empty_url_allowed",
		conf:       &GFWListConfig{Enabled: true, URL: ""},
		wantErrMsg: "",
	}, {
		name:       "invalid_scheme",
		conf:       &GFWListConfig{Enabled: true, URL: "ftp://example.com/list"},
		wantErrMsg: `gfwlist: url scheme must be http or https, got "ftp"`,
	}, {
		name:       "no_host",
		conf:       &GFWListConfig{Enabled: true, URL: "https:///list.txt"},
		wantErrMsg: "gfwlist: url must have a host",
	}, {
		name: "interval_too_short",
		conf: &GFWListConfig{
			Enabled:        true,
			URL:            "https://example.com/list",
			UpdateInterval: timeutil.Duration(time.Minute),
		},
		wantErrMsg: "gfwlist: update interval 1m0s is too short, minimum is 1h0m0s",
	}, {
		name: "interval_too_long",
		conf: &GFWListConfig{
			Enabled:        true,
			URL:            "https://example.com/list",
			UpdateInterval: timeutil.Duration(30 * 24 * time.Hour),
		},
		wantErrMsg: "gfwlist: update interval 720h0m0s is too long, maximum is 168h0m0s",
	}, {
		name: "zero_interval_allowed",
		conf: &GFWListConfig{
			Enabled:        true,
			URL:            "https://example.com/list",
			UpdateInterval: 0,
		},
		wantErrMsg: "",
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.conf.validate()
			if tc.wantErrMsg == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Equal(t, tc.wantErrMsg, err.Error())
			}
		})
	}
}
