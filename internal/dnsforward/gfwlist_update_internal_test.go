package dnsforward

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGFWListManagerUpdate_RejectsEmptyDownloadedList(t *testing.T) {
	dir := t.TempDir()
	emptyList := base64.StdEncoding.EncodeToString([]byte("[AutoProxy 0.2.9]\n! no domains\n"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(emptyList))
	}))
	t.Cleanup(srv.Close)

	conf := &GFWListConfig{
		URL: srv.URL,
	}
	m := newGFWListManager(testLogger, conf, dir, nil)
	m.setDomains(map[string]struct{}{
		"old.example.com": {},
	})

	cachePath := filepath.Join(dir, gfwlistCacheFile)
	oldCache := []byte(base64.StdEncoding.EncodeToString([]byte("[AutoProxy 0.2.9]\n||old.example.com\n")))
	err := os.WriteFile(cachePath, oldCache, 0o600)
	require.NoError(t, err)

	_, err = m.update(t.Context())
	require.Error(t, err)
	assert.ErrorContains(t, err, "contains no domains")
	assert.Equal(t, map[string]struct{}{
		"old.example.com": {},
	}, m.domainSnapshot())

	gotCache, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	assert.Equal(t, oldCache, gotCache)
}

func TestGFWListManagerBackgroundUpdater_StopCancelsInFlightDownload(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	var once sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			close(requestStarted)
		})

		<-r.Context().Done()
		close(requestCanceled)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	conf := &GFWListConfig{
		URL: srv.URL,
	}
	m := newGFWListManager(testLogger, conf, dir, nil)
	t.Cleanup(m.stop)

	go m.backgroundUpdater(context.WithoutCancel(t.Context()), 0, time.Hour)

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("gfwlist download did not start")
	}

	m.stop()

	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("gfwlist download was not canceled")
	}

	_, err := os.Stat(filepath.Join(dir, gfwlistCacheFile))
	require.ErrorIs(t, err, os.ErrNotExist)
}
