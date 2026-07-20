package dnsforward

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGFWListManagerUpdate_ConcurrentCallsSerialized verifies that
// concurrent calls to update on the same manager do not both download: one
// wins, the others observe errGFWListUpdateInProgress.
//
// Synchronization is orchestrated from the main test goroutine: the HTTP
// handler closes downloadStarted on entry and blocks on proceed, so the
// second update is guaranteed to run while the first still holds
// updateMu.  No assertions happen inside goroutines.
func TestGFWListManagerUpdate_ConcurrentCallsSerialized(t *testing.T) {
	downloadStarted := make(chan struct{})
	proceed := make(chan struct{})
	var once sync.Once
	var downloadCount atomic.Int32

	body := base64.StdEncoding.EncodeToString([]byte("[AutoProxy 0.2.9]\n||example.org\n"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloadCount.Add(1)
		once.Do(func() { close(downloadStarted) })
		<-proceed
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	m := newGFWListManager(testLogger, &GFWListConfig{URL: srv.URL}, t.TempDir(), nil)
	t.Cleanup(m.stop)

	firstDone := make(chan error, 1)
	go func() {
		_, err := m.update(t.Context())
		firstDone <- err
	}()

	// Wait until the first update is definitely holding updateMu inside its
	// download, then race it from the main goroutine.
	<-downloadStarted

	_, secondErr := m.update(t.Context())

	// Let the first download finish and collect its result.
	close(proceed)
	firstErr := <-firstDone

	require.NoError(t, firstErr, "the first update should succeed")
	require.ErrorIs(t, secondErr, errGFWListUpdateInProgress)
	assert.EqualValues(t, 1, downloadCount.Load(), "only one HTTP download should have happened")
}

// TestGFWListManagerUpdate_SerialCallsSucceed verifies that the in-progress
// sentinel does not block later updates once the previous one has finished.
func TestGFWListManagerUpdate_SerialCallsSucceed(t *testing.T) {
	body := base64.StdEncoding.EncodeToString([]byte("[AutoProxy 0.2.9]\n||example.org\n"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	m := newGFWListManager(testLogger, &GFWListConfig{URL: srv.URL}, t.TempDir(), nil)
	t.Cleanup(m.stop)

	for i := 0; i < 3; i++ {
		_, err := m.update(context.Background())
		require.NoError(t, err, "iteration %d", i)
	}
}
