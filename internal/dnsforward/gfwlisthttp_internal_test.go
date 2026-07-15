package dnsforward

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleGFWListStatusPagination(t *testing.T) {
	conf := &GFWListConfig{
		Enabled:       true,
		CustomDomains: []string{"a.example", "b.example", "c.example"},
	}
	mgr := newGFWListManager(testLogger, conf, t.TempDir(), nil)
	mgr.setDomains(map[string]struct{}{
		"gfw.example": {},
	})

	s := &Server{
		logger: testLogger,
		conf: ServerConfig{
			Config: Config{
				GFWList: conf,
			},
		},
		gfwlist: mgr,
	}

	req := httptest.NewRequest(
		http.MethodGet,
		"/control/gfwlist/status?custom_domain_page=1&custom_domain_page_size=2",
		nil,
	)
	w := httptest.NewRecorder()

	s.handleGFWListStatus(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var got jsonGFWListStatus
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, []string{"c.example"}, got.CustomDomains)
	assert.Equal(t, 3, got.CustomDomainsTotal)
	assert.Equal(t, 1, got.CustomDomainPage)
	assert.Equal(t, 4, got.DomainCount)
}

func TestGFWListConfigCustomDomainsCache(t *testing.T) {
	conf := &GFWListConfig{
		CustomDomains: []string{"Example.org", "example.org", "foo.bar"},
	}

	added := conf.addCustomDomains([]string{"*.foo.bar", "new.example.net", "new.example.net"})
	assert.Equal(t, 1, added)
	assert.Equal(t, []string{"example.org", "foo.bar", "new.example.net"}, conf.CustomDomains)

	removed := conf.removeCustomDomains([]string{"foo.bar"})
	assert.Equal(t, 1, removed)
	assert.Equal(t, []string{"example.org", "new.example.net"}, conf.CustomDomains)
}

func TestGFWListManagerDomainCountUsesSnapshot(t *testing.T) {
	conf := &GFWListConfig{
		CustomDomains: []string{"example.org"},
	}
	m := newGFWListManager(testLogger, conf, t.TempDir(), nil)
	m.setDomains(map[string]struct{}{
		"example.com": {},
	})

	assert.Equal(t, 2, m.domainCount())

	m.setDomains(map[string]struct{}{
		"example.com": {},
		"example.org": {},
	})

	assert.Equal(t, 2, m.domainCount())

	snapshot := m.domainSnapshot()
	assert.Equal(t, map[string]struct{}{
		"example.com": {},
		"example.org": {},
	}, snapshot)
}

func TestHandleGFWListAddDomainsNoChange(t *testing.T) {
	conf := &GFWListConfig{
		CustomDomains: []string{"example.org"},
	}
	s := &Server{
		logger: testLogger,
		conf: ServerConfig{
			Config: Config{
				GFWList: conf,
			},
		},
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/control/gfwlist/domains/add",
		strings.NewReader(`{"domains":["example.org"]}`),
	)
	w := httptest.NewRecorder()

	s.handleGFWListAddDomains(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var got jsonGFWListDomainResp
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, 0, got.AddedCount)
	assert.Equal(t, 1, got.CustomDomainsTotal)
	assert.Equal(t, []string{"example.org"}, conf.CustomDomains)
}

func TestHandleGFWListRemoveDomainsNoChange(t *testing.T) {
	conf := &GFWListConfig{
		CustomDomains: []string{"example.org"},
	}
	s := &Server{
		logger: testLogger,
		conf: ServerConfig{
			Config: Config{
				GFWList: conf,
			},
		},
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/control/gfwlist/domains/remove",
		strings.NewReader(`{"domains":["missing.example"]}`),
	)
	w := httptest.NewRecorder()

	s.handleGFWListRemoveDomains(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var got jsonGFWListDomainResp
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, 0, got.RemovedCount)
	assert.Equal(t, 1, got.CustomDomainsTotal)
	assert.Equal(t, []string{"example.org"}, conf.CustomDomains)
}
