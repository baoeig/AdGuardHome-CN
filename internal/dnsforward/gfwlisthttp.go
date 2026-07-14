package dnsforward

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/AdguardTeam/AdGuardHome/internal/aghhttp"
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
	UpdateInterval *int `json:"update_interval"`
}

// jsonGFWListDomainReq is the JSON request for adding/removing custom domains.
type jsonGFWListDomainReq struct {
	// Domains is the list of domains to add or remove.
	Domains []string `json:"domains"`
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

	s.serverLock.RLock()
	defer s.serverLock.RUnlock()

	conf := s.conf.GFWList
	if conf == nil {
		aghhttp.WriteJSONResponseOK(ctx, s.logger, w, r, &jsonGFWListStatus{})

		return
	}

	domainCount := 0
	if s.gfwlist != nil {
		domainCount = s.gfwlist.domainCount()
	}

	resp := &jsonGFWListStatus{
		Enabled:        conf.Enabled,
		URL:            conf.URL,
		UpstreamDNS:    slices.Clone(conf.UpstreamDNS),
		CustomDomains:  slices.Clone(conf.CustomDomains),
		DomainCount:    domainCount,
		UpdateInterval: int(time.Duration(s.conf.GFWList.UpdateInterval).Seconds()),
	}

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

	s.serverLock.Lock()

	if s.conf.GFWList == nil {
		s.conf.GFWList = &GFWListConfig{}
	}

	conf := s.conf.GFWList

	if req.Enabled != nil {
		conf.Enabled = *req.Enabled
	}

	if req.URL != nil {
		conf.URL = *req.URL
	}

	if req.UpstreamDNS != nil {
		conf.UpstreamDNS = *req.UpstreamDNS
	}

	if req.UpdateInterval != nil {
		conf.UpdateInterval = timeutil.Duration(time.Duration(*req.UpdateInterval) * time.Second)
	}

	s.serverLock.Unlock()

	// Trigger a reconfigure to apply changes.
	if err := s.Reconfigure(ctx, nil); err != nil {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, err)

		return
	}

	aghhttp.WriteJSONResponseOK(ctx, s.logger, w, r, &struct{}{})
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

	if err := gfwMgr.update(ctx); err != nil {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, err)

		return
	}

	// Reconfigure to apply new domains.
	if err := s.Reconfigure(ctx, nil); err != nil {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, err)

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

	if s.conf.GFWList == nil {
		s.conf.GFWList = &GFWListConfig{}
	}

	existing := make(map[string]struct{}, len(s.conf.GFWList.CustomDomains))
	for _, d := range s.conf.GFWList.CustomDomains {
		existing[d] = struct{}{}
	}

	for _, d := range req.Domains {
		d = normalizeGFWDomainRule(d)
		if d == "" {
			continue
		}

		if _, ok := existing[d]; !ok {
			s.conf.GFWList.CustomDomains = append(s.conf.GFWList.CustomDomains, d)
			existing[d] = struct{}{}
		}
	}

	s.serverLock.Unlock()

	// Reconfigure to apply.
	if err := s.Reconfigure(ctx, nil); err != nil {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, err)

		return
	}

	aghhttp.WriteJSONResponseOK(ctx, s.logger, w, r, &struct{}{})
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

	if s.conf.GFWList != nil {
		toRemove := make(map[string]struct{}, len(req.Domains))
		for _, d := range req.Domains {
			toRemove[normalizeGFWDomainRule(d)] = struct{}{}
		}

		s.conf.GFWList.CustomDomains = slices.DeleteFunc(
			s.conf.GFWList.CustomDomains,
			func(d string) bool {
				_, ok := toRemove[normalizeGFWDomainRule(d)]

				return ok
			},
		)
	}

	s.serverLock.Unlock()

	// Reconfigure to apply.
	if err := s.Reconfigure(ctx, nil); err != nil {
		aghhttp.WriteJSONResponseError(ctx, s.logger, w, r, err)

		return
	}

	aghhttp.WriteJSONResponseOK(ctx, s.logger, w, r, &struct{}{})
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
