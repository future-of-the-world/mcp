// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// OAuth HTTP constants.
const (
	oauthPathToken  = "/token"
	oauthPathDevice = "/device/code"

	oauthGrantTypeDevice   = "device_code"
	oauthContentType       = "application/x-www-form-urlencoded"
	oauthParamClientID     = "client_id"
	oauthParamClientSecret = "client_secret"
	oauthParamGrantType    = "grant_type"
	oauthParamCode         = "code"

	oauthErrorPending = "authorization_pending"
	oauthErrorSlow    = "slow_down"
	oauthErrorExpired = "expired_token"

	oauthMinPollInterval = 5 * time.Second
	oauthSlowDownDelta   = 5 * time.Second
)

// oauthConfig holds the OAuth client credentials and endpoints.
type oauthConfig struct {
	ClientID     string
	ClientSecret string
	TokenURL     *url.URL
	DeviceURL    *url.URL
}

// tokenRequestParams holds parameters for a single token exchange request.
type tokenRequestParams struct {
	TokenURL     *url.URL
	ClientID     string
	ClientSecret string
	DeviceCode   string
}

// parseOAuthURL parses raw as a URL, falling back to defaultIfEmpty when raw is blank.
func parseOAuthURL(raw, defaultIfEmpty string) *url.URL {
	if raw == "" {
		raw = defaultIfEmpty
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		// URL constants are validated at compile time; this should never happen.
		panic("parse OAuth URL: " + err.Error())
	}

	return parsed
}

// exchangeDeviceCode performs the Yandex OAuth device authorization flow
// and returns an access token.
//
// Device code request uses golang.org/x/oauth2.Config.DeviceAuth.
// Token polling is implemented manually because Yandex requires
// grant_type=device_code (short form) while the oauth2 library sends the
// RFC 8628 URN form urn:ietf:params:oauth:grant-type:device_code.
func exchangeDeviceCode(ctx context.Context, cfg oauthConfig) (string, error) {
	oauthCfg := oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:       "",
			DeviceAuthURL: cfg.DeviceURL.String(),
			TokenURL:      cfg.TokenURL.String(),
			AuthStyle:     oauth2.AuthStyleAutoDetect,
		},
	}

	// Step 1: Request device code.
	deviceResp, err := oauthCfg.DeviceAuth(ctx)
	if err != nil {
		return "", fmt.Errorf("request device code: %w", err)
	}

	// Step 2: Display instructions to the user.
	log.Printf("[tracker] OAuth: Please visit %s and enter code: %s",
		deviceResp.VerificationURI,
		deviceResp.UserCode,
	)

	// Step 3: Poll for token with a deadline derived from device code expiry.
	pollCtx, cancel := context.WithDeadline(ctx, deviceResp.Expiry)
	defer cancel()

	params := tokenRequestParams{
		TokenURL:     cfg.TokenURL,
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		DeviceCode:   deviceResp.DeviceCode,
	}

	token, err := pollForToken(pollCtx, params)
	if err != nil {
		return "", fmt.Errorf("poll for token: %w", err)
	}

	log.Printf("[tracker] OAuth: token obtained successfully (expires in %s)",
		time.Until(token.Expiry))

	log.Println(
		"[tracker] OAuth: you can use this token directly in config to avoid the device flow:",
	)
	log.Printf("[tracker]   token: %s", token.AccessToken)

	return token.AccessToken, nil
}

// pollForToken polls the token endpoint until the user authorizes the device code.
// It sends grant_type=device_code (Yandex's expected format) and handles
// authorization_pending, slow_down, and expired_token responses.
func pollForToken(ctx context.Context, params tokenRequestParams) (*oauth2.Token, error) {
	interval := oauthMinPollInterval

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context canceled: %w", context.Cause(ctx))

		case <-time.After(interval):
		}

		tokenResp, err := requestToken(ctx, params)
		if err != nil {
			return nil, err
		}

		switch tokenResp.Error {
		case oauthErrorPending:
			// User hasn't authorized yet, keep polling.
			continue

		case oauthErrorSlow:
			interval += oauthSlowDownDelta

			log.Printf("[tracker] OAuth: polling too fast, increasing interval to %s", interval)

			continue

		case oauthErrorExpired:
			return nil, fmt.Errorf("%w: device code expired", errOAuthFailed)

		case "":
			return tokenResp.token()

		default:
			return nil, fmt.Errorf("%w: %s: %s",
				errOAuthFailed,
				tokenResp.Error,
				tokenResp.ErrorDescription,
			)
		}
	}
}

// oauthTokenResponse holds the response from the OAuth token exchange request.
type oauthTokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error,omitempty"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// token converts the raw response into an oauth2.Token.
func (r *oauthTokenResponse) token() (*oauth2.Token, error) {
	if r.AccessToken == "" {
		return nil, fmt.Errorf("%w: empty access token", errOAuthFailed)
	}

	return &oauth2.Token{
		AccessToken:  r.AccessToken,
		TokenType:    r.TokenType,
		RefreshToken: r.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(r.ExpiresIn) * time.Second),
		ExpiresIn:    int64(r.ExpiresIn),
	}, nil
}

// requestToken sends a single token exchange request to the OAuth provider.
// It uses grant_type=device_code (short form) which is what Yandex expects.
func requestToken(ctx context.Context, params tokenRequestParams) (*oauthTokenResponse, error) {
	data := url.Values{}
	data.Set(oauthParamGrantType, oauthGrantTypeDevice)
	data.Set(oauthParamCode, params.DeviceCode)
	data.Set(oauthParamClientID, params.ClientID)
	data.Set(oauthParamClientSecret, params.ClientSecret)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		params.TokenURL.String(),
		strings.NewReader(data.Encode()),
	)
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}

	req.Header.Set("Content-Type", oauthContentType)

	client := &http.Client{Transport: &http.Transport{}}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}

	//nolint:errcheck // body close error is not critical
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}

	var tokenResp oauthTokenResponse

	err = json.Unmarshal(body, &tokenResp)
	if err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}

	return &tokenResp, nil
}
