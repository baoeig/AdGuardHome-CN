package dnsforward

import (
	"context"
	"testing"
	"time"

	"github.com/AdguardTeam/golibs/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadWithRetry(t *testing.T) {
	errBoom := errors.Error("boom")

	t.Run("succeeds_first_try", func(t *testing.T) {
		calls := 0
		body, err := downloadWithRetry(
			t.Context(),
			testLogger,
			time.Millisecond,
			3,
			func(_ context.Context) ([]byte, error) {
				calls++

				return []byte("ok"), nil
			},
		)
		require.NoError(t, err)
		assert.Equal(t, []byte("ok"), body)
		assert.Equal(t, 1, calls)
	})

	t.Run("succeeds_after_retries", func(t *testing.T) {
		calls := 0
		body, err := downloadWithRetry(
			t.Context(),
			testLogger,
			time.Millisecond,
			3,
			func(_ context.Context) ([]byte, error) {
				calls++
				if calls < 3 {
					return nil, errBoom
				}

				return []byte("ok"), nil
			},
		)
		require.NoError(t, err)
		assert.Equal(t, []byte("ok"), body)
		assert.Equal(t, 3, calls)
	})

	t.Run("fails_after_max_attempts", func(t *testing.T) {
		calls := 0
		_, err := downloadWithRetry(
			t.Context(),
			testLogger,
			time.Millisecond,
			3,
			func(_ context.Context) ([]byte, error) {
				calls++

				return nil, errBoom
			},
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, errBoom)
		assert.Equal(t, 3, calls)
	})

	t.Run("respects_context_cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		_, err := downloadWithRetry(
			ctx,
			testLogger,
			time.Hour, // Long backoff so cancellation wins.
			3,
			func(_ context.Context) ([]byte, error) {
				calls++
				cancel()

				return nil, errBoom
			},
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 1, calls)
	})
}
