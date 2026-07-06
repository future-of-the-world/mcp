// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package source defines the top-level source configuration consumed by
// the MCP server and the dispatcher that wires per-type Connect
// implementations into the server.
//
// A Source describes a single named connection to an external system.
// Source files contain a top-level `sources:` map keyed by the
// user-chosen source name; for each key, the value is decoded into a
// Source and the map key is assigned to Source.Name by LoadSources.
//
// The dispatcher in dispatcher.go switches on Source.Type and, in this
// issue, returns an empty tool.Response for every known type. The
// per-type Connect implementations land in a follow-up issue.
package source

import (
	"bytes"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Source describes a single named connection to an external system.
//
// Name is populated by the loader from the surrounding `sources:` map
// key (e.g. `forgejo:`) — it is not itself a config field. The
// remaining fields map directly onto the YAML/JSON keys of the same
// name. Connect is a free-form map so each per-type implementation can
// decode its own connection schema without the source package having
// to know about it.
type Source struct {
	Name string `yaml:"-" json:"-"` // populated by the loader from the map key

	Type string `yaml:"type" json:"type"`

	Connect map[string]any `yaml:"connect,omitempty" json:"connect,omitempty"`

	Tools ToolsConfig `yaml:"tools" json:"tools"`
}

// ToolsConfig governs per-source tool-name and tool-set adjustments
// applied by the dispatcher middlewares. The fields are consumed by the
// middlewares added in a follow-up issue; this issue only decodes them
// so a later pass can mutate them.
//
// Remove and EnableOnly are mutually exclusive: setting both on the
// same source is a configuration error and LoadSources rejects it.
// The two fields have opposite intent (drop matching vs keep matching
// only) and combining them produces ambiguous behavior on tools that
// match only one of the two pattern sets.
type ToolsConfig struct {
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`

	Remove []string `yaml:"remove,omitempty" json:"remove,omitempty"`

	EnableOnly []string `yaml:"enable_only,omitempty" json:"enable_only,omitempty"`

	ReadOnly bool `yaml:"read_only,omitempty" json:"read_only,omitempty"`
}

// LoadSources decodes a YAML or JSON document and returns the entries
// under the top-level `sources:` map as a slice of Source values with
// each Source.Name populated from its map key.
//
// The document format is auto-detected from the first non-whitespace
// byte: a leading '{' or '[' is treated as JSON, anything else as
// YAML. Decoding errors are wrapped with a format-specific prefix so
// callers can tell which parser failed.
func LoadSources(data []byte) ([]Source, error) {
	sources := make(map[string]Source)

	doc := rawDocument{Sources: sources}

	var err error

	if isJSON(data) {
		err = json.Unmarshal(data, &doc)
		if err != nil {
			return nil, fmt.Errorf("decode sources json: %w", err)
		}
	} else {
		err = yaml.Unmarshal(data, &doc)
		if err != nil {
			return nil, fmt.Errorf("decode sources yaml: %w", err)
		}
	}

	out := make([]Source, 0, len(sources))

	for name := range doc.Sources {
		src := doc.Sources[name]

		if len(src.Tools.Remove) > 0 && len(src.Tools.EnableOnly) > 0 {
			return nil, fmt.Errorf(
				"source %q: tools.remove and tools.enable_only are mutually exclusive",
				name,
			)
		}

		src.Name = name

		out = append(out, src)
	}

	return out, nil
}

// rawDocument is the on-disk shape of a source config file. Only the
// `sources:` block is consumed in this issue; later issues extend the
// document with server identity, default middlewares, etc.
type rawDocument struct {
	Sources map[string]Source `yaml:"sources" json:"sources"`
}

// isJSON reports whether data begins with a JSON object or array
// opening bracket after whitespace. It is a best-effort format hint
// for LoadSources, not a strict validator.
func isJSON(data []byte) bool {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 {
		return false
	}

	return trimmed[0] == '{' || trimmed[0] == '['
}
