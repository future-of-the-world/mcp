// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"time"
)

const (
	idPrefix   = "r_"
	idStoreMax = 2000
	idStoreTTL = 1 * time.Hour
	idHexLen   = 12
)

var idPattern = regexp.MustCompile(`^r_[0-9a-f]{12}$`)

// idStore maps short deterministic IDs back to their canonical URLs.
var idStore = NewLRUCache[string](idStoreMax, idStoreTTL)

// MintResultID derives a deterministic 14-character ID from url using
// SHA-256, stores the mapping, and returns the ID. The ID is prefixed with
// "r_" followed by the first 12 hex characters of the hash.
func MintResultID(url string) string {
	h := sha256.Sum256([]byte(url))
	hexStr := hex.EncodeToString(h[:])
	resultID := idPrefix + hexStr[:idHexLen]

	idStore.Set(resultID, url)

	return resultID
}

// LooksLikeResultID reports whether s matches the shape of an ID produced by
// MintResultID (the "r_" prefix followed by exactly 12 lowercase hex digits).
func LooksLikeResultID(s string) bool {
	return idPattern.MatchString(s)
}

// ResolveResultID returns the URL associated with resultID, or the zero value and
// false if the ID is unknown or has expired.
func ResolveResultID(resultID string) (string, bool) {
	return idStore.Get(resultID)
}

// ClearIDStore removes all stored ID mappings. Primarily useful in tests.
func ClearIDStore() {
	idStore.Clear()
}
