package dnsforward

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/AdguardTeam/golibs/timeutil"
	"github.com/stretchr/testify/assert"
)

// TestGFWListStartupDelay_MemorySnapshotDefersToInterval verifies that when a
// manager starts with an in-memory domain snapshot (e.g. right after a manual
// "update now" triggered a Reconfigure), the background updater's first
// download is deferred to the next regular interval instead of running
// immediately.  This avoids downloading the same list twice in quick
// succession: once synchronously in the HTTP handler, and once again a few
// seconds later from the background updater's startup jitter.
func TestGFWListStartupDelay_MemorySnapshotDefersToInterval(t *testing.T) {
	const interval = 42 * time.Minute

	assert.Equal(t, interval, gfwListStartupDelay(true, interval))
}

// TestGFWListStartupDelay_NoSnapshotUsesJitter verifies that when a manager
// starts without an in-memory snapshot (fresh install or loaded from cache),
// the first download uses the bounded random jitter, not the full interval,
// so a fresh install without cache does not stay empty for a long time.
func TestGFWListStartupDelay_NoSnapshotUsesJitter(t *testing.T) {
	const interval = time.Hour

	for i := 0; i < 20; i++ {
		got := gfwListStartupDelay(false, interval)
		assert.GreaterOrEqual(t, got, time.Duration(0))
		assert.Less(t, got, gfwListMaxInitialDelay)
	}
}

// TestGFWListManager_startWithMemorySnapshotDoesNotDownloadBeforeInterval is
// a deterministic integration check: with a short interval and a non-nil
// memory snapshot, no download happens before the interval elapses.
func TestGFWListManager_startWithMemorySnapshotDoesNotDownloadBeforeInterval(t *testing.T) {
	var downloadCount atomic.Int32
	srv := newCountingGFWListServer(t, &downloadCount)

	const interval = 150 * time.Millisecond
	conf := &GFWListConfig{
		Enabled:        true,
		URL:            srv.URL,
		UpdateInterval: timeutil.Duration(interval),
	}
	m := newGFWListManager(testLogger, conf, t.TempDir(), nil)
	t.Cleanup(m.stop)

	m.start(t.Context(), map[string]struct{}{"example.org": {}})

	// Well before the interval elapses, no download should have happened.
	time.Sleep(interval / 3)
	assert.EqualValues(t, 0, downloadCount.Load())

	// After the interval elapses, exactly one download should have happened.
	assert.Eventually(t, func() bool {
		return downloadCount.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
}
