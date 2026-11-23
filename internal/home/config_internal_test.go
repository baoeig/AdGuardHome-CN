package home

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AdguardTeam/AdGuardHome/internal/dnsforward"
	"github.com/AdguardTeam/golibs/testutil"
	"github.com/AdguardTeam/golibs/timeutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yaml "go.yaml.in/yaml/v4"
)

func TestConfigFilePath(t *testing.T) {
	const (
		realConf       = "real.yaml"
		linkConf       = "conf.link"
		missingConf    = "missing.yaml"
		brokenLinkConf = "broken.link"
	)

	workDir := t.TempDir()
	targetPath := filepath.Join(workDir, realConf)
	linkPath := filepath.Join(workDir, linkConf)
	missingPath := filepath.Join(workDir, missingConf)
	brokenLinkPath := filepath.Join(workDir, brokenLinkConf)

	err := os.Symlink(targetPath, linkPath)
	require.NoError(t, err)

	err = os.Symlink(missingPath, brokenLinkPath)
	require.NoError(t, err)

	f, err := os.Create(targetPath)
	require.NoError(t, err)

	testutil.CleanupAndRequireSuccess(t, f.Close)

	otherDir := t.TempDir()

	// Canonicalize the absolute path (e.g., on macOS: /var -> /private/var; on
	// Windows: RUNNER~1 -> runneradmin).
	wantAbs := targetPath
	p, err := filepath.EvalSymlinks(wantAbs)
	if err == nil {
		wantAbs = p
	}

	testCases := []struct {
		name     string
		chDir    string
		confPath string
		want     string
	}{{
		name:     "absolute_path",
		chDir:    "",
		confPath: targetPath,
		want:     wantAbs,
	}, {
		name:     "relative_path",
		chDir:    "",
		confPath: realConf,
		want:     targetPath,
	}, {
		name:     "symlink",
		chDir:    "",
		confPath: linkConf,
		want:     linkPath,
	}, {
		name:     "symlink_broken",
		chDir:    "",
		confPath: brokenLinkConf,
		want:     brokenLinkPath,
	}, {
		name:     "symlink_before_join",
		chDir:    otherDir,
		confPath: linkConf,
		want:     linkPath,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.chDir != "" {
				t.Chdir(tc.chDir)
			}

			ctx := testutil.ContextWithTimeout(t, testTimeout)
			got := configFilePath(ctx, testLogger, workDir, tc.confPath)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDNSConfigToInternal_gfwList(t *testing.T) {
	const data = `dns:
  gfwlist:
    enabled: true
    url: https://example.org/gfwlist.txt
    upstream_dns: [1.1.1.1]
    update_interval: 24h
    custom_domains: [example.com]
`
	conf := &configuration{}
	require.NoError(t, yaml.Unmarshal([]byte(data), conf))
	assert.Equal(t, &dnsforward.GFWListConfig{
		Enabled:        true,
		URL:            "https://example.org/gfwlist.txt",
		UpstreamDNS:    []string{"1.1.1.1"},
		UpdateInterval: timeutil.Duration(24 * time.Hour),
		CustomDomains:  []string{"example.com"},
	}, dnsConfigToInternal(&conf.DNS, dnsforward.EmptyClientsContainer{}).GFWList)
}
