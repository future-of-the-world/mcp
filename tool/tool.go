// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package tool defines the shared data types used by source-level tool
// declarations, the per-type Connect functions, and the source dispatcher
// middlewares. It exposes the Tool/Response structs (consumed uniformly
// by middlewares and the MCP server registration) and a functional
// Options type (used to thread a logger and tracer through the call chain
// from cmd/main.go down to each per-type Connect).
//
// Tool embeds *mcp.Tool so every field of the upstream mcp.Tool flows
// through the dispatcher untouched. The dispatcher only adds the Handler.
// Per-type Connect functions return a Tool whose embedded *mcp.Tool has
// Annotations set by the implementer; the dispatcher is a no-op transform.
package tool

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool is a single MCP tool description returned by a per-type Connect.
// It is consumed uniformly by the source dispatcher and middlewares: the
// dispatcher registers it with the MCP server via mcp.Server.AddTool, and
// middlewares may mutate the embedded *mcp.Tool before registration
// (renaming, filtering, classifying, etc.).
//
// The *mcp.Tool is embedded by value-of-pointer: the dispatcher and any
// middlewares observe the same *mcp.Tool instance the per-type package
// constructed. The Handler is the only field the tool/ package adds.
type Tool struct {
	*mcp.Tool

	Handler mcp.ToolHandler
}

// Response is the result of a per-type Connect. Every Connect
// implementation returns this concrete type so that middlewares can
// iterate Tools in registration order without knowing the source type.
type Response struct {
	Tools []Tool
}
