package dnsforward

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGFWListManagerSaveToCache_StoppedManager verifies that a stopped
// manager refuses to write the shared cache file.  This is the guard that
// prevents a stale manager — replaced by a Reconfigure while its download
// was in flight — from overwriting the new manager's fresh cache with
// domains from an old URL.
func TestGFWListManagerSaveToCache_StoppedManager(t *testing.T) {
	dir := t.TempDir()

	m := newGFWListManager(testLogger, &GFWListConfig{}, dir, nil)
	m.stop()

	err := m.saveToCache(t.Context(), []byte("stale data"))
	require.ErrorIs(t, err, errGFWListManagerStopped)

	_, statErr := os.Stat(filepath.Join(dir, gfwlistCacheFile))
	require.ErrorIs(t, statErr, os.ErrNotExist, "stopped manager must not create the cache file")
}

// TestGFWListManagerUpdate_DoesNotOverwriteCacheAfterStop exercises the
// full race: a download is in flight when the manager is stopped (as
// happens during Reconfigure).  The update completes, but the stale data
// must not land in the shared cache file.
func TestGFWListManagerUpdate_DoesNotOverwriteCacheAfterStop(t *testing.T) {
	dir := t.TempDir()

	// Pre-seed the cache with content that simulates the new manager's
	// fresh data.  The stale download must not overwrite it.
	cachePath := filepath.Join(dir, gfwlistCacheFile)
	freshCache := []byte("fresh cache from new manager")
	require.NoError(t, os.WriteFile(cachePath, freshCache, 0o600))

	downloadStarted := make(chan struct{})
	proceed := make(chan struct{})
	var once sync.Once

	staleBody := base64.StdEncoding.EncodeToString([]byte("[AutoProxy 0.2.9]\n||stale.example.org\n"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(downloadStarted) })
		<-proceed
		_, _ = w.Write([]byte(staleBody))
	}))
	t.Cleanup(srv.Close)

	m := newGFWListManager(testLogger, &GFWListConfig{URL: srv.URL}, dir, nil)

	updateDone := make(chan error, 1)
	go func() {
		_, err := m.update(t.Context())
		updateDone <- err
	}()

	// Wait for the download to be in flight, then stop the manager — this
	// mirrors stopGFWList during Reconfigure.
	<-downloadStarted
	m.stop()
	close(proceed)

	err := <-updateDone
	require.NoError(t, err, "update itself still completes; only the cache write is skipped")

	got, readErr := os.ReadFile(cachePath)
	require.NoError(t, readErr)
	assert.Equal(t, freshCache, got, "stale manager must not overwrite the fresh cache")
}
