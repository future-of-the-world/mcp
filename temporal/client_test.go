// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

//nolint:exhaustruct,wsl_v5 // test fixtures use partial structs and cluster assertions
package temporal

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- isLocalHost ---

func TestIsLocalHost(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		host string
		want bool
	}{
		{"empty host", "", true},
		{"localhost", "localhost:7233", true},
		{"127.0.0.1", "127.0.0.1:7233", true},
		{"host.docker.internal", "host.docker.internal:7233", true},
		{"remote hostname", "temporal.prod.example.com", false},
		{"ipv6 loopback", "[::1]:7233", false}, // not in the upstream's needle list
		{"bare hostname", "internal-temporal", false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, testCase.want, isLocalHost(testCase.host))
		})
	}
}

// --- determineTLSConfig: priority order ---

// TestDetermineTLSConfig_PriorityOrder walks the documented priority
// order documented in the issue:
//
//  1. mTLS cert+key both set → returns *tls.Config with the client
//     cert loaded.
//  2. API key set → *tls.Config with MinVersion = TLS 1.2 (server
//     cert verification).
//  3. tls_enabled = true → same as #2.
//  4. tls_enabled = false → nil.
//  5. tls_enabled absent + remote host → same as #2.
//  6. tls_enabled absent + local host → nil.
func TestDetermineTLSConfig_PriorityOrder(t *testing.T) {
	t.Parallel()

	t.Run("mTLS path", func(t *testing.T) {
		t.Parallel()

		// We do NOT want to read real cert files here — the loader
		// is exercised by loadMTLSTLSConfig's own test below. For
		// this priority test, the loadMTLSTLSConfig call inside
		// determineTLSConfig will return nil if the files don't
		// exist, which is fine: we are testing that the mTLS branch
		// is taken (returning whatever loadMTLSTLSConfig returns).
		cfg := &config{
			Host:              "remote.example.com:7233",
			TLSClientCertPath: "/nonexistent/cert.pem",
			TLSClientKeyPath:  "/nonexistent/key.pem",
		}
		got := determineTLSConfig(cfg)
		assert.Nilf(
			t,
			got,
			"mTLS path delegates to loadMTLSTLSConfig which returns nil on missing files",
		)
	})

	t.Run("api_key forces TLS", func(t *testing.T) {
		t.Parallel()

		cfg := &config{Host: "localhost:7233", APIKey: "tmprl-secret"}
		got := determineTLSConfig(cfg)
		require.NotNil(t, got)
		assert.Equal(t, uint16(tls.VersionTLS12), got.MinVersion)
	})

	t.Run("tls_enabled=true", func(t *testing.T) {
		t.Parallel()
		yes := true
		cfg := &config{Host: "localhost:7233", TLSEnabled: &yes}
		got := determineTLSConfig(cfg)
		require.NotNil(t, got)
		assert.Equal(t, uint16(tls.VersionTLS12), got.MinVersion)
	})

	t.Run("tls_enabled=false", func(t *testing.T) {
		t.Parallel()
		disabled := false
		cfg := &config{Host: "remote.example.com:7233", TLSEnabled: &disabled}
		got := determineTLSConfig(cfg)
		assert.Nil(t, got)
	})

	t.Run("auto-local", func(t *testing.T) {
		t.Parallel()
		cfg := &config{Host: "localhost:7233"}
		got := determineTLSConfig(cfg)
		assert.Nil(t, got)
	})

	t.Run("auto-remote", func(t *testing.T) {
		t.Parallel()
		cfg := &config{Host: "temporal.prod.example.com:7233"}
		got := determineTLSConfig(cfg)
		require.NotNil(t, got)
		assert.Equal(t, uint16(tls.VersionTLS12), got.MinVersion)
	})

	t.Run("mTLS beats api_key", func(t *testing.T) {
		t.Parallel()

		// When both are set, mTLS branch wins. loadMTLSTLSConfig
		// returns nil for missing files, so the net result is the
		// same shape as the "mTLS path" branch above.
		cfg := &config{
			Host:              "remote.example.com:7233",
			APIKey:            "tmprl-secret",
			TLSClientCertPath: "/nonexistent/cert.pem",
			TLSClientKeyPath:  "/nonexistent/key.pem",
		}
		got := determineTLSConfig(cfg)
		assert.Nilf(t, got, "mTLS branch taken, file loader returns nil for missing files")
	})

	t.Run("api_key beats tls_enabled=false", func(t *testing.T) {
		t.Parallel()

		// The explicit tls_enabled=false is checked AFTER api_key in
		// determineTLSConfig. The api_key branch fires first and
		// returns a TLS config; the explicit tls_enabled=false is
		// captured separately via ConnectionOptions.TLSDisabled in
		// newClientManager. This test documents the priority.
		disabled := false
		cfg := &config{
			Host:       "localhost:7233",
			APIKey:     "tmprl-secret",
			TLSEnabled: &disabled,
		}
		got := determineTLSConfig(cfg)
		require.NotNilf(t, got, "api_key branch fires before tls_enabled branch")
		assert.Equal(t, uint16(tls.VersionTLS12), got.MinVersion)
	})
}

// --- determineCredentials ---

// TestDetermineCredentials_APIKey confirms the API-key path.
func TestDetermineCredentials_APIKey(t *testing.T) {
	t.Parallel()

	cfg := &config{APIKey: "tmprl-secret"}
	got := determineCredentials(cfg)
	assert.NotNilf(t, got, "api_key → non-nil credentials")
}

// TestDetermineCredentials_NoAuth confirms the no-auth path.
func TestDetermineCredentials_NoAuth(t *testing.T) {
	t.Parallel()

	cfg := &config{Host: "localhost:7233"}
	got := determineCredentials(cfg)
	assert.Nilf(t, got, "no auth → nil credentials")
}

// TestDetermineCredentials_MTLSDelegatesToTLS confirms that mTLS does
// NOT use determineCredentials (it bakes the cert into the *tls.Config
// instead). Per the SDK doc, both paths would conflict at NewLazyClient.
func TestDetermineCredentials_MTLSDelegatesToTLS(t *testing.T) {
	t.Parallel()

	cfg := &config{
		Host:              "remote.example.com:7233",
		TLSClientCertPath: "/tmp/cert.pem",
		TLSClientKeyPath:  "/tmp/key.pem",
	}
	got := determineCredentials(cfg)
	assert.Nilf(t, got, "mTLS handled by determineTLSConfig, not here")
}

// --- loadMTLSClientCert ---

// TestLoadMTLSClientCert_MissingFile confirms the missing-file branch.
func TestLoadMTLSClientCert_MissingFile(t *testing.T) {
	t.Parallel()

	_, err := loadMTLSClientCert("/nonexistent/cert.pem", "/nonexistent/key.pem")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

// TestLoadMTLSClientCert_InvalidPEM confirms that a present-but-invalid
// cert pair fails at tls.X509KeyPair, not at os.ReadFile.
func TestLoadMTLSClientCert_InvalidPEM(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	require.NoError(t, os.WriteFile(certPath, []byte("not a PEM"), 0o600))
	require.NoError(t, os.WriteFile(keyPath, []byte("not a PEM either"), 0o600))

	_, err := loadMTLSClientCert(certPath, keyPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key pair invalid")
}

// --- loadMTLSClientCert / loadMTLSTLSConfig happy paths ---

// writeSelfSignedCertPair generates a throw-away self-signed cert and
// matching ECDSA private key in t.TempDir() and returns the paths. The
// material is intentionally not trusted by any CA — loadMTLSTLSConfig
// and loadMTLSClientCert only need a valid PEM-encoded X509KeyPair.
func writeSelfSignedCertPair(t *testing.T) (certPath, keyPath string) {
	t.Helper()

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	require.NoError(t, err)

	notBefore := time.Now().Add(-time.Hour)
	notAfter := time.Now().Add(time.Hour)

	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "temporal-coverage-test"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	derBytes, err := x509.CreateCertificate(
		rand.Reader, &template, &template, &priv.PublicKey, priv,
	)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))

	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))

	return certPath, keyPath
}

// TestLoadMTLSClientCert_ValidPair confirms the happy path: a valid
// PEM-encoded X509KeyPair round-trips through loadMTLSClientCert to a
// non-zero tls.Certificate. Lifts function coverage above the 64.3%
// baseline recorded in the issue baseline.
func TestLoadMTLSClientCert_ValidPair(t *testing.T) {
	t.Parallel()

	certPath, keyPath := writeSelfSignedCertPair(t)

	cert, err := loadMTLSClientCert(certPath, keyPath)
	require.NoError(t, err)
	require.Lenf(t, cert.Certificate, 1, "self-signed cert chain has exactly one entry")
	assert.NotEmptyf(t, cert.PrivateKey, "loaded cert carries a private key")
}

// TestLoadMTLSClientCert_KeyMissing confirms that a missing KEY file
// is reported separately from a missing cert file. The error message
// and the wrapper target the keyPath, not certPath.
func TestLoadMTLSClientCert_KeyMissing(t *testing.T) {
	t.Parallel()

	certPath, _ := writeSelfSignedCertPair(t)
	keyPath := filepath.Join(t.TempDir(), "nonexistent-key.pem")

	_, err := loadMTLSClientCert(certPath, keyPath)
	require.Error(t, err)
	require.ErrorIs(t, err, errTLSCertMissing)
	assert.Containsf(t, err.Error(), keyPath, "error message names the missing key path")
}

// TestLoadMTLSClientCert_PathIsDirectory covers the
// errTLSCertUnreadable branch in loadMTLSClientCert for the cert
// path. Reading a directory fails with EIFDIR — not IsNotExist —
// so the loader takes the errTLSCertUnreadable wrapper instead of
// errTLSCertMissing.
func TestLoadMTLSClientCert_PathIsDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	_, err := loadMTLSClientCert(dir, dir)
	require.Error(t, err)
	require.NotErrorIsf(t, err, errTLSCertMissing, "directory read is not a missing-file error")
	require.ErrorIsf(t, err, errTLSCertUnreadable, "directory read → errTLSCertUnreadable")
}

// TestLoadMTLSClientCert_KeyPathIsDirectory covers the key-read
// errTLSCertUnreadable branch: the cert file is valid but the key
// path is a directory. Reading the directory fails non-IsNotExist →
// wrapped as errTLSCertUnreadable keyed to the keyPath.
func TestLoadMTLSClientCert_KeyPathIsDirectory(t *testing.T) {
	t.Parallel()

	certPath, _ := writeSelfSignedCertPair(t)

	dir := t.TempDir() // used as the unreadable key path
	_, err := loadMTLSClientCert(certPath, dir)
	require.Error(t, err)
	require.ErrorIsf(t, err, errTLSCertUnreadable, "key-path directory read → errTLSCertUnreadable")
	assert.NotErrorIs(t, err, errTLSCertMissing)
}

// TestLoadMTLSTLSConfig_ValidPair confirms the happy path: a valid
// PEM pair produces a *tls.Config with one Certificate and TLS 1.2+
// minimum. This lifts loadMTLSTLSConfig from 80% (only the
// error-return branch hit) to 100%.
func TestLoadMTLSTLSConfig_ValidPair(t *testing.T) {
	t.Parallel()

	certPath, keyPath := writeSelfSignedCertPair(t)

	cfg := loadMTLSTLSConfig(certPath, keyPath)
	require.NotNilf(t, cfg, "valid cert pair → non-nil *tls.Config")
	require.Lenf(t, cfg.Certificates, 1, "expected exactly one client certificate")
	assert.Equalf(t, uint16(tls.VersionTLS12), cfg.MinVersion, "min TLS 1.2")
	assert.Falsef(t, cfg.InsecureSkipVerify, "server cert verification stays on")
}

// --- newClientManager ---

// TestNewClientManager_MinimalConfig confirms the happy path:
// newClientManager builds a *clientManager from a minimal config
// without dialing. client.NewLazyClient is non-blocking per the SDK
// contract.
func TestNewClientManager_MinimalConfig(t *testing.T) {
	t.Parallel()

	cfg := &config{Host: "localhost:7233", Namespace: "default"}

	manager, err := newClientManager(t.Context(), cfg)
	require.NoError(t, err)
	require.NotNilf(t, manager, "lazy client constructed → non-nil *clientManager")
}

// TestNewClientManager_TLSDisabled exercises the tls_enabled=false
// branch: a nil *tls.Config from determineTLSConfig plus an
// explicit TLSEnabled=false flips ConnectionOptions.TLSDisabled = true.
func TestNewClientManager_TLSDisabled(t *testing.T) {
	t.Parallel()

	disabled := false
	cfg := &config{
		Host:       "localhost:7233",
		Namespace:  "default",
		TLSEnabled: &disabled,
	}

	manager, err := newClientManager(t.Context(), cfg)
	require.NoError(t, err)
	assert.NotNil(t, manager)
}

// TestNewClientManager_WithAPIKey exercises the api_key branch:
// determineTLSConfig returns a non-nil *tls.Config AND
// determineCredentials returns non-nil client.Credentials. Both
// branches in newClientManager need a real config to fire.
func TestNewClientManager_WithAPIKey(t *testing.T) {
	t.Parallel()

	cfg := &config{
		Host:      "temporal.prod.example.com:7233",
		Namespace: "production",
		APIKey:    "tmprl-secret",
	}

	manager, err := newClientManager(t.Context(), cfg)
	require.NoError(t, err)
	require.NotNil(t, manager)
}

// --- (*clientManager).Close ---

// TestClientManager_CloseNilSafe confirms that calling Close on a
// nil receiver does not panic. The first line of Close is the nil
// guard.
func TestClientManager_CloseNilSafe(t *testing.T) {
	t.Parallel()

	var nilCM *clientManager
	assert.NotPanicsf(t, func() { nilCM.Close() }, "Close on nil receiver must be a no-op")
}

// TestClientManager_CloseIdempotent confirms that calling Close
// twice on a manager whose underlying client is nil does not panic.
// The second call must also be a no-op.
func TestClientManager_CloseIdempotent(t *testing.T) {
	t.Parallel()

	manager := &clientManager{}
	assert.NotPanicsf(t, func() { manager.Close() }, "first Close with nil SDK client is safe")
	assert.NotPanicsf(t, func() { manager.Close() }, "second Close must also be safe")
}

// TestClientManager_CloseRealLazyClient confirms that Close is
// safe on a real (non-nil) lazy client. newClientManager builds a
// lazy client that has not dialed — Close must still be safe.
func TestClientManager_CloseRealLazyClient(t *testing.T) {
	t.Parallel()

	cfg := &config{Host: "localhost:7233", Namespace: "default"}

	manager, err := newClientManager(t.Context(), cfg)
	require.NoError(t, err)
	require.NotNil(t, manager)

	assert.NotPanicsf(t, manager.Close, "Close on a live lazy client")
}

// --- (*clientManager).ScheduleClient ---

// TestClientManager_ScheduleClientNilReceiver confirms that
// ScheduleClient returns nil when called on a nil receiver.
func TestClientManager_ScheduleClientNilReceiver(t *testing.T) {
	t.Parallel()

	var nilCM *clientManager
	assert.Nilf(t, nilCM.ScheduleClient(), "nil receiver → nil schedule client")
}

// TestClientManager_ScheduleClient returns a non-nil schedule
// sub-client from a freshly-built lazy client. This is the only
// branch in (*clientManager).ScheduleClient beyond the nil guard.
func TestClientManager_ScheduleClient(t *testing.T) {
	t.Parallel()

	cfg := &config{Host: "localhost:7233", Namespace: "default"}

	manager, err := newClientManager(t.Context(), cfg)
	require.NoError(t, err)
	require.NotNil(t, manager)

	sched := manager.ScheduleClient()
	assert.NotNilf(t, sched, "ScheduleClient must return non-nil from a live *clientManager")
}
