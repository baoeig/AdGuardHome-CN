package dnsforward

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGFWListManagerLoadFromCache_RejectsOversizedFile verifies that an
// oversized local cache file is rejected instead of being read into memory in
// full, mirroring the size limit already enforced on network downloads.
func TestGFWListManagerLoadFromCache_RejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()

	// Write a file larger than maxGFWListSize.  Content does not need to be
	// valid base64/AutoProxy data, since the size check must reject it before
	// parsing is attempted.
	oversized := make([]byte, maxGFWListSize+1)
	err := os.WriteFile(filepath.Join(dir, gfwlistCacheFile), oversized, 0o600)
	require.NoError(t, err)

	m := newGFWListManager(testLogger, &GFWListConfig{}, dir, nil)

	err = m.loadFromCache(t.Context())
	require.Error(t, err)
	assert.ErrorContains(t, err, "too large")
	assert.Equal(t, 0, m.domainCount())
}

func TestGFWListManagerLoadFromCache_RejectsEmptyList(t *testing.T) {
	dir := t.TempDir()
	emptyList := base64.StdEncoding.EncodeToString([]byte("[AutoProxy 0.2.9]\n! no domains\n"))
	err := os.WriteFile(filepath.Join(dir, gfwlistCacheFile), []byte(emptyList), 0o600)
	require.NoError(t, err)

	m := newGFWListManager(testLogger, &GFWListConfig{}, dir, nil)

	err = m.loadFromCache(t.Context())
	require.Error(t, err)
	assert.ErrorContains(t, err, "contains no domains")
	assert.Equal(t, 0, m.domainCount())
}

func TestGFWListManagerSaveToCache_ReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, gfwlistCacheFile)
	err := os.WriteFile(cachePath, []byte("old cache"), 0o600)
	require.NoError(t, err)

	m := newGFWListManager(testLogger, &GFWListConfig{}, dir, nil)

	const newCache = "new cache"
	err = m.saveToCache(t.Context(), []byte(newCache))
	require.NoError(t, err)

	got, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	assert.Equal(t, newCache, string(got))
}
