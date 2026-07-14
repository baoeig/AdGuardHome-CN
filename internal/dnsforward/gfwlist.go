package dnsforward

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/AdguardTeam/golibs/logutil/slogutil"
	"github.com/AdguardTeam/golibs/timeutil"
)

// GFWListConfig is the configuration for GFW list based DNS split routing.
type GFWListConfig struct {
	// Enabled defines if GFW list split routing is enabled.
	Enabled bool `yaml:"enabled"`

	// URL is the URL of the GFW list.  It must be a valid URL pointing to a
	// base64-encoded AutoProxy format list.
	URL string `yaml:"url"`

	// UpstreamDNS is the list of upstream DNS servers to use for domains
	// matched by the GFW list or custom domains.
	UpstreamDNS []string `yaml:"upstream_dns"`

	// UpdateInterval is the interval between automatic GFW list updates.
	// The default is 24 hours.
	UpdateInterval timeutil.Duration `yaml:"update_interval"`

	// CustomDomains is the list of user-defined domains that should also use
	// the GFW list upstream DNS servers.
	CustomDomains []string `yaml:"custom_domains"`
}

// defaultGFWListUpdateInterval is the default update interval for the GFW list.
const defaultGFWListUpdateInterval = 24 * time.Hour

// gfwlistCacheFile is the filename used to cache the GFW list locally.
const gfwlistCacheFile = "gfwlist_cache.txt"

// gfwlistManager manages the GFW list download, parsing, and domain matching.
type gfwlistManager struct {
	logger *slog.Logger

	// mu protects domains.
	mu sync.RWMutex

	// domains is the set of domains parsed from the GFW list.
	domains map[string]struct{}

	// conf is the current configuration.
	conf *GFWListConfig

	// dataDir is the path to the data directory for caching.
	dataDir string

	// stopCh is used to stop the background updater.
	stopCh chan struct{}
}

// newGFWListManager creates a new gfwlistManager.  l and conf must not be nil.
func newGFWListManager(l *slog.Logger, conf *GFWListConfig, dataDir string) *gfwlistManager {
	return &gfwlistManager{
		logger:  l,
		domains: make(map[string]struct{}),
		conf:    conf,
		dataDir: dataDir,
		stopCh:  make(chan struct{}),
	}
}

// start loads the GFW list (from cache or network) and starts the background
// updater.  It does not block.
func (m *gfwlistManager) start(ctx context.Context) {
	// Try loading from cache first.
	if err := m.loadFromCache(ctx); err != nil {
		m.logger.WarnContext(ctx, "loading gfwlist from cache", slogutil.KeyError, err)
	}

	// Try downloading from network.
	if err := m.update(ctx); err != nil {
		m.logger.WarnContext(ctx, "downloading gfwlist", slogutil.KeyError, err)
	}

	m.logger.InfoContext(ctx, "gfwlist loaded", "domains", m.domainCount())

	// Start background updater.
	interval := time.Duration(m.conf.UpdateInterval)
	if interval <= 0 {
		interval = defaultGFWListUpdateInterval
	}

	go m.backgroundUpdater(interval)
}

// stop stops the background updater.
func (m *gfwlistManager) stop() {
	close(m.stopCh)
}

// backgroundUpdater periodically updates the GFW list.
func (m *gfwlistManager) backgroundUpdater(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			ctx := context.Background()
			if err := m.update(ctx); err != nil {
				m.logger.WarnContext(ctx, "updating gfwlist", slogutil.KeyError, err)
			} else {
				m.logger.InfoContext(ctx, "gfwlist updated", "domains", m.domainCount())
			}
		}
	}
}

// update downloads and parses the GFW list from the configured URL.
func (m *gfwlistManager) update(ctx context.Context) (err error) {
	if m.conf.URL == "" {
		return nil
	}

	m.logger.DebugContext(ctx, "downloading gfwlist", "url", m.conf.URL)

	// Use a timeout for the download.
	dlCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, m.conf.URL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	domains, err := parseGFWList(body)
	if err != nil {
		return fmt.Errorf("parsing gfwlist: %w", err)
	}

	m.mu.Lock()
	m.domains = domains
	m.mu.Unlock()

	// Save to cache.
	if cacheErr := m.saveToCache(ctx, body); cacheErr != nil {
		m.logger.WarnContext(ctx, "saving gfwlist cache", slogutil.KeyError, cacheErr)
	}

	return nil
}

// parseGFWList decodes and parses a base64-encoded AutoProxy format GFW list.
// It returns a set of domains extracted from the list.
func parseGFWList(data []byte) (domains map[string]struct{}, err error) {
	// The GFW list is base64-encoded.
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("base64 decoding: %w", err)
	}

	domains = make(map[string]struct{})
	scanner := bufio.NewScanner(strings.NewReader(string(decoded)))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		domain := extractDomainFromAutoProxy(line)
		if domain != "" {
			domains[domain] = struct{}{}
		}
	}

	if err = scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning lines: %w", err)
	}

	return domains, nil
}

// extractDomainFromAutoProxy extracts a domain name from an AutoProxy format
// rule line.  It returns an empty string if the line is not a domain rule.
//
// Supported formats:
//   - ||domain.com  — domain match (most common)
//   - .domain.com   — domain suffix match
//
// Ignored formats:
//   - ! comment
//   - @@ exception rules
//   - /regexp/
//   - |http:// URL rules
//   - [AutoProxy ...] header
func extractDomainFromAutoProxy(line string) (domain string) {
	// Skip empty lines, comments, exceptions, regexps and header.
	if line == "" ||
		strings.HasPrefix(line, "!") ||
		strings.HasPrefix(line, "@@") ||
		strings.HasPrefix(line, "[") ||
		strings.HasPrefix(line, "/") {
		return ""
	}

	// Handle ||domain.com format (domain match).
	if strings.HasPrefix(line, "||") {
		domain = strings.TrimPrefix(line, "||")
		// Remove any trailing path or characters after the domain.
		if idx := strings.IndexAny(domain, "/^*"); idx >= 0 {
			domain = domain[:idx]
		}

		return normalizeDomain(domain)
	}

	// Handle .domain.com format (domain suffix).
	if strings.HasPrefix(line, ".") {
		domain = strings.TrimPrefix(line, ".")

		return normalizeDomain(domain)
	}

	// Handle |https://domain.com or |http://domain.com URL rules.
	if strings.HasPrefix(line, "|http://") || strings.HasPrefix(line, "|https://") {
		domain = line
		domain = strings.TrimPrefix(domain, "|http://")
		domain = strings.TrimPrefix(domain, "|https://")
		if idx := strings.IndexAny(domain, "/:?"); idx >= 0 {
			domain = domain[:idx]
		}

		return normalizeDomain(domain)
	}

	// Try to extract domain from other patterns that look like plain domains.
	// E.g., lines that are just domain names without any prefix.
	if !strings.ContainsAny(line, "/*\\|@!#") {
		return normalizeDomain(line)
	}

	return ""
}

// normalizeDomain validates and normalizes a domain name.  It returns an empty
// string if the domain is invalid.
func normalizeDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimSuffix(domain, ".")

	if domain == "" {
		return ""
	}

	// Basic validation: must contain at least one dot and no spaces.
	if !strings.Contains(domain, ".") || strings.ContainsAny(domain, " \t") {
		return ""
	}

	// Reject IP addresses.
	if strings.IndexByte(domain, ':') >= 0 {
		return ""
	}

	// Check for valid domain characters.
	for _, r := range domain {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_') {
			return ""
		}
	}

	return domain
}

// domainCount returns the total number of domains (GFW list + custom).
func (m *gfwlistManager) domainCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.domains) + len(m.conf.CustomDomains)
}

// allDomains returns all domains from both the GFW list and custom list.
func (m *gfwlistManager) allDomains() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]string, 0, len(m.domains)+len(m.conf.CustomDomains))
	for d := range m.domains {
		result = append(result, d)
	}

	// Append custom domains, deduplicated.
	for _, d := range m.conf.CustomDomains {
		d = normalizeDomain(d)
		if d == "" {
			continue
		}

		if _, exists := m.domains[d]; !exists {
			result = append(result, d)
		}
	}

	return result
}

// cachePath returns the path to the GFW list cache file.
func (m *gfwlistManager) cachePath() string {
	return filepath.Join(m.dataDir, gfwlistCacheFile)
}

// loadFromCache loads the GFW list from the local cache file.
func (m *gfwlistManager) loadFromCache(ctx context.Context) (err error) {
	data, err := os.ReadFile(m.cachePath())
	if err != nil {
		return fmt.Errorf("reading cache: %w", err)
	}

	domains, err := parseGFWList(data)
	if err != nil {
		return fmt.Errorf("parsing cached gfwlist: %w", err)
	}

	m.mu.Lock()
	m.domains = domains
	m.mu.Unlock()

	m.logger.InfoContext(ctx, "loaded gfwlist from cache", "domains", len(domains))

	return nil
}

// saveToCache saves the raw GFW list data to the local cache file.
func (m *gfwlistManager) saveToCache(_ context.Context, data []byte) (err error) {
	return os.WriteFile(m.cachePath(), data, 0o644)
}

// applyToUpstreamConfig adds GFW list domain routing rules to the given
// upstream configuration.  It creates upstream instances for the configured
// GFW list upstream DNS servers and maps each domain to them using
// DomainReservedUpstreams.
//
// This method does NOT modify any existing upstreams — it only appends new
// domain-specific entries, preserving the original upstream configuration.
func (m *gfwlistManager) applyToUpstreamConfig(
	ctx context.Context,
	uc *proxy.UpstreamConfig,
	opts *upstream.Options,
) (err error) {
	domains := m.allDomains()
	if len(domains) == 0 || len(m.conf.UpstreamDNS) == 0 {
		return nil
	}

	// Build the upstream lines in [/domain/]upstream format and parse them.
	lines := make([]string, 0, len(domains)*len(m.conf.UpstreamDNS))
	for _, d := range domains {
		for _, ups := range m.conf.UpstreamDNS {
			lines = append(lines, fmt.Sprintf("[/%s/]%s", d, ups))
		}
	}

	gfwUC, err := proxy.ParseUpstreamsConfig(lines, opts)
	if err != nil {
		return fmt.Errorf("parsing gfwlist upstreams: %w", err)
	}

	// Merge the GFW list domain upstreams into the existing config.
	if uc.DomainReservedUpstreams == nil {
		uc.DomainReservedUpstreams = make(map[string][]upstream.Upstream)
	}

	for d, ups := range gfwUC.DomainReservedUpstreams {
		uc.DomainReservedUpstreams[d] = append(uc.DomainReservedUpstreams[d], ups...)
	}

	m.logger.InfoContext(
		ctx,
		"applied gfwlist domains to upstream config",
		"domain_count", len(domains),
		"upstream_count", len(m.conf.UpstreamDNS),
	)

	return nil
}

// prepareGFWList initializes the GFW list manager and applies domain routing
// rules to the upstream configuration.  It must be called after
// prepareUpstreamSettings.  This method is independent from the main upstream
// update logic and does not modify it.
func (s *Server) prepareGFWList(ctx context.Context) (err error) {
	conf := s.conf.GFWList
	if conf == nil || !conf.Enabled {
		return nil
	}

	s.logger.InfoContext(ctx, "initializing gfwlist split routing")

	// Stop previous manager if any.
	if s.gfwlist != nil {
		s.gfwlist.stop()
	}

	gfwLogger := s.baseLogger.With(slogutil.KeyPrefix, "gfwlist")
	s.gfwlist = newGFWListManager(gfwLogger, conf, filepath.Dir(s.conf.UpstreamDNSFileName))

	// If dataDir is empty, use a default.
	if s.gfwlist.dataDir == "" || s.gfwlist.dataDir == "." {
		s.gfwlist.dataDir = "data"
	}

	// Load and start background updater.
	s.gfwlist.start(ctx)

	// Apply to upstream config.
	if s.conf.UpstreamConfig != nil {
		opts := &upstream.Options{
			Logger:     s.baseLogger,
			Bootstrap:  s.bootstrap,
			Timeout:    s.conf.UpstreamTimeout,
			PreferIPv6: s.conf.BootstrapPreferIPv6,
		}

		if applyErr := s.gfwlist.applyToUpstreamConfig(ctx, s.conf.UpstreamConfig, opts); applyErr != nil {
			return fmt.Errorf("applying gfwlist to upstream config: %w", applyErr)
		}
	}

	return nil
}
