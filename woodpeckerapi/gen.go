// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package woodpeckerapi provides primitives to interact with the openapi HTTP API.
//
// The vendored spec is the Woodpecker 3.15.0 OpenAPI document, normalized
// from upstream — see spec_fixes.md for the list of mechanical fixes
// applied to make oapi-codegen accept the file. The generated client
// exposes one Go method per documented endpoint; per-source Connect
// functions in package mcp/woodpecker consume it.
package woodpeckerapi

//go:generate go tool oapi-codegen -generate types,client -o api.gen.go -package woodpeckerapi api.swagger.json
