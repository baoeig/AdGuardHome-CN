package dnsforward

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGFWListManagerUpdate_ConcurrentCallsSerialized verifies that
// concurrent calls to update on the same manager do not both download: one
// wins, the others observe errGFWListUpdateInProgress.
func TestGFWListManagerUpdate_ConcurrentCallsSerialized(t *testing.T) {
	// Gate the download so the first caller holds the lock long enough for
	// the second caller to observe it.
	release := make(chan struct{})
	var downloadCount atomic.Int32

	body := base64.StdEncoding.EncodeToString([]byte("[AutoProxy 0.2.9]\n||example.org\n"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloadCount.Add(1)
		<-release
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	m := newGFWListManager(testLogger, &GFWListConfig{URL: srv.URL}, t.TempDir(), nil)
	t.Cleanup(m.stop)

	var wg sync.WaitGroup
	results := make(chan error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := m.update(t.Context())
		results <- err
	}()
	go func() {
		defer wg.Done()
		// Wait until the first download has actually started before racing it.
		require.Eventually(t, func() bool {
			return downloadCount.Load() == 1
		}, time.Second, time.Millisecond)

		_, err := m.update(t.Context())
		results <- err
	}()

	// Let the first download finish.
	close(release)
	wg.Wait()
	close(results)

	var successCount, inProgressCount int
	for err := range results {
		switch {
		case err == nil:
			successCount++
		case err == errGFWListUpdateInProgress:
			inProgressCount++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}

	assert.Equal(t, 1, successCount, "exactly one update should succeed")
	assert.Equal(t, 1, inProgressCount, "the concurrent update should observe the in-progress sentinel")
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
