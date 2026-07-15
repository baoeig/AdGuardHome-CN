package dnsforward

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/AdguardTeam/golibs/logutil/slogutil"
	"github.com/AdguardTeam/golibs/netutil"
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

// maxGFWListSize is the maximum accepted GFW list response size.
const maxGFWListSize = 16 * 1024 * 1024

// gfwlistManager manages the GFW list download, parsing, and domain matching.
type gfwlistManager struct {
	logger *slog.Logger

	// mu protects domains.
	mu sync.RWMutex

	// domains is the set of domains parsed from the GFW list.
	domains map[string]struct{}

	// customDomains is the normalized set of user-defined domains.
	customDomains map[string]struct{}

	// conf is the current configuration.
	conf *GFWListConfig

	// dataDir is the path to the data directory for caching.
	dataDir string

	// stopCh is used to stop the background updater.
	stopCh chan struct{}

	// stopOnce ensures stopCh is closed only once.
	stopOnce sync.Once

	// onUpdate is called after a successful background update.
	onUpdate func(ctx context.Context, domains map[string]struct{})
}

// newGFWListManager creates a new gfwlistManager.  l and conf must not be nil.
func newGFWListManager(
	l *slog.Logger,
	conf *GFWListConfig,
	dataDir string,
	onUpdate func(ctx context.Context, domains map[string]struct{}),
) *gfwlistManager {
	conf = cloneGFWListConfig(conf)

	return &gfwlistManager{
		logger:        l,
		domains:       make(map[string]struct{}),
		customDomains: normalizeGFWDomainRules(conf.CustomDomains),
		conf:          conf,
		dataDir:       dataDir,
		stopCh:        make(chan struct{}),
		onUpdate:      onUpdate,
	}
}

// cloneGFWListConfig returns an independent copy of conf.
func cloneGFWListConfig(conf *GFWListConfig) (clone *GFWListConfig) {
	return &GFWListConfig{
		Enabled:        conf.Enabled,
		URL:            conf.URL,
		UpstreamDNS:    slices.Clone(conf.UpstreamDNS),
		UpdateInterval: conf.UpdateInterval,
		CustomDomains:  slices.Clone(conf.CustomDomains),
	}
}

// cloneGFWListDomains returns an independent copy of domains.
func cloneGFWListDomains(domains map[string]struct{}) (clone map[string]struct{}) {
	clone = make(map[string]struct{}, len(domains))
	for d := range domains {
		clone[d] = struct{}{}
	}

	return clone
}

// normalizeGFWDomainRules returns the normalized domain set for rules.
func normalizeGFWDomainRules(rules []string) (domains map[string]struct{}) {
	domains = make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		domain := normalizeGFWDomainRule(rule)
		if domain != "" {
			domains[domain] = struct{}{}
		}
	}

	return domains
}

// setDomains replaces m's downloaded GFW list domains.  It takes ownership of
// domains, which must not be mutated after calling setDomains.
func (m *gfwlistManager) setDomains(domains map[string]struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.domains = domains
}

// domainSnapshot returns a copy of m's downloaded GFW list domains.
func (m *gfwlistManager) domainSnapshot() (domains map[string]struct{}) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return cloneGFWListDomains(m.domains)
}

// start initializes domains and starts the background updater.  It does not
// block; network downloads are handled by the background updater goroutine.
func (m *gfwlistManager) start(ctx context.Context, domains map[string]struct{}) {
	if domains != nil {
		m.setDomains(domains)
		m.logger.InfoContext(ctx, "gfwlist loaded from memory", "domains", len(domains))
	} else {
		// Try loading from cache first so domains are available immediately.
		if err := m.loadFromCache(ctx); err != nil {
			m.logger.WarnContext(ctx, "loading gfwlist from cache", slogutil.KeyError, err)
		}
	}

	m.logger.InfoContext(ctx, "gfwlist loaded", "domains", m.domainCount())

	// Start background updater.  The current cache is applied during
	// preparation; later successful updates trigger a reconfigure through
	// m.onUpdate.
	interval := time.Duration(m.conf.UpdateInterval)
	if interval <= 0 {
		interval = defaultGFWListUpdateInterval
	}

	go m.backgroundUpdater(context.WithoutCancel(ctx), interval)
}

// stop stops the background updater.
func (m *gfwlistManager) stop() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
}

// backgroundUpdater downloads the GFW list on every interval tick.
func (m *gfwlistManager) backgroundUpdater(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			domains, err := m.update(ctx)
			if err != nil {
				m.logger.WarnContext(ctx, "updating gfwlist", slogutil.KeyError, err)
			} else {
				m.logger.InfoContext(ctx, "gfwlist updated", "domains", m.domainCount())
				if m.onUpdate != nil {
					select {
					case <-m.stopCh:
						return
					default:
						m.onUpdate(ctx, domains)
					}
				}
			}
		}
	}
}

// update downloads and parses the GFW list from the configured URL.
func (m *gfwlistManager) update(ctx context.Context) (domains map[string]struct{}, err error) {
	if m.conf.URL == "" {
		return m.domainSnapshot(), nil
	}

	m.logger.DebugContext(ctx, "downloading gfwlist", "url", m.conf.URL)

	// Use a timeout for the download.
	dlCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, m.conf.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGFWListSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if len(body) > maxGFWListSize {
		return nil, fmt.Errorf("response is too large")
	}

	domains, err = parseGFWList(body)
	if err != nil {
		return nil, fmt.Errorf("parsing gfwlist: %w", err)
	}

	m.setDomains(domains)

	// Save to cache.
	if cacheErr := m.saveToCache(ctx, body); cacheErr != nil {
		m.logger.WarnContext(ctx, "saving gfwlist cache", slogutil.KeyError, cacheErr)
	}

	return cloneGFWListDomains(domains), nil
}

// parseGFWList decodes and parses a base64-encoded AutoProxy format GFW list.
// It returns a set of domains extracted from the list.
func parseGFWList(data []byte) (domains map[string]struct{}, err error) {
	// The GFW list is base64-encoded.
	encoded := bytes.TrimSpace(data)
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	n, err := base64.StdEncoding.Decode(decoded, encoded)
	if err != nil {
		return nil, fmt.Errorf("base64 decoding: %w", err)
	}
	decoded = decoded[:n]

	domains = make(map[string]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(decoded))

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
//   - |http://domain.com or |https://domain.com — URL rules
//   - plain domain name without prefix
//
// Ignored formats:
//   - ! comment
//   - @@ exception rules
//   - /regexp/
//   - [AutoProxy ...] header

// isIgnoredAutoProxyLine reports whether line should be skipped when parsing
// an AutoProxy format GFW list.  It returns true for empty lines, comments,
// exception rules, regexp rules, and the AutoProxy header.
func isIgnoredAutoProxyLine(line string) bool {
	return line == "" ||
		strings.HasPrefix(line, "!") ||
		strings.HasPrefix(line, "@@") ||
		strings.HasPrefix(line, "[") ||
		strings.HasPrefix(line, "/")
}

// extractDomainFromURLRule extracts the hostname from a |http:// or |https://
// style AutoProxy rule.  It returns an empty string if the domain is invalid.
func extractDomainFromURLRule(line string) string {
	domain := strings.TrimPrefix(line, "|http://")
	domain = strings.TrimPrefix(domain, "|https://")
	if idx := strings.IndexAny(domain, "/:?"); idx >= 0 {
		domain = domain[:idx]
	}

	return normalizeDomain(domain)
}

// normalizeGFWDomainRule normalizes a user-entered GFW list custom rule.  It
// accepts plain domains, wildcard domains, common adblock-style domain rules,
// URL rules, and simple hosts-file lines.
func normalizeGFWDomainRule(rule string) (domain string) {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return ""
	}
	if strings.HasPrefix(rule, "!") ||
		strings.HasPrefix(rule, "#") ||
		strings.HasPrefix(rule, "@@") ||
		strings.HasPrefix(rule, "[") {
		return ""
	}

	fields := strings.Fields(rule)
	if len(fields) > 1 {
		rule = fields[len(fields)-1]
	}

	rule = strings.TrimPrefix(rule, "*.")

	return extractDomainFromAutoProxy(rule)
}

func extractDomainFromAutoProxy(line string) (domain string) {
	// Skip empty lines, comments, exceptions, regexps and header.
	if isIgnoredAutoProxyLine(line) {
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
		return extractDomainFromURLRule(line)
	}

	// Try to extract domain from other patterns that look like plain domains.
	// E.g., lines that are just domain names without any prefix.
	if !strings.ContainsAny(line, "/*\\|@!#") {
		return normalizeDomain(line)
	}

	return ""
}

func isValidDomainChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_'
}

// normalizeDomain validates and normalizes a domain name.  It returns an empty
// string if the domain is invalid or is an IP address.
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

	// Reject both IPv4 and IPv6 addresses using net.ParseIP.
	if net.ParseIP(domain) != nil {
		return ""
	}

	// Check for valid domain characters.
	for _, r := range domain {
		if !isValidDomainChar(r) {
			return ""
		}
	}

	return domain
}

// domainCount returns the total number of domains (GFW list + custom).
func (m *gfwlistManager) domainCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := len(m.domains)
	for d := range m.customDomains {
		if _, exists := m.domains[d]; !exists {
			count++
		}
	}

	return count
}

// hasDomains reports whether m has any GFW list or custom domains.
func (m *gfwlistManager) hasDomains() (ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.domains) > 0 || len(m.customDomains) > 0
}

// matchDomainInSet reports whether domain exactly matches or is a subdomain of
// a domain in set.
func matchDomainInSet(domain string, set map[string]struct{}) (ok bool) {
	for {
		if _, ok = set[domain]; ok {
			return true
		}

		i := strings.IndexByte(domain, '.')
		if i < 0 {
			return false
		}

		domain = domain[i+1:]
	}
}

// checkDomain returns true if domain is found in the GFW list or custom domain
// list.  source is "gfwlist", "custom", or empty.
func (m *gfwlistManager) checkDomain(domain string) (matched bool, source string) {
	domain = normalizeDomain(domain)
	if domain == "" {
		return false, ""
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if matchDomainInSet(domain, m.domains) {
		return true, "gfwlist"
	}

	if matchDomainInSet(domain, m.customDomains) {
		return true, "custom"
	}

	return false, ""
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

	m.setDomains(domains)

	m.logger.InfoContext(ctx, "loaded gfwlist from cache", "domains", len(domains))

	return nil
}

// saveToCache saves the raw GFW list data to the local cache file.
func (m *gfwlistManager) saveToCache(_ context.Context, data []byte) (err error) {
	err = os.MkdirAll(m.dataDir, 0o700)
	if err != nil {
		return fmt.Errorf("creating cache dir: %w", err)
	}

	return os.WriteFile(m.cachePath(), data, 0o600)
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
	if len(m.conf.UpstreamDNS) == 0 || !m.hasDomains() {
		return nil
	}

	gfwUC, err := proxy.ParseUpstreamsConfig(m.conf.UpstreamDNS, opts)
	if err != nil {
		return fmt.Errorf("parsing gfwlist upstreams: %w", err)
	}
	if len(gfwUC.Upstreams) == 0 {
		return fmt.Errorf("gfwlist upstreams must be plain upstream addresses")
	}

	// Merge the GFW list domain upstreams into the existing config.
	if uc.DomainReservedUpstreams == nil {
		uc.DomainReservedUpstreams = make(map[string][]upstream.Upstream)
	}
	if uc.SpecifiedDomainUpstreams == nil {
		uc.SpecifiedDomainUpstreams = make(map[string][]upstream.Upstream)
	}

	domainCount, err := m.appendToUpstreamConfig(uc, gfwUC.Upstreams)
	if err != nil {
		return err
	}

	m.logger.InfoContext(
		ctx,
		"applied gfwlist domains to upstream config",
		"domain_count", domainCount,
		"upstream_count", len(m.conf.UpstreamDNS),
	)

	return nil
}

// appendToUpstreamConfig appends all GFW list and custom domains to uc.
func (m *gfwlistManager) appendToUpstreamConfig(
	uc *proxy.UpstreamConfig,
	ups []upstream.Upstream,
) (domainCount int, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	appendDomain := func(domain string) (err error) {
		key, keyErr := domainReservedUpstreamKey(domain)
		if keyErr != nil {
			return fmt.Errorf("preparing gfwlist domain %q: %w", domain, keyErr)
		}

		uc.DomainReservedUpstreams[key] = append(uc.DomainReservedUpstreams[key], ups...)
		uc.SpecifiedDomainUpstreams[key] = append(uc.SpecifiedDomainUpstreams[key], ups...)

		domainCount++

		return nil
	}

	for d := range m.domains {
		if err = appendDomain(d); err != nil {
			return 0, err
		}
	}
	for d := range m.customDomains {
		if _, exists := m.domains[d]; exists {
			continue
		}

		if err = appendDomain(d); err != nil {
			return 0, err
		}
	}

	return domainCount, nil
}

// domainReservedUpstreamKey returns a domain key in the format used by
// proxy.UpstreamConfig domain-specific upstream maps.
func domainReservedUpstreamKey(domain string) (key string, err error) {
	if err = netutil.ValidateDomainName(domain); err != nil {
		return "", err
	}

	return domain + ".", nil
}

// prepareGFWList initializes the GFW list manager and applies domain routing
// rules to the upstream configuration.  It must be called after
// prepareUpstreamSettings.  This method is independent from the main upstream
// update logic and does not modify it.
func (s *Server) prepareGFWList(ctx context.Context) (err error) {
	conf := s.conf.GFWList
	if conf == nil || !conf.Enabled {
		s.pendingGFWListDomains = nil
		if s.gfwlist != nil {
			s.gfwlist.stop()
			s.gfwlist = nil
		}

		return nil
	}

	s.logger.InfoContext(ctx, "initializing gfwlist split routing")

	// Stop previous manager if any.
	if s.gfwlist != nil {
		s.gfwlist.stop()
	}

	// Determine the data directory for caching.  Prefer the directory of the
	// upstream DNS config file; fall back to "data" if it is empty or "."
	// (which happens when UpstreamDNSFileName is a bare filename with no path).
	dataDir := filepath.Dir(s.conf.UpstreamDNSFileName)
	if dataDir == "" || dataDir == "." {
		dataDir = "data"
	}

	gfwLogger := s.baseLogger.With(slogutil.KeyPrefix, "gfwlist")
	var gfwMgr *gfwlistManager
	gfwMgr = newGFWListManager(gfwLogger, conf, dataDir, func(ctx context.Context, domains map[string]struct{}) {
		if !s.setPendingGFWListDomains(gfwMgr, domains) {
			return
		}

		s.logger.InfoContext(ctx, "reconfiguring after gfwlist update")

		if reconfigureErr := s.Reconfigure(ctx, nil); reconfigureErr != nil {
			s.logger.ErrorContext(
				ctx,
				"reconfiguring after gfwlist update",
				slogutil.KeyError,
				reconfigureErr,
			)
		}
	})
	s.gfwlist = gfwMgr

	domains := s.pendingGFWListDomains
	s.pendingGFWListDomains = nil

	// Load and start background updater.
	s.gfwlist.start(ctx, domains)

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

// setPendingGFWListDomains stores domains for the next GFW list prepare.  It
// takes ownership of domains.
func (s *Server) setPendingGFWListDomains(
	gfwMgr *gfwlistManager,
	domains map[string]struct{},
) (ok bool) {
	s.serverLock.Lock()
	defer s.serverLock.Unlock()

	if s.gfwlist != gfwMgr {
		return false
	}

	s.pendingGFWListDomains = domains

	return true
}
