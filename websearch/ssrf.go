// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// IPv4 private range constants.
const (
	ipv4ZeroA         = 0
	ipv4Private10A    = 10
	ipv4LoopbackA     = 127
	ipv4LinkLocalA    = 169
	ipv4LinkLocalB    = 254
	ipv4CGNATA        = 100
	ipv4CGNATBLo      = 64
	ipv4CGNATBHi      = 127
	ipv4Private172A   = 172
	ipv4Private172BLo = 16
	ipv4Private172BHi = 31
	ipv4Private192A   = 192
	ipv4Private192B   = 168
	ipv4MulticastA    = 224

	twoParts   = 2
	threeParts = 3
	indexThird = 2
)

// ParseAndCheckScheme parses rawURL and rejects any scheme other than http or
// https. Returns the parsed URL on success.
func ParseAndCheckScheme(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %s: %w", rawURL, err)
	}

	scheme := parsed.Scheme
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf(
			"only http/https URLs are allowed (got %s)",
			scheme,
		)
	}

	return parsed, nil
}

// AssertPublicHost resolves hostname via DNS and rejects private, loopback,
// link-local, and multicast addresses. It also blocks well-known internal
// hostnames such as localhost, *.local, and *.internal.
func AssertPublicHost(ctx context.Context, hostname string) error {
	stripped := strings.Trim(hostname, "[]")
	lower := strings.ToLower(stripped)

	err := rejectBlockedHostname(lower)
	if err != nil {
		return err
	}

	literal := resolveLiteral(stripped)
	if literal != nil {
		if IsPrivateIP(literal) {
			return fmt.Errorf("refusing to fetch private host: %s", hostname)
		}

		return nil
	}

	return resolveAndCheckHost(ctx, hostname)
}

// rejectBlockedHostname returns an error if the hostname matches a known
// internal pattern (localhost, *.local, *.internal).
func rejectBlockedHostname(lower string) error {
	if isBlockedHostname(lower) {
		return fmt.Errorf("refusing to fetch internal host: %s", lower)
	}

	return nil
}

// isBlockedHostname reports whether the hostname is a known internal name.
func isBlockedHostname(lower string) bool {
	return lower == "localhost" ||
		strings.HasSuffix(lower, ".local") ||
		strings.HasSuffix(lower, ".internal")
}

// resolveLiteral tries to interpret host as an IP literal (including loose
// IPv4 forms like hex/octal shorthand). Returns a net.IP or nil.
func resolveLiteral(host string) net.IP {
	if ipAddr := net.ParseIP(host); ipAddr != nil {
		return ipAddr
	}

	if ipStr, ok := ParseLooseIPv4(host); ok {
		return net.ParseIP(ipStr)
	}

	return nil
}

// resolveAndCheckHost performs DNS resolution on hostname and checks each
// returned address against the private IP list.
func resolveAndCheckHost(ctx context.Context, hostname string) error {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, hostname)
	if err != nil {
		return fmt.Errorf("cannot resolve host %q: %w", hostname, err)
	}

	for _, addr := range addrs {
		if IsPrivateIP(addr.IP) {
			return fmt.Errorf(
				"refusing to fetch private host: %s → %s",
				hostname,
				addr.IP,
			)
		}
	}

	return nil
}

// IsPrivateIP reports whether ipAddr belongs to a private, loopback,
// link-local, or multicast range. It handles both IPv4 and IPv6 addresses.
func IsPrivateIP(ipAddr net.IP) bool {
	if ipAddr == nil {
		return false
	}

	ipV4 := ipAddr.To4()
	if ipV4 != nil {
		return isPrivateIPv4(ipV4)
	}

	return isPrivateIPv6(ipAddr)
}

// isPrivateIPv4 checks an IPv4 address against blocked ranges.
func isPrivateIPv4(ipAddr net.IP) bool {
	octetA := ipAddr[0]
	octetB := ipAddr[1]

	return isPrivateIPv4Range(octetA, octetB)
}

// isPrivateIPv4Range checks whether the first two octets fall in a blocked
// range.
//
//nolint:wsl_v5 // switch-case compact style is intentional
func isPrivateIPv4Range(octetA, octetB byte) bool {
	switch {
	case octetA == ipv4ZeroA:
		return true
	case octetA == ipv4Private10A:
		return true
	case octetA == ipv4LoopbackA:
		return true
	case octetA == ipv4LinkLocalA && octetB == ipv4LinkLocalB:
		return true
	case octetA == ipv4Private172A &&
		octetB >= ipv4Private172BLo &&
		octetB <= ipv4Private172BHi:
		return true
	case octetA == ipv4Private192A && octetB == ipv4Private192B:
		return true
	case octetA == ipv4CGNATA &&
		octetB >= ipv4CGNATBLo &&
		octetB <= ipv4CGNATBHi:
		return true
	case octetA >= ipv4MulticastA:
		return true
	default:
		return false
	}
}

// isPrivateIPv6 checks an IPv6 address for loopback, unspecified, link-local,
// unique-local, and IPv4-mapped private ranges.
func isPrivateIPv6(ipAddr net.IP) bool {
	lower := strings.ToLower(ipAddr.String())

	if lower == "::1" || lower == "::" {
		return true
	}

	if strings.HasPrefix(lower, "fe80:") {
		return true
	}

	if strings.HasPrefix(lower, "fc") || strings.HasPrefix(lower, "fd") {
		return true
	}

	mapped := parseIPv4Mapped(lower)
	if mapped != nil {
		return isPrivateIPv4(mapped)
	}

	return false
}

// parseIPv4Mapped extracts the IPv4 portion from a ::ffff:x.x.x.x address.
func parseIPv4Mapped(lower string) net.IP {
	if !strings.HasPrefix(lower, "::ffff:") {
		return nil
	}

	idx := strings.LastIndex(lower, ":")
	if idx < 0 {
		return nil
	}

	v4Str := lower[idx+1:]

	ipAddr := net.ParseIP(v4Str)
	if ipAddr == nil {
		return nil
	}

	return ipAddr.To4()
}

// ParseLooseIPv4 attempts to parse host as a loose IPv4 address (decimal,
// hex with 0x prefix, or octal with leading 0). Returns the canonical
// dotted-decimal string and true on success.
func ParseLooseIPv4(host string) (string, bool) {
	if !isValidLooseChars(host) {
		return "", false
	}

	parts := strings.Split(host, ".")
	if len(parts) < ipv4PartsMin || len(parts) > ipv4PartsMax {
		return "", false
	}

	nums, ok := parseLooseParts(parts)
	if !ok {
		return "", false
	}

	return assembleIPv4(nums)
}

// isValidLooseChars reports whether host contains only characters valid for
// loose IPv4 notation (digits, a-f, A-F, x, X, and dots).
func isValidLooseChars(host string) bool {
	for _, char := range host {
		if !isLooseChar(char) {
			return false
		}
	}

	return true
}

// isLooseChar reports whether a single character is valid in loose IPv4.
//
//nolint:wsl_v5 // switch-case compact style is intentional
func isLooseChar(char rune) bool {
	switch {
	case char >= '0' && char <= '9':
		return true
	case char >= 'a' && char <= 'f':
		return true
	case char >= 'A' && char <= 'F':
		return true
	case char == 'x' || char == 'X':
		return true
	case char == '.':
		return true
	default:
		return false
	}
}

// parseLooseParts converts each dotted part to a numeric value using
// decimal, hex (0x), or octal (0-prefix) interpretation.
func parseLooseParts(parts []string) ([]uint32, bool) {
	nums := make([]uint32, 0, len(parts))

	for _, part := range parts {
		if part == "" {
			return nil, false
		}

		num, ok := parseLoosePart(part)
		if !ok {
			return nil, false
		}

		nums = append(nums, num)
	}

	return nums, true
}

// parseLoosePart parses a single dotted segment as decimal, hex, or octal.
//
//nolint:wsl_v5 // switch-case compact style is intentional
func parseLoosePart(part string) (uint32, bool) {
	switch {
	case strings.HasPrefix(part, "0x") || strings.HasPrefix(part, "0X"):
		return parseUintUnchecked(part[2:], hexBase)
	case len(part) > 1 && part[0] == '0' && part[1] >= '0' && part[1] <= '7':
		return parseUintUnchecked(part, octalBase)
	default:
		return parseUintUnchecked(part, decimalBase)
	}
}

// parseUintUnchecked parses str in the given base and returns the value.
func parseUintUnchecked(str string, base int) (uint32, bool) {
	var result uint32

	for _, char := range str {
		newResult, ok := accumulateDigit(result, char, base)
		if !ok {
			return 0, false
		}

		result = newResult
	}

	return result, true
}

// accumulateDigit multiplies result by base and adds the digit value.
// base is always a small constant (8, 10, or 16), so overflow is impossible.
//
//nolint:gosec // base is always 8/10/16, no overflow possible
func accumulateDigit(result uint32, char rune, base int) (uint32, bool) {
	result *= uint32(base)

	digit := digitValue(char, base)
	if digit < 0 {
		return 0, false
	}

	result += uint32(digit)

	return result, true
}

// digitValue returns the numeric value of char in the given base, or -1 if
// char is not valid in that base.
//
//nolint:wsl_v5 // switch-case compact style is intentional
func digitValue(char rune, base int) int {
	switch {
	case char >= '0' && char <= '9':
		val := int(char - '0')
		if val < base {
			return val
		}
	case char >= 'a' && char <= 'f' && base == hexBase:
		return int(char-'a') + hexDigitOffset
	case char >= 'A' && char <= 'F' && base == hexBase:
		return int(char-'A') + hexDigitOffset
	}

	return -1
}

// assembleIPv4 constructs a canonical dotted-decimal string from 1–4 numeric
// parts following the POSIX inet_aton rules.
func assembleIPv4(nums []uint32) (string, bool) {
	octets, ok := computeOctets(nums)
	if !ok {
		return "", false
	}

	return fmt.Sprintf(
		"%d.%d.%d.%d",
		octets[0],
		octets[1],
		octets[2],
		octets[3],
	), true
}

// computeOctets returns the four octets for the given numeric parts.
//
//nolint:wsl_v5 // switch-case compact style is intentional
func computeOctets(nums []uint32) ([ipv4OctetCount]byte, bool) {
	switch len(nums) {
	case 1:
		return computeOctets1(nums[0])
	case twoParts:
		return computeOctets2(nums)
	case threeParts:
		return computeOctets3(nums)
	case ipv4OctetCount:
		return computeOctets4(nums)
	default:
		return [ipv4OctetCount]byte{}, false
	}
}

// computeOctets1 handles the single-value case.
func computeOctets1(val uint32) ([ipv4OctetCount]byte, bool) {
	return [ipv4OctetCount]byte{
		byte(val >> bitShift24),
		byte(val >> bitShift16),
		byte(val >> bitShift8),
		byte(val),
	}, true
}

// computeOctets2 handles the two-part case (a, rest).
func computeOctets2(nums []uint32) ([ipv4OctetCount]byte, bool) {
	if nums[0] > ipv4MaxOctet || nums[1] > ipv4MaxTriple {
		return [ipv4OctetCount]byte{}, false
	}

	rest := nums[1]

	return [ipv4OctetCount]byte{
		byte(nums[0]),
		byte(rest >> bitShift16),
		byte(rest >> bitShift8),
		byte(rest),
	}, true
}

// computeOctets3 handles the three-part case (a, b, rest).
func computeOctets3(nums []uint32) ([ipv4OctetCount]byte, bool) {
	if nums[0] > ipv4MaxOctet ||
		nums[1] > ipv4MaxOctet ||
		nums[indexThird] > ipv4MaxWord {

		return [ipv4OctetCount]byte{}, false
	}

	rest := nums[indexThird]

	return [ipv4OctetCount]byte{
		byte(nums[0]),
		byte(nums[1]),
		byte(rest >> bitShift8),
		byte(rest),
	}, true
}

// computeOctets4 handles the standard four-part case.
func computeOctets4(nums []uint32) ([ipv4OctetCount]byte, bool) {
	for _, num := range nums {
		if num > ipv4MaxOctet {
			return [ipv4OctetCount]byte{}, false
		}
	}

	return [ipv4OctetCount]byte{
		byte(nums[0]),
		byte(nums[1]),
		byte(nums[2]),
		byte(nums[3]),
	}, true
}
