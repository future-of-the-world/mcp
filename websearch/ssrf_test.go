// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAndCheckScheme_HTTP(t *testing.T) {
	t.Parallel()

	parsed, err := ParseAndCheckScheme("http://example.com/path")
	require.NoError(t, err)
	assert.Equal(t, "http", parsed.Scheme)
	assert.Equal(t, "example.com", parsed.Hostname())
}

func TestParseAndCheckScheme_HTTPS(t *testing.T) {
	t.Parallel()

	parsed, err := ParseAndCheckScheme("https://example.com/path")
	require.NoError(t, err)
	assert.Equal(t, "https", parsed.Scheme)
}

func TestParseAndCheckScheme_FTP(t *testing.T) {
	t.Parallel()

	_, err := ParseAndCheckScheme("ftp://example.com/file")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only http/https")
}

func TestParseAndCheckScheme_FileScheme(t *testing.T) {
	t.Parallel()

	_, err := ParseAndCheckScheme("file:///etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only http/https")
}

func TestParseAndCheckScheme_InvalidURL(t *testing.T) {
	t.Parallel()

	_, err := ParseAndCheckScheme("://missing-scheme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid URL")
}

func TestAssertPublicHost_Localhost(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "localhost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "internal host")
}

func TestAssertPublicHost_DotLocal(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "myhost.local")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "internal host")
}

func TestAssertPublicHost_DotInternal(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "service.internal")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "internal host")
}

func TestAssertPublicHost_LoopbackIP(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "127.0.0.1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private host")
}

func TestAssertPublicHost_PrivateIPv4_10(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "10.0.0.1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private host")
}

func TestAssertPublicHost_PrivateIPv4_172(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "172.16.0.1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private host")
}

func TestAssertPublicHost_PrivateIPv4_172_31(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "172.31.255.255")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private host")
}

func TestAssertPublicHost_PublicIPv4_172(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "172.15.0.1")
	assert.NoError(t, err)
}

func TestAssertPublicHost_PrivateIPv4_192(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "192.168.1.1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private host")
}

func TestAssertPublicHost_LinkLocal(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "169.254.0.1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private host")
}

func TestAssertPublicHost_CGNAT(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "100.64.0.1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private host")
}

func TestAssertPublicHost_Multicast(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "224.0.0.1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private host")
}

func TestAssertPublicHost_IPv6Loopback(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "::1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private host")
}

func TestAssertPublicHost_IPv6Unspecified(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "::")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private host")
}

func TestAssertPublicHost_IPv6LinkLocal(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "fe80::1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private host")
}

func TestAssertPublicHost_IPv6UniqueLocalFC(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "fc00::1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private host")
}

func TestAssertPublicHost_IPv6UniqueLocalFD(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "fd00::1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private host")
}

func TestAssertPublicHost_IPv6MappedPrivate(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "::ffff:127.0.0.1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private host")
}

func TestAssertPublicHost_IPv6Bracketed(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(t.Context(), "[::1]")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private host")
}

func TestAssertPublicHost_Unresolvable(t *testing.T) {
	t.Parallel()

	err := AssertPublicHost(
		t.Context(),
		"this-host-definitely-does-not-exist.invalid",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot resolve")
}

func TestIsPrivateIP_Nil(t *testing.T) {
	t.Parallel()

	assert.False(t, IsPrivateIP(net.IP(nil)))
}

func TestIsPrivateIP_Public(t *testing.T) {
	t.Parallel()

	publicAddrs := []string{
		"8.8.8.8",
		"1.1.1.1",
		"203.0.113.1",
		"172.15.0.1",
		"192.169.0.1",
	}

	for _, addr := range publicAddrs {
		ipAddr := net.ParseIP(addr)
		require.NotNil(t, ipAddr)
		assert.Falsef(t, IsPrivateIP(ipAddr), "expected %s to be public", addr)
	}
}

func TestIsPrivateIP_PrivateIPv4(t *testing.T) {
	t.Parallel()

	privateAddrs := []string{
		"0.0.0.0",
		"10.0.0.1",
		"127.0.0.1",
		"169.254.0.1",
		"172.16.0.1",
		"172.31.255.255",
		"192.168.0.1",
		"100.64.0.1",
		"100.127.255.255",
		"224.0.0.1",
		"255.255.255.255",
	}

	for _, addr := range privateAddrs {
		ipAddr := net.ParseIP(addr)
		require.NotNil(t, ipAddr)
		assert.Truef(t, IsPrivateIP(ipAddr), "expected %s to be private", addr)
	}
}

func TestIsPrivateIP_PrivateIPv6(t *testing.T) {
	t.Parallel()

	privateAddrs := []string{
		"::1",
		"::",
		"fe80::1",
		"fc00::1",
		"fd00::1",
		"::ffff:127.0.0.1",
		"::ffff:10.0.0.1",
	}

	for _, addr := range privateAddrs {
		ipAddr := net.ParseIP(addr)
		require.NotNil(t, ipAddr)
		assert.Truef(t, IsPrivateIP(ipAddr), "expected %s to be private", addr)
	}
}

func TestIsPrivateIP_PublicIPv6(t *testing.T) {
	t.Parallel()

	publicAddrs := []string{
		"2001:4860:4860::8888",
		"2606:4700:4700::1111",
	}

	for _, addr := range publicAddrs {
		ipAddr := net.ParseIP(addr)
		require.NotNil(t, ipAddr)
		assert.Falsef(t, IsPrivateIP(ipAddr), "expected %s to be public", addr)
	}
}

func TestParseLooseIPv4_Standard(t *testing.T) {
	t.Parallel()

	result, ok := ParseLooseIPv4("192.168.1.1")
	require.True(t, ok)
	assert.Equal(t, "192.168.1.1", result)
}

func TestParseLooseIPv4_Hex(t *testing.T) {
	t.Parallel()

	result, ok := ParseLooseIPv4("0xC0.0xA8.0x01.0x01")
	require.True(t, ok)
	assert.Equal(t, "192.168.1.1", result)
}

func TestParseLooseIPv4_Octal(t *testing.T) {
	t.Parallel()

	result, ok := ParseLooseIPv4("0300.0250.01.01")
	require.True(t, ok)
	assert.Equal(t, "192.168.1.1", result)
}

func TestParseLooseIPv4_SingleValue(t *testing.T) {
	t.Parallel()

	result, ok := ParseLooseIPv4("3232235777")
	require.True(t, ok)
	assert.Equal(t, "192.168.1.1", result)
}

func TestParseLooseIPv4_TwoParts(t *testing.T) {
	t.Parallel()

	result, ok := ParseLooseIPv4("192.168257")
	require.True(t, ok)
	assert.Equal(t, "192.2.145.65", result)
}

func TestParseLooseIPv4_ThreeParts(t *testing.T) {
	t.Parallel()

	result, ok := ParseLooseIPv4("192.168.257")
	require.True(t, ok)
	assert.Equal(t, "192.168.1.1", result)
}

func TestParseLooseIPv4_InvalidChars(t *testing.T) {
	t.Parallel()

	_, ok := ParseLooseIPv4("hello")
	assert.False(t, ok)
}

func TestParseLooseIPv4_Empty(t *testing.T) {
	t.Parallel()

	_, ok := ParseLooseIPv4("")
	assert.False(t, ok)
}

func TestParseLooseIPv4_TooManyParts(t *testing.T) {
	t.Parallel()

	_, ok := ParseLooseIPv4("1.2.3.4.5")
	assert.False(t, ok)
}

func TestParseLooseIPv4_Loopback(t *testing.T) {
	t.Parallel()

	result, ok := ParseLooseIPv4("127.0.0.1")
	require.True(t, ok)
	assert.Equal(t, "127.0.0.1", result)
}

func TestParseLooseIPv4_HexWhole(t *testing.T) {
	t.Parallel()

	result, ok := ParseLooseIPv4("0x7F000001")
	require.True(t, ok)
	assert.Equal(t, "127.0.0.1", result)
}

func TestParseLooseIPv4_PartOverflow(t *testing.T) {
	t.Parallel()

	_, ok := ParseLooseIPv4("256.0.0.0")
	assert.False(t, ok)
}

func TestParseLooseIPv4_EmptyPart(t *testing.T) {
	t.Parallel()

	_, ok := ParseLooseIPv4("192..1.1")
	assert.False(t, ok)
}

func TestRejectBlockedHostname_Localhost(t *testing.T) {
	t.Parallel()

	err := rejectBlockedHostname("localhost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "internal host")
}

func TestRejectBlockedHostname_SubdomainLocal(t *testing.T) {
	t.Parallel()

	err := rejectBlockedHostname("myhost.local")
	require.Error(t, err)
}

func TestRejectBlockedHostname_SubdomainInternal(t *testing.T) {
	t.Parallel()

	err := rejectBlockedHostname("svc.internal")
	require.Error(t, err)
}

func TestRejectBlockedHostname_Public(t *testing.T) {
	t.Parallel()

	err := rejectBlockedHostname("example.com")
	assert.NoError(t, err)
}
