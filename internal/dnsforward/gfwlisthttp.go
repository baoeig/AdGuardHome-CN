package dnsforward

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/AdguardTeam/AdGuardHome/internal/aghhttp"
	"github.com/AdguardTeam/golibs/errors"
	"github.com/AdguardTeam/golibs/timeutil"
)

// jsonGFWListStatus is the JSON representation of the GFW list status.
type jsonGFWListStatus struct {
	// Enabled defines if GFW list split routing is enabled.
	Enabled bool `json:"enabled"`

	// URL is the URL of the GFW list.
	URL string `json:"url"`

	// UpstreamDNS is the list of upstream DNS servers for GFW list domains.
	UpstreamDNS []string `json:"upstream_dns"`

	// CustomDomains is the list of user-defined split routing domains.
	CustomDomains []string `json:"custom_domains"`

	// CustomDomainsTotal is the total number of custom domains.
	CustomDomainsTotal int `json:"custom_domains_total"`

	// CustomDomainPage is the zero-based page index returned by the server.
	CustomDomainPage int `json:"custom_domain_page"`

	// DomainCount is the total number of domains (GFW list + custom).
	DomainCount int `json:"domain_count"`

	// UpdateInterval is the update interval in seconds.
	UpdateInterval int `json:"update_interval"`
}

// jsonGFWListConfigReq is the JSON request for configuring GFW list.
type jsonGFWListConfigReq struct {
	// Enabled defines if GFW list split routing is enabled.
	Enabled *bool `json:"enabled"`

	// URL is the URL of the GFW list.
	URL *string `json:"url"`

	// UpstreamDNS is the list of upstream DNS servers for GFW list domains.
	UpstreamDNS *[]string `json:"upstream_dns"`

	// UpdateInterval is the update interval in seconds.
	UpdateInterval *int64 `json:"update_interval"`
}

// jsonGFWListDomainReq is the JSON request for adding/removing custom domains.
type jsonGFWListDomainReq struct {
	// Domains is the list of domains to add or remove.
	Domains []string `json:"domains"`
}

// jsonGFWListDomainResp is the JSON response for modifying custom domains.
type jsonGFWListDomainResp struct {
	// AddedCount is the number of newly added domains.
	AddedCount int `json:"added_count,omitempty"`

	// RemovedCount is the number of removed domains.
	RemovedCount int `json:"removed_count,omitempty"`

	// CustomDomainsTotal is the total number of custom domains after update.
	CustomDomainsTotal int `json:"custom_domains_total"`
}

// jsonGFWListCheckResp is the JSON response for checking a domain against the
// GFW list split-routing rules.
type jsonGFWListCheckResp struct {
	// Domain is the normalized domain that was checked.
	Domain string `json:"domain"`

	// Matched is true if the domain is in the GFW list or custom list.
	Matched bool `json:"matched"`

	// Source is "gfwlist", "custom", or empty.
	Source string `json:"source"`
}

// handleGFWListStatus handles GET /control/gfwlist/status requests.
func (s *Server) handleGFWListStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	page, pageSize, err := parseGFWListDomainPage(r)
	if err != nil {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, err)

		return
	}

	s.serverLock.RLock()
	conf := s.conf.GFWList
	if conf == nil {
		s.serverLock.RUnlock()
		aghhttp.WriteJSONResponseOK(ctx, s.logger, w, r, &jsonGFWListStatus{})

		return
	}

	domainCount := 0
	if s.gfwlist != nil {
		domainCount = s.gfwlist.domainCount()
	}

	customDomains := conf.CustomDomains
	customDomainPage := 0
	if pageSize > 0 {
		customDomains, customDomainPage = paginateGFWListCustomDomains(customDomains, page, pageSize)
	} else {
		customDomains = slices.Clone(customDomains)
	}

	resp := &jsonGFWListStatus{
		Enabled:            conf.Enabled,
		URL:                conf.URL,
		UpstreamDNS:        slices.Clone(conf.UpstreamDNS),
		CustomDomains:      customDomains,
		CustomDomainsTotal: len(conf.CustomDomains),
		CustomDomainPage:   customDomainPage,
		DomainCount:        domainCount,
		UpdateInterval:     int(time.Duration(conf.UpdateInterval).Seconds()),
	}
	s.serverLock.RUnlock()

	aghhttp.WriteJSONResponseOK(ctx, s.logger, w, r, resp)
}

// handleGFWListSetConfig handles POST /control/gfwlist/config requests.
func (s *Server) handleGFWListSetConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	req := &jsonGFWListConfigReq{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, err)

		return
	}
	// Read-modify-validate-write is intentionally done under a single
	// serverLock critical section.  Do NOT split this into "read config,
	// unlock, validate, lock, write": validate() runs in well under a
	// millisecond (the heaviest check, proxy.ParseUpstreamsConfig, is
	// ~100µs), and this handler is a low-frequency management endpoint, not
	// a hot path.  Releasing the lock around validate() would open a window
	// where a concurrent config change (e.g. another admin adding a custom
	// domain) could be silently overwritten by this request's candidate,
	// which is a worse outcome than a sub-millisecond lock hold.
	s.serverLock.Lock()

	existing := s.conf.GFWList
	if existing == nil {
		existing = &GFWListConfig{}
	}

	// Validate a candidate configuration before committing it, so that an
	// invalid request (e.g. a bad URL scheme or out-of-range update
	// interval) neither mutates the stored configuration nor triggers a
	// reconfigure.
	candidate, err := applyGFWListConfigReq(existing, req)
	if err != nil {
		s.serverLock.Unlock()
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, err)

		return
	}
	if validateErr := candidate.validate(); validateErr != nil {
		s.serverLock.Unlock()
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, validateErr)

		return
	}

	s.conf.GFWList = candidate

	s.serverLock.Unlock()

	s.conf.ConfModifier.Apply(ctx)

	// Trigger a reconfigure to apply changes.
	if reconfigureErr := s.Reconfigure(ctx, nil); reconfigureErr != nil {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, reconfigureErr)

		return
	}

	aghhttp.WriteJSONResponseOK(ctx, s.logger, w, r, &struct{}{})
}

// applyGFWListConfigReq returns a new GFWListConfig with req's fields applied
// on top of existing.  existing is not mutated.
func applyGFWListConfigReq(
	existing *GFWListConfig,
	req *jsonGFWListConfigReq,
) (candidate *GFWListConfig, err error) {
	candidate = cloneGFWListConfig(existing)

	if req.Enabled != nil {
		candidate.Enabled = *req.Enabled
	}

	if req.URL != nil {
		candidate.URL = *req.URL
	}

	if req.UpstreamDNS != nil {
		candidate.UpstreamDNS = *req.UpstreamDNS
	}

	if req.UpdateInterval != nil {
		if err = validateGFWListUpdateIntervalSeconds(*req.UpdateInterval); err != nil {
			return nil, err
		}

		candidate.UpdateInterval = timeutil.Duration(time.Duration(*req.UpdateInterval) * time.Second)
	}

	return candidate, nil
}

// validateGFWListUpdateIntervalSeconds validates a raw JSON update interval
// value before it is converted to [time.Duration].  This prevents integer
// overflow during the seconds-to-duration conversion.
func validateGFWListUpdateIntervalSeconds(seconds int64) (err error) {
	if seconds == 0 {
		return nil
	}

	minSeconds := int64(minGFWListUpdateInterval / time.Second)
	maxSeconds := int64(maxGFWListUpdateInterval / time.Second)
	if seconds < minSeconds || seconds > maxSeconds {
		return fmt.Errorf(
			"gfwlist: update_interval must be 0 or between %d and %d seconds, got %d",
			minSeconds,
			maxSeconds,
			seconds,
		)
	}

	return nil
}

// handleGFWListUpdate handles POST /control/gfwlist/update requests.
func (s *Server) handleGFWListUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	s.serverLock.RLock()
	gfwMgr := s.gfwlist
	s.serverLock.RUnlock()

	if gfwMgr == nil {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, fmt.Errorf("gfwlist is not enabled"))

		return
	}

	domains, err := gfwMgr.update(ctx)
	if errors.Is(err, errGFWListUpdateInProgress) {
		// Another update (typically the background tick) is already
		// downloading.  Return 409 so the UI can show an informational
		// notice instead of a misleading failure.
		aghhttp.WriteJSONResponse(ctx, s.logger, w, r, http.StatusConflict, &aghhttp.HTTPAPIErrorResp{
			Msg: err.Error(),
		})

		return
	}
	if err != nil {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, err)

		return
	}
	if !s.setPendingGFWListDomains(gfwMgr, domains) {
		aghhttp.WriteJSONResponseError(
			ctx,
			s.logger,
			w,
			r,
			fmt.Errorf("gfwlist configuration changed while updating"),
		)

		return
	}

	// Reconfigure to apply new domains.
	if reconfigureErr := s.Reconfigure(ctx, nil); reconfigureErr != nil {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, reconfigureErr)

		return
	}

	aghhttp.WriteJSONResponseOK(ctx, s.logger, w, r, &struct {
		DomainCount int `json:"domain_count"`
	}{
		DomainCount: gfwMgr.domainCount(),
	})
}

// handleGFWListAddDomains handles POST /control/gfwlist/domains/add requests.
func (s *Server) handleGFWListAddDomains(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	req := &jsonGFWListDomainReq{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, err)

		return
	}

	s.serverLock.Lock()
	addedCount := s.addGFWListCustomDomainsLocked(req.Domains)
	customDomainsTotal := 0
	if s.conf.GFWList != nil {
		customDomainsTotal = len(s.conf.GFWList.CustomDomains)
	}
	s.serverLock.Unlock()

	if addedCount == 0 {
		aghhttp.WriteJSONResponseOK(ctx, s.logger, w, r, &jsonGFWListDomainResp{
			CustomDomainsTotal: customDomainsTotal,
		})

		return
	}

	s.conf.ConfModifier.Apply(ctx)

	// Reconfigure to apply.
	if err := s.Reconfigure(ctx, nil); err != nil {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, err)

		return
	}

	aghhttp.WriteJSONResponseOK(ctx, s.logger, w, r, &jsonGFWListDomainResp{
		AddedCount:         addedCount,
		CustomDomainsTotal: customDomainsTotal,
	})
}

// handleGFWListRemoveDomains handles POST /control/gfwlist/domains/remove
// requests.
func (s *Server) handleGFWListRemoveDomains(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	req := &jsonGFWListDomainReq{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, err)

		return
	}

	s.serverLock.Lock()

	removedCount := 0
	if s.conf.GFWList != nil {
		removedCount = s.removeGFWListCustomDomainsLocked(req.Domains)
	}

	customDomainsTotal := 0
	if s.conf.GFWList != nil {
		customDomainsTotal = len(s.conf.GFWList.CustomDomains)
	}

	s.serverLock.Unlock()

	if removedCount == 0 {
		aghhttp.WriteJSONResponseOK(ctx, s.logger, w, r, &jsonGFWListDomainResp{
			CustomDomainsTotal: customDomainsTotal,
		})

		return
	}

	s.conf.ConfModifier.Apply(ctx)

	// Reconfigure to apply.
	if err := s.Reconfigure(ctx, nil); err != nil {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, err)

		return
	}

	aghhttp.WriteJSONResponseOK(ctx, s.logger, w, r, &jsonGFWListDomainResp{
		RemovedCount:       removedCount,
		CustomDomainsTotal: customDomainsTotal,
	})
}

// addGFWListCustomDomainsLocked adds normalized domains to the GFW list
// configuration.  s.serverLock must be held.
func (s *Server) addGFWListCustomDomainsLocked(domains []string) (added int) {
	if s.conf.GFWList == nil {
		conf := &GFWListConfig{}
		added = conf.addCustomDomains(domains)
		if added == 0 {
			return 0
		}

		s.conf.GFWList = conf

		return added
	}

	added = s.conf.GFWList.addCustomDomains(domains)

	return added
}

// removeGFWListCustomDomainsLocked removes normalized domains from the GFW list
// configuration.  s.serverLock must be held.
func (s *Server) removeGFWListCustomDomainsLocked(domains []string) (removed int) {
	if s.conf.GFWList == nil {
		return 0
	}

	return s.conf.GFWList.removeCustomDomains(domains)
}

// parseGFWListDomainPage parses custom domain pagination query parameters.
func parseGFWListDomainPage(r *http.Request) (page, pageSize int, err error) {
	page, err = parseNonNegativeIntQuery(r, "custom_domain_page")
	if err != nil {
		return 0, 0, err
	}

	pageSize, err = parseNonNegativeIntQuery(r, "custom_domain_page_size")
	if err != nil {
		return 0, 0, err
	}

	return page, pageSize, nil
}

// parseNonNegativeIntQuery parses a non-negative integer query parameter.
func parseNonNegativeIntQuery(r *http.Request, key string) (int, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return 0, nil
	}

	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("invalid %s: must be non-negative", key)
	}

	return n, nil
}

// paginateGFWListCustomDomains returns the requested page of domains and the
// zero-based page index actually used after clamping.
func paginateGFWListCustomDomains(domains []string, page, pageSize int) (pageDomains []string, actualPage int) {
	total := len(domains)
	if pageSize <= 0 || total == 0 {
		return slices.Clone(domains), 0
	}

	maxPage := (total - 1) / pageSize
	if page > maxPage {
		page = maxPage
	}

	start := page * pageSize
	end := start + pageSize
	if end > total {
		end = total
	}

	return slices.Clone(domains[start:end]), page
}

// handleGFWListCheck handles GET /control/gfwlist/check requests.
func (s *Server) handleGFWListCheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	domain := normalizeGFWDomainRule(r.URL.Query().Get("domain"))
	if domain == "" {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, fmt.Errorf("invalid domain"))

		return
	}

	s.serverLock.RLock()
	gfwMgr := s.gfwlist
	s.serverLock.RUnlock()

	if gfwMgr == nil {
		aghhttp.WriteJSONResponseOK(ctx, s.logger, w, r, &jsonGFWListCheckResp{
			Domain: domain,
		})

		return
	}

	matched, source := gfwMgr.checkDomain(domain)
	aghhttp.WriteJSONResponseOK(ctx, s.logger, w, r, &jsonGFWListCheckResp{
		Domain:  domain,
		Matched: matched,
		Source:  source,
	})
}

// registerGFWListHandlers registers the GFW list HTTP API handlers.
func (s *Server) registerGFWListHandlers() {
	if s.conf.HTTPReg == nil {
		return
	}

	s.conf.HTTPReg.Register(
		http.MethodGet, "/control/gfwlist/status", s.handleGFWListStatus,
	)
	s.conf.HTTPReg.Register(
		http.MethodPost, "/control/gfwlist/config", s.handleGFWListSetConfig,
	)
	s.conf.HTTPReg.Register(
		http.MethodPost, "/control/gfwlist/update", s.handleGFWListUpdate,
	)
	s.conf.HTTPReg.Register(
		http.MethodPost, "/control/gfwlist/domains/add", s.handleGFWListAddDomains,
	)
	s.conf.HTTPReg.Register(
		http.MethodPost, "/control/gfwlist/domains/remove", s.handleGFWListRemoveDomains,
	)
	s.conf.HTTPReg.Register(
		http.MethodGet, "/control/gfwlist/check", s.handleGFWListCheck,
	)
}
