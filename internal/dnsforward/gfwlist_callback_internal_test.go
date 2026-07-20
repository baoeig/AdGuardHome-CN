package dnsforward

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/AdguardTeam/golibs/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestServerWithGFWList returns a minimal Server suitable for exercising
// the GFW list update callback.  Only the fields touched by
// setPendingGFWListDomains and the callback are populated.
func newTestServerWithGFWList(mgr *gfwlistManager) *Server {
	return &Server{
		logger:  testLogger,
		gfwlist: mgr,
	}
}

// TestMakeGFWListUpdateCallback_CurrentManagerStoresPendingAndReconfigures
// verifies the happy path: when the firing manager is the one currently
// registered on the server, its domains are stored as pending and
// reconfigure is invoked.
func TestMakeGFWListUpdateCallback_CurrentManagerStoresPendingAndReconfigures(t *testing.T) {
	mgr := newGFWListManager(testLogger, &GFWListConfig{}, t.TempDir(), nil)
	t.Cleanup(mgr.stop)
	s := newTestServerWithGFWList(mgr)

	var reconfigureCalls atomic.Int32
	callback := makeGFWListUpdateCallback(s, mgr, func(_ context.Context, _ *ServerConfig) error {
		reconfigureCalls.Add(1)

		return nil
	})

	domains := map[string]struct{}{"example.org": {}}
	callback(t.Context(), domains)

	assert.EqualValues(t, 1, reconfigureCalls.Load())
	assert.Equal(t, domains, s.pendingGFWListDomains)
}

// TestMakeGFWListUpdateCallback_StaleManagerDropped verifies the critical
// safety property: when the firing manager is NOT the one currently
// registered on the server (e.g. it was replaced by a concurrent
// Reconfigure while its download was in flight), the callback drops the
// update.  Neither pendingGFWListDomains nor reconfigure should be
// touched, so the new manager's state is not clobbered by a stale
// download finishing late.
func TestMakeGFWListUpdateCallback_StaleManagerDropped(t *testing.T) {
	staleMgr := newGFWListManager(testLogger, &GFWListConfig{}, t.TempDir(), nil)
	t.Cleanup(staleMgr.stop)
	currentMgr := newGFWListManager(testLogger, &GFWListConfig{}, t.TempDir(), nil)
	t.Cleanup(currentMgr.stop)

	s := newTestServerWithGFWList(currentMgr)

	var reconfigureCalls atomic.Int32
	callback := makeGFWListUpdateCallback(s, staleMgr, func(_ context.Context, _ *ServerConfig) error {
		reconfigureCalls.Add(1)

		return nil
	})

	callback(t.Context(), map[string]struct{}{"stale.example.org": {}})

	assert.EqualValues(t, 0, reconfigureCalls.Load(), "stale manager must not trigger reconfigure")
	assert.Nil(t, s.pendingGFWListDomains, "stale manager must not store pending domains")
}

// TestMakeGFWListUpdateCallback_AfterStopGFWList covers the case where the
// manager was cleared entirely (e.g. GFW list was disabled) between the
// download finishing and the callback firing.
func TestMakeGFWListUpdateCallback_AfterStopGFWList(t *testing.T) {
	mgr := newGFWListManager(testLogger, &GFWListConfig{}, t.TempDir(), nil)
	t.Cleanup(mgr.stop)
	s := newTestServerWithGFWList(mgr)

	// Simulate disabling GFW list: the server no longer references the
	// manager.
	s.gfwlist = nil

	var reconfigureCalls atomic.Int32
	callback := makeGFWListUpdateCallback(s, mgr, func(_ context.Context, _ *ServerConfig) error {
		reconfigureCalls.Add(1)

		return nil
	})

	callback(t.Context(), map[string]struct{}{"example.org": {}})

	assert.EqualValues(t, 0, reconfigureCalls.Load())
	assert.Nil(t, s.pendingGFWListDomains)
}

// TestMakeGFWListUpdateCallback_ReconfigureErrorSwallowed verifies that a
// reconfigure failure does not propagate back to the caller — it is only
// logged.  The background updater goroutine must not crash because of a
// transient reconfigure error.
func TestMakeGFWListUpdateCallback_ReconfigureErrorSwallowed(t *testing.T) {
	mgr := newGFWListManager(testLogger, &GFWListConfig{}, t.TempDir(), nil)
	t.Cleanup(mgr.stop)
	s := newTestServerWithGFWList(mgr)

	callback := makeGFWListUpdateCallback(s, mgr, func(_ context.Context, _ *ServerConfig) error {
		return errors.Error("boom")
	})

	domains := map[string]struct{}{"example.org": {}}

	// Must not panic.
	require.NotPanics(t, func() {
		callback(t.Context(), domains)
	})

	// Pending domains are still stored even though reconfigure failed, so
	// the next successful reconfigure can pick them up.
	assert.Equal(t, domains, s.pendingGFWListDomains)
}

// TestMakeGFWListUpdateCallback_ConcurrentStaleAndCurrent checks the race
// that the identity check is designed to prevent: a stale manager and the
// current manager firing their callbacks at the same time.  Only the
// current manager's update should land.
func TestMakeGFWListUpdateCallback_ConcurrentStaleAndCurrent(t *testing.T) {
	staleMgr := newGFWListManager(testLogger, &GFWListConfig{}, t.TempDir(), nil)
	t.Cleanup(staleMgr.stop)
	currentMgr := newGFWListManager(testLogger, &GFWListConfig{}, t.TempDir(), nil)
	t.Cleanup(currentMgr.stop)

	s := newTestServerWithGFWList(currentMgr)

	var reconfigureCalls atomic.Int32
	stub := func(_ context.Context, _ *ServerConfig) error {
		reconfigureCalls.Add(1)

		return nil
	}
	staleCB := makeGFWListUpdateCallback(s, staleMgr, stub)
	currentCB := makeGFWListUpdateCallback(s, currentMgr, stub)

	currentDomains := map[string]struct{}{"current.example.org": {}}
	staleDomains := map[string]struct{}{"stale.example.org": {}}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		staleCB(t.Context(), staleDomains)
	}()
	go func() {
		defer wg.Done()
		currentCB(t.Context(), currentDomains)
	}()
	wg.Wait()

	assert.EqualValues(t, 1, reconfigureCalls.Load(), "only the current manager should trigger reconfigure")
	assert.Equal(t, currentDomains, s.pendingGFWListDomains)
}
