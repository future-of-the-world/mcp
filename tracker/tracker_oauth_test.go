// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package tracker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExchangeDeviceCode_Success(t *testing.T) {
	t.Parallel()

	deviceRequestCount := 0

	tokenRequestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case deviceRequestCount == 0:
			//nolint:errcheck // test write
			_, _ = w.Write([]byte(`{
				"device_code": "test-device-code",
				"user_code": "ABCD1234",
				"verification_uri": "https://oauth.yandex.com/verify",
				"interval": 1,
				"expires_in": 300
			}`))

			deviceRequestCount++

		case tokenRequestCount < 2:
			//nolint:errcheck // test write
			_, _ = w.Write([]byte(`{"error": "authorization_pending"}`))

			tokenRequestCount++

		default:
			//nolint:errcheck // test write
			_, _ = w.Write([]byte(`{
				"access_token": "test-access-token",
				"expires_in": 3600,
				"refresh_token": "test-refresh-token",
				"token_type": "bearer"
			}`))

			tokenRequestCount++
		}
	}))

	t.Cleanup(server.Close)

	parsed := mustParseURL(t, server.URL)

	token, err := exchangeDeviceCode(t.Context(), oauthConfig{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		TokenURL:     mustParseURL(t, parsed.String()+oauthPathToken),
		DeviceURL:    mustParseURL(t, parsed.String()+oauthPathDevice),
	})
	require.NoError(t, err)
	require.Equal(t, "test-access-token", token)
	require.Equal(t, 1, deviceRequestCount)
	require.GreaterOrEqual(t, tokenRequestCount, 2)
}

func TestExchangeDeviceCode_Expired(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case oauthPathDevice:
			//nolint:errcheck // test write
			_, _ = w.Write([]byte(`{
				"device_code": "test-device-code",
				"user_code": "ABCD1234",
				"verification_uri": "https://oauth.yandex.com/verify",
				"interval": 1,
				"expires_in": 1
			}`))

		case oauthPathToken:
			// Always return pending to simulate expiry.
			//nolint:errcheck // test write
			_, _ = w.Write([]byte(`{"error": "authorization_pending"}`))
		}
	}))

	t.Cleanup(server.Close)

	parsed := mustParseURL(t, server.URL)

	// Use a short context timeout so the test doesn't take long.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	t.Cleanup(cancel)

	_, err := exchangeDeviceCode(ctx, oauthConfig{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		TokenURL:     mustParseURL(t, parsed.String()+oauthPathToken),
		DeviceURL:    mustParseURL(t, parsed.String()+oauthPathDevice),
	})
	// Should fail either from context deadline or expired_token error.
	require.Error(t, err)
}

func TestExchangeDeviceCode_DeviceCodeError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)

		//nolint:errcheck // test write
		_, _ = w.Write([]byte(`{"error": "invalid_client"}`))
	}))

	t.Cleanup(server.Close)

	parsed := mustParseURL(t, server.URL)

	_, err := exchangeDeviceCode(t.Context(), oauthConfig{
		ClientID:     "bad-client",
		ClientSecret: "bad-secret",
		TokenURL:     mustParseURL(t, parsed.String()+oauthPathToken),
		DeviceURL:    mustParseURL(t, parsed.String()+oauthPathDevice),
	})
	require.Error(t, err)
}

func TestExchangeDeviceCode_SlowDown(t *testing.T) {
	t.Parallel()

	tokenRequestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch tokenRequestCount {
		case 0:
			//nolint:errcheck // test write
			_, _ = w.Write([]byte(`{
				"device_code": "test-device-code",
				"user_code": "ABCD1234",
				"verification_uri": "https://oauth.yandex.com/verify",
				"interval": 1,
				"expires_in": 300
			}`))

			tokenRequestCount++

		case 1:
			//nolint:errcheck // test write
			_, _ = w.Write([]byte(`{"error": "slow_down"}`))

			tokenRequestCount++

		default:
			//nolint:errcheck // test write
			_, _ = w.Write([]byte(`{
				"access_token": "test-access-token",
				"expires_in": 3600,
				"token_type": "bearer"
			}`))

			tokenRequestCount++
		}
	}))

	t.Cleanup(server.Close)

	parsed := mustParseURL(t, server.URL)

	token, err := exchangeDeviceCode(t.Context(), oauthConfig{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		TokenURL:     mustParseURL(t, parsed.String()+oauthPathToken),
		DeviceURL:    mustParseURL(t, parsed.String()+oauthPathDevice),
	})
	require.NoError(t, err)
	require.Equal(t, "test-access-token", token)
}

func TestParseOAuthURL(t *testing.T) {
	t.Parallel()

	t.Run("empty string uses default", func(t *testing.T) {
		t.Parallel()

		result := parseOAuthURL("", "https://oauth.yandex.com/token")
		require.Equal(t, "https://oauth.yandex.com/token", result.String())
	})

	t.Run("non-empty string overrides default", func(t *testing.T) {
		t.Parallel()

		result := parseOAuthURL(
			"https://custom.example.com/token",
			"https://oauth.yandex.com/token",
		)
		require.Equal(t, "https://custom.example.com/token", result.String())
	})
}

// mustParseURL parses a URL string, failing the test if it's invalid.
func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	require.NoError(t, err)

	return parsed
}
