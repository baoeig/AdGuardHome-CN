package dnsforward

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AdguardTeam/golibs/timeutil"
	"github.com/stretchr/testify/assert"
)

// newTestGFWListServer returns an httptest server serving a small base64
// AutoProxy list containing example.org.
func newTestGFWListServer(t *testing.T) *httptest.Server {
	t.Helper()

	body := base64.StdEncoding.EncodeToString([]byte("[AutoProxy 0.2.9]\n||example.org\n"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv
}

// newCountingGFWListServer is like newTestGFWListServer, but increments
// *count on every request, allowing tests to assert on the number of
// downloads performed.
func newCountingGFWListServer(t *testing.T, count *atomic.Int32) *httptest.Server {
	t.Helper()

	body := base64.StdEncoding.EncodeToString([]byte("[AutoProxy 0.2.9]\n||example.org\n"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestGFWListManager_backgroundUpdaterInitialDownload(t *testing.T) {
	srv := newTestGFWListServer(t)

	conf := &GFWListConfig{
		Enabled: true,
		URL:     srv.URL,
		// A long interval ensures that any populated domains come from the
		// immediate initial download, not from a ticker tick.
		UpdateInterval: timeutil.Duration(time.Hour),
	}
	m := newGFWListManager(testLogger, conf, t.TempDir(), nil)
	t.Cleanup(m.stop)

	// initialDelay of 0 means download immediately.
	go m.backgroundUpdater(t.Context(), 0, time.Hour)

	assert.Eventually(t, func() bool {
		return m.domainCount() > 0
	}, 2*time.Second, 10*time.Millisecond)
}
