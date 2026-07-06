// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetch_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)

		_, _ = w.Write([]byte("ok")) //nolint:errcheck // test helper
	}))
	defer server.Close()

	resp, err := Fetch(t.Context(), server.URL, FetchOptions{})
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, "ok", string(body))
}

func TestFetch_SetsUserAgent(t *testing.T) {
	t.Parallel()

	var gotUA string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	resp, err := Fetch(t.Context(), server.URL, FetchOptions{})
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, userAgent, gotUA)
}

func TestFetch_CustomHeaders(t *testing.T) {
	t.Parallel()

	var gotHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	resp, err := Fetch(t.Context(), server.URL, FetchOptions{
		Headers: map[string]string{"X-Custom": "test-value"},
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, "test-value", gotHeader)
}

func TestFetch_RetryOnStatus503(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		w.WriteHeader(http.StatusOK)

		_, _ = w.Write([]byte("recovered")) //nolint:errcheck // test helper
	}))
	defer server.Close()

	resp, err := Fetch(t.Context(), server.URL, FetchOptions{Retries: 2, Timeout: 5 * time.Second})
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(3), attempts.Load())
}

func TestFetch_NoRetryOnStatus400(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	resp, err := Fetch(t.Context(), server.URL, FetchOptions{Retries: 2})
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, int32(1), attempts.Load())
}

func TestFetch_ConnectionRefused(t *testing.T) {
	t.Parallel()

	// Use a port that nothing is listening on.
	resp, err := Fetch(t.Context(), "http://127.0.0.1:1", FetchOptions{
		Retries: 1,
		Timeout: 2 * time.Second,
	})

	if resp != nil {
		defer func() { require.NoError(t, resp.Body.Close()) }()
	}

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestFetch_ContextCancellation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Use a very short timeout so the request is canceled before completing.
	resp, err := Fetch(t.Context(), server.URL, FetchOptions{
		Timeout: 50 * time.Millisecond,
		Retries: 0,
	})

	if resp != nil {
		defer func() { require.NoError(t, resp.Body.Close()) }()
	}

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestIsRetryableStatus(t *testing.T) {
	t.Parallel()

	expected := []int{
		http.StatusRequestTimeout,
		http.StatusTooEarly,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	}

	for _, code := range expected {
		t.Run(http.StatusText(code), func(t *testing.T) {
			t.Parallel()

			assert.True(t, isRetryableStatus(code))
		})
	}

	nonRetryable := []int{
		http.StatusOK,
		http.StatusCreated,
		http.StatusMovedPermanently,
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusMethodNotAllowed,
	}

	for _, code := range nonRetryable {
		t.Run(http.StatusText(code), func(t *testing.T) {
			t.Parallel()

			assert.False(t, isRetryableStatus(code))
		})
	}
}

func TestRandByte(t *testing.T) {
	t.Parallel()

	for range 100 {
		val := randByte()

		assert.GreaterOrEqual(t, val, 0)
		assert.Less(t, val, maxJitterMs)
	}
}
