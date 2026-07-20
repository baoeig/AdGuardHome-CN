package dnsforward

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/AdguardTeam/AdGuardHome/internal/aghhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandleGFWListUpdate_ReturnsConflictWhenUpdateInProgress verifies that
// a manual update racing an in-flight update receives 409 Conflict with a
// recognizable message, so the UI can show an informational notice instead
// of a misleading failure.
func TestHandleGFWListUpdate_ReturnsConflictWhenUpdateInProgress(t *testing.T) {
	downloadStarted := make(chan struct{})
	proceed := make(chan struct{})
	var once sync.Once

	body := base64.StdEncoding.EncodeToString([]byte("[AutoProxy 0.2.9]\n||example.org\n"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(downloadStarted) })
		<-proceed
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	conf := &GFWListConfig{
		Enabled: true,
		URL:     srv.URL,
	}
	mgr := newGFWListManager(testLogger, conf, t.TempDir(), nil)
	t.Cleanup(mgr.stop)

	s := &Server{
		logger:  testLogger,
		gfwlist: mgr,
	}

	// Occupy the manager with an in-flight download.
	firstDone := make(chan error, 1)
	go func() {
		_, err := mgr.update(t.Context())
		firstDone <- err
	}()
	<-downloadStarted

	req := httptest.NewRequest(http.MethodPost, "/control/gfwlist/update", nil)
	w := httptest.NewRecorder()
	s.handleGFWListUpdate(w, req)

	close(proceed)
	require.NoError(t, <-firstDone)

	require.Equal(t, http.StatusConflict, w.Code)

	var got aghhttp.HTTPAPIErrorResp
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Contains(t, got.Msg, "update already in progress")
}
