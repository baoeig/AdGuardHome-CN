package dnsforward

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/AdguardTeam/AdGuardHome/internal/aghrenameio"
	"github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/AdguardTeam/golibs/errors"
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

	// customDomainsSet caches normalized custom domains for fast deduplication.
	customDomainsSet map[string]struct{} `yaml:"-"`
}

// defaultGFWListUpdateInterval is the default update interval for the GFW list.
const defaultGFWListUpdateInterval = 24 * time.Hour

// minGFWListUpdateInterval and maxGFWListUpdateInterval bound the configurable
// update interval to prevent request floods and excessively stale lists.
const (
	minGFWListUpdateInterval = time.Hour
	maxGFWListUpdateInterval = 7 * 24 * time.Hour
)

// validate checks that conf is a valid GFW list configuration.  It returns nil
// if conf is disabled.
func (conf *GFWListConfig) validate() (err error) {
	if !conf.Enabled {
		return nil
	}

	if err = validateGFWListURL(conf.URL); err != nil {
		return err
	}

	if err = validateGFWListUpstreams(conf.UpstreamDNS); err != nil {
		return err
	}

	return validateGFWListUpdateInterval(time.Duration(conf.UpdateInterval))
}

// validateGFWListURL validates urlStr if it is not empty.
func validateGFWListURL(urlStr string) (err error) {
	if urlStr == "" {
		return nil
	}

	u, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("gfwlist: invalid url: %w", err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("gfwlist: url scheme must be http or https, got %q", u.Scheme)
	}

	if u.Host == "" {
		return errors.Error("gfwlist: url must have a host")
	}

	return nil
}

// validateGFWListUpstreams validates upstreams if any are configured.
func validateGFWListUpstreams(upstreams []string) (err error) {
	if len(upstreams) == 0 {
		return nil
	}

	conf, err := proxy.ParseUpstreamsConfig(upstreams, &upstream.Options{})
	if err != nil {
		return fmt.Errorf("gfwlist: invalid upstream_dns: %w", err)
	}
	if len(conf.DomainReservedUpstreams) != 0 ||
		len(conf.SpecifiedDomainUpstreams) != 0 ||
		conf.SubdomainExclusions.Len() != 0 {
		return errors.Error("gfwlist: upstream_dns must contain plain upstream addresses only")
	}
	if len(conf.Upstreams) == 0 {
		return errors.Error("gfwlist: upstream_dns must contain at least one plain upstream address")
	}

	return nil
}

// validateGFWListUpdateInterval validates interval if it is not zero.
func validateGFWListUpdateInterval(interval time.Duration) (err error) {
	if interval == 0 {
		return nil
	}

	if interval < minGFWListUpdateInterval {
		return fmt.Errorf(
			"gfwlist: update interval %s is too short, minimum is %s",
			interval,
			minGFWListUpdateInterval,
		)
	}

	if interval > maxGFWListUpdateInterval {
		return fmt.Errorf(
			"gfwlist: update interval %s is too long, maximum is %s",
			interval,
			maxGFWListUpdateInterval,
		)
	}

	return nil
}

// gfwlistCacheFile is the filename used to cache the GFW list locally.
const gfwlistCacheFile = "gfwlist_cache.txt"

// maxGFWListSize is the maximum accepted GFW list response size.
const maxGFWListSize = 16 * 1024 * 1024

// gfwlistManager manages the GFW list download, parsing, and domain matching.
type gfwlistManager struct {
	logger *slog.Logger

	// mu protects domains.
	mu sync.RWMutex

	// updateMu serializes calls to update.  It prevents the background
	// updater and the manual HTTP trigger from downloading at the same
	// time.
	updateMu sync.Mutex

	// domains is the set of domains parsed from the GFW list.
	domains map[string]struct{}

	// customDomains is the normalized set of user-defined domains.
	customDomains map[string]struct{}

	// totalDomainCount is the total number of GFW list and custom domains.
	totalDomainCount int

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
	customDomains := normalizeGFWDomainRules(conf.CustomDomains)

	return &gfwlistManager{
		logger:           l,
		domains:          make(map[string]struct{}),
		customDomains:    customDomains,
		totalDomainCount: len(customDomains),
		conf:             conf,
		dataDir:          dataDir,
		stopCh:           make(chan struct{}),
		onUpdate:         onUpdate,
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

// ensureCustomDomainsSet normalizes custom domains once and caches the result.
// It returns the cached set for fast deduplication.
func (conf *GFWListConfig) ensureCustomDomainsSet() (domains map[string]struct{}) {
	if conf.customDomainsSet != nil {
		return conf.customDomainsSet
	}

	domains = make(map[string]struct{}, len(conf.CustomDomains))
	normalized := make([]string, 0, len(conf.CustomDomains))
	for _, rule := range conf.CustomDomains {
		domain := normalizeGFWDomainRule(rule)
		if domain == "" {
			continue
		}

		if _, ok := domains[domain]; ok {
			continue
		}

		domains[domain] = struct{}{}
		normalized = append(normalized, domain)
	}

	conf.CustomDomains = normalized
	conf.customDomainsSet = domains

	return domains
}

// addCustomDomains adds normalized domains to the configuration.  It returns
// the number of newly added domains.
func (conf *GFWListConfig) addCustomDomains(domains []string) (added int) {
	existing := conf.ensureCustomDomainsSet()
	for _, rule := range domains {
		domain := normalizeGFWDomainRule(rule)
		if domain == "" {
			continue
		}

		if _, ok := existing[domain]; ok {
			continue
		}

		conf.CustomDomains = append(conf.CustomDomains, domain)
		existing[domain] = struct{}{}
		added++
	}

	return added
}

// removeCustomDomains removes normalized domains from the configuration.  It
// returns the number of removed domains.
func (conf *GFWListConfig) removeCustomDomains(domains []string) (removed int) {
	existing := conf.ensureCustomDomainsSet()
	toRemove := make(map[string]struct{}, len(domains))
	for _, rule := range domains {
		domain := normalizeGFWDomainRule(rule)
		if domain != "" {
			toRemove[domain] = struct{}{}
		}
	}

	if len(toRemove) == 0 {
		return 0
	}

	filtered := conf.CustomDomains[:0]
	for _, domain := range conf.CustomDomains {
		if _, ok := toRemove[domain]; ok {
			delete(existing, domain)
			removed++
			continue
		}

		filtered = append(filtered, domain)
	}

	conf.CustomDomains = filtered

	return removed
}

// setDomains replaces m's downloaded GFW list domains.  It takes ownership of
// domains, which must not be mutated after calling setDomains.
func (m *gfwlistManager) setDomains(domains map[string]struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.domains = domains

	m.totalDomainCount = len(domains)
	for d := range m.customDomains {
		if _, exists := m.domains[d]; !exists {
			m.totalDomainCount++
		}
	}
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

	if m.conf.URL == "" {
		return
	}

	// Start background updater.  The current cache is applied during
	// preparation; later successful updates trigger a reconfigure through
	// m.onUpdate.
	interval := time.Duration(m.conf.UpdateInterval)
	if interval <= 0 {
		interval = defaultGFWListUpdateInterval
	}

	delay := gfwListStartupDelay(domains != nil, interval)

	go m.backgroundUpdater(context.WithoutCancel(ctx), delay, interval)
}

// gfwListMaxInitialDelay bounds the random startup jitter before the first GFW
// list download.
const gfwListMaxInitialDelay = 30 * time.Second

// gfwListStartupDelay returns the delay before the background updater's first
// download.
//
// If hasMemorySnapshot is true, the manager was just handed a freshly
// downloaded in-memory domain set — typically right after a manual "update
// now" request triggered a Reconfigure.  In that case the first download is
// deferred to the next full interval, to avoid downloading the same list
// twice in quick succession.
//
// Otherwise (fresh install or domains loaded from the on-disk cache), the
// first download happens shortly after startup using a bounded random
// jitter in [0, gfwListMaxInitialDelay), so a fresh install without cache
// does not stay empty for a long time, while the jitter avoids a thundering
// herd of simultaneous requests across many instances.
func gfwListStartupDelay(hasMemorySnapshot bool, interval time.Duration) (d time.Duration) {
	if hasMemorySnapshot {
		return interval
	}

	maxDelay := big.NewInt(int64(gfwListMaxInitialDelay))
	delay, err := rand.Int(rand.Reader, maxDelay)
	if err != nil {
		return 0
	}

	return time.Duration(delay.Int64())
}

// stop stops the background updater.
func (m *gfwlistManager) stop() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
}

// backgroundUpdater performs an initial download after initialDelay, then
// downloads the GFW list on every interval tick.  It returns when the manager
// is stopped or ctx is done.
func (m *gfwlistManager) backgroundUpdater(
	ctx context.Context,
	initialDelay time.Duration,
	interval time.Duration,
) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-m.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	timer := time.NewTimer(initialDelay)
	defer timer.Stop()

	select {
	case <-m.stopCh:
		return
	case <-ctx.Done():
		return
	case <-timer.C:
		m.handleBackgroundUpdate(ctx)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.handleBackgroundUpdate(ctx)
		}
	}
}

// handleBackgroundUpdate runs a single update tick.
func (m *gfwlistManager) handleBackgroundUpdate(ctx context.Context) {
	domains, err := m.update(ctx)
	if errors.Is(err, errGFWListUpdateInProgress) {
		m.logger.DebugContext(ctx, "skipping gfwlist tick; update already in progress")

		return
	}
	if err != nil {
		m.logger.WarnContext(ctx, "updating gfwlist", slogutil.KeyError, err)

		return
	}

	m.logger.InfoContext(ctx, "gfwlist updated", "domains", m.domainCount())
	if m.onUpdate != nil {
		m.notifyUpdate(ctx, domains)
	}
}

// notifyUpdate invokes the post-update callback unless the manager is stopping.
func (m *gfwlistManager) notifyUpdate(ctx context.Context, domains map[string]struct{}) {
	select {
	case <-m.stopCh:
		return
	default:
		m.onUpdate(ctx, domains)
	}
}

// gfwListDownloadRetries is the number of download attempts per update.
const gfwListDownloadRetries = 3

// gfwListBaseBackoff is the initial backoff between download retries.  It grows
// exponentially on each subsequent attempt.
const gfwListBaseBackoff = time.Second

// downloadWithRetry calls download up to maxAttempts times, waiting with
// exponential backoff between attempts starting at baseBackoff.  It stops early
// if ctx is cancelled.  logger and download must not be nil.
func downloadWithRetry(
	ctx context.Context,
	logger *slog.Logger,
	baseBackoff time.Duration,
	maxAttempts int,
	download func(ctx context.Context) (body []byte, err error),
) (body []byte, err error) {
	backoff := baseBackoff
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		body, err = download(ctx)
		if err == nil {
			return body, nil
		}

		if attempt == maxAttempts {
			break
		}

		logger.WarnContext(
			ctx,
			"gfwlist download failed, retrying",
			"attempt", attempt,
			"max_attempts", maxAttempts,
			slogutil.KeyError, err,
		)

		select {
		case <-time.After(backoff):
			backoff *= 2
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return nil, fmt.Errorf("after %d attempts: %w", maxAttempts, err)
}

// downloadOnce performs a single GFW list download attempt from the configured
// URL, returning the raw response body.
func (m *gfwlistManager) downloadOnce(ctx context.Context) (body []byte, err error) {
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

	body, err = io.ReadAll(io.LimitReader(resp.Body, maxGFWListSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if len(body) > maxGFWListSize {
		return nil, errors.Error("response is too large")
	}

	return body, nil
}

// errGFWListUpdateInProgress is returned by update when another update is
// already running on this manager.  It is a sentinel; callers may compare
// with errors.Is to skip noisy logging on the background tick.
var errGFWListUpdateInProgress = errors.Error("gfwlist: update already in progress")

// update downloads and parses the GFW list from the configured URL.  At most
// one update runs at a time; concurrent callers receive
// errGFWListUpdateInProgress.
func (m *gfwlistManager) update(ctx context.Context) (domains map[string]struct{}, err error) {
	if !m.updateMu.TryLock() {
		return nil, errGFWListUpdateInProgress
	}
	defer m.updateMu.Unlock()

	if m.conf.URL == "" {
		return m.domainSnapshot(), nil
	}

	m.logger.DebugContext(ctx, "downloading gfwlist", "url", m.conf.URL)

	body, err := downloadWithRetry(
		ctx,
		m.logger,
		gfwListBaseBackoff,
		gfwListDownloadRetries,
		m.downloadOnce,
	)
	if err != nil {
		return nil, fmt.Errorf("downloading gfwlist: %w", err)
	}

	domains, err = parseGFWList(body)
	if err != nil {
		return nil, fmt.Errorf("parsing gfwlist: %w", err)
	}
	if len(domains) == 0 {
		return nil, errors.Error("downloaded gfwlist contains no domains")
	}

	m.setDomains(domains)

	// Save to cache.  A stopped manager skipping the write is expected
	// during Reconfigure, so it is logged at Debug rather than Warn.
	if cacheErr := m.saveToCache(ctx, body); cacheErr != nil {
		if errors.Is(cacheErr, errGFWListManagerStopped) {
			m.logger.DebugContext(ctx, "skipping gfwlist cache save; manager stopped")
		} else {
			m.logger.WarnContext(ctx, "saving gfwlist cache", slogutil.KeyError, cacheErr)
		}
	}

	return domains, nil
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

	return m.totalDomainCount
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

// loadFromCache loads the GFW list from the local cache file.  It enforces the
// same size limit as network downloads, so an unexpectedly large or corrupted
// cache file is rejected before being read into memory in full.
func (m *gfwlistManager) loadFromCache(ctx context.Context) (err error) {
	path := m.cachePath()

	// #nosec G304 -- cachePath is always dataDir joined with a constant file
	// name.
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("statting cache: %w", err)
	}
	if info.Size() > maxGFWListSize {
		return fmt.Errorf("cache file is too large: %d bytes", info.Size())
	}

	// #nosec G304 -- cachePath is always dataDir joined with a constant file
	// name.
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading cache: %w", err)
	}

	domains, err := parseGFWList(data)
	if err != nil {
		return fmt.Errorf("parsing cached gfwlist: %w", err)
	}
	if len(domains) == 0 {
		return errors.Error("cached gfwlist contains no domains")
	}

	m.setDomains(domains)

	m.logger.InfoContext(ctx, "loaded gfwlist from cache", "domains", len(domains))

	return nil
}

// errGFWListManagerStopped is returned by saveToCache when the manager has
// been stopped.  After a Reconfigure the cache file belongs to the new
// manager, so a stale manager finishing its in-flight download must not
// overwrite fresh data with domains from an old URL.
var errGFWListManagerStopped = errors.Error("gfwlist: manager stopped")

// saveToCache saves the raw GFW list data to the local cache file.  It
// refuses to write if the manager has been stopped; see
// errGFWListManagerStopped.
func (m *gfwlistManager) saveToCache(ctx context.Context, data []byte) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}

	select {
	case <-m.stopCh:
		return errGFWListManagerStopped
	default:
	}

	err = os.MkdirAll(m.dataDir, 0o700)
	if err != nil {
		return fmt.Errorf("creating cache dir: %w", err)
	}

	file, err := aghrenameio.NewPendingFile(m.cachePath(), 0o600)
	if err != nil {
		return fmt.Errorf("creating cache file: %w", err)
	}
	defer func() { err = aghrenameio.WithDeferredCleanup(err, file) }()

	_, err = file.Write(data)
	if err != nil {
		return fmt.Errorf("writing cache: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}

	return nil
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

	// Merge the GFW list domain upstreams into the existing config.  The
	// capacity hint reduces map growth reallocations for large GFW lists;
	// it is only an estimate, since some domains may be skipped as invalid
	// and existing maps may already hold unrelated entries.
	capacityHint := m.domainCount()
	if uc.DomainReservedUpstreams == nil {
		uc.DomainReservedUpstreams = make(map[string][]upstream.Upstream, capacityHint)
	}
	if uc.SpecifiedDomainUpstreams == nil {
		uc.SpecifiedDomainUpstreams = make(map[string][]upstream.Upstream, capacityHint)
	}

	domainCount, skippedCount := m.appendToUpstreamConfig(uc, gfwUC.Upstreams)

	m.logger.InfoContext(
		ctx,
		"applied gfwlist domains to upstream config",
		"domain_count", domainCount,
		"upstream_count", len(m.conf.UpstreamDNS),
	)

	if skippedCount > 0 {
		m.logger.WarnContext(
			ctx,
			"skipped invalid gfwlist domains",
			"skipped_count", skippedCount,
		)
	}

	return nil
}

// appendToUpstreamConfig appends all valid GFW list and custom domains to uc.
// A domain that fails validation (e.g. an over-long label) is skipped rather
// than aborting the whole operation, so that a single bad domain cannot
// prevent the rest of the list from being applied, and cannot leave uc
// partially populated on error.  Skipped domains are counted, not logged
// individually, so that a GFW list with many invalid entries does not flood
// the log; the caller logs a single summary line using skippedCount.
func (m *gfwlistManager) appendToUpstreamConfig(
	uc *proxy.UpstreamConfig,
	ups []upstream.Upstream,
) (domainCount, skippedCount int) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	appendDomain := func(domain string) {
		key, keyErr := domainReservedUpstreamKey(domain)
		if keyErr != nil {
			skippedCount++

			return
		}

		uc.DomainReservedUpstreams[key] = append(uc.DomainReservedUpstreams[key], ups...)
		uc.SpecifiedDomainUpstreams[key] = append(uc.SpecifiedDomainUpstreams[key], ups...)

		domainCount++
	}

	for d := range m.domains {
		appendDomain(d)
	}
	for d := range m.customDomains {
		if _, exists := m.domains[d]; exists {
			continue
		}

		appendDomain(d)
	}

	return domainCount, skippedCount
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
		s.stopGFWList()

		return nil
	}

	if err = conf.validate(); err != nil {
		return err
	}

	s.logger.InfoContext(ctx, "initializing gfwlist split routing")

	// Stop previous manager if any.
	s.stopGFWList()

	dataDir := s.gfwListDataDir()

	gfwLogger := s.baseLogger.With(slogutil.KeyPrefix, "gfwlist")
	gfwMgr := newGFWListManager(gfwLogger, conf, dataDir, nil)
	// Wire the callback after construction since it closes over gfwMgr.  The
	// callback checks s.gfwlist against this exact instance, so a stale
	// manager's late-arriving update is dropped instead of triggering a
	// reconfigure.
	gfwMgr.onUpdate = makeGFWListUpdateCallback(s, gfwMgr, s.Reconfigure)
	s.gfwlist = gfwMgr

	domains := s.pendingGFWListDomains
	s.pendingGFWListDomains = nil

	// Load and start background updater.
	s.gfwlist.start(ctx, domains)

	// Apply to upstream config.
	if applyErr := s.applyGFWListToUpstreamConfig(ctx); applyErr != nil {
		return applyErr
	}

	return nil
}

// stopGFWList stops and clears the current GFW list manager.
func (s *Server) stopGFWList() {
	if s.gfwlist == nil {
		return
	}

	s.gfwlist.stop()
	s.gfwlist = nil
}

// makeGFWListUpdateCallback returns the callback invoked when the background
// updater successfully downloads a new GFW list.  It is a package-level
// function (rather than a closure inside prepareGFWList) so the
// pending-domains → reconfigure flow can be unit-tested with a stubbed
// reconfigure.  reconfigure is typically s.Reconfigure.
func makeGFWListUpdateCallback(
	s *Server,
	gfwMgr *gfwlistManager,
	reconfigure func(ctx context.Context, conf *ServerConfig) error,
) func(ctx context.Context, domains map[string]struct{}) {
	return func(ctx context.Context, domains map[string]struct{}) {
		if !s.setPendingGFWListDomains(gfwMgr, domains) {
			return
		}

		s.logger.InfoContext(ctx, "reconfiguring after gfwlist update")

		if reconfigureErr := reconfigure(ctx, nil); reconfigureErr != nil {
			s.logger.ErrorContext(
				ctx,
				"reconfiguring after gfwlist update",
				slogutil.KeyError,
				reconfigureErr,
			)
		}
	}
}

// gfwListDataDir returns the cache directory for the GFW list.
func (s *Server) gfwListDataDir() (dataDir string) {
	// Prefer the directory of the upstream DNS config file; fall back to
	// "data" if it is empty or "." (which happens when UpstreamDNSFileName is
	// a bare filename with no path).
	dataDir = filepath.Dir(s.conf.UpstreamDNSFileName)
	if dataDir == "" || dataDir == "." {
		return "data"
	}

	return dataDir
}

// applyGFWListToUpstreamConfig applies GFW list routing to the upstream
// configuration if it exists.
func (s *Server) applyGFWListToUpstreamConfig(ctx context.Context) (err error) {
	if s.conf.UpstreamConfig == nil {
		return nil
	}

	opts := &upstream.Options{
		Logger:     s.baseLogger,
		Bootstrap:  s.bootstrap,
		Timeout:    s.conf.UpstreamTimeout,
		PreferIPv6: s.conf.BootstrapPreferIPv6,
	}

	if applyErr := s.gfwlist.applyToUpstreamConfig(ctx, s.conf.UpstreamConfig, opts); applyErr != nil {
		return fmt.Errorf("applying gfwlist to upstream config: %w", applyErr)
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
