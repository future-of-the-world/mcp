// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package tracker

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// TrackerFieldSchema describes the data type of a field value.
type TrackerFieldSchema struct {
	Type     string `json:"type,omitzero"     jsonschema:"Value type: string or array"`
	Items    string `json:"items,omitzero"    jsonschema:"Item type for array fields"`
	Required bool   `json:"required,omitzero" jsonschema:"Whether the field is required"`
}

// TrackerFieldOptionsProvider describes valid values for a field.
type TrackerFieldOptionsProvider struct {
	Type   string   `json:"type,omitzero"   jsonschema:"Options provider type"`
	Values []string `json:"values,omitzero" jsonschema:"Allowed values for the field"`
}

// TrackerFieldQueryProvider describes the query capability of a field.
type TrackerFieldQueryProvider struct {
	Type string `json:"type,omitzero" jsonschema:"Query provider type"`
}

// TrackerFieldCategory groups fields into categories.
type TrackerFieldCategory struct {
	Self    string `json:"self,omitzero"    jsonschema:"API URL of the category"`
	ID      string `json:"id,omitzero"      jsonschema:"Category ID"`
	Display string `json:"display,omitzero" jsonschema:"Category display name"`
}

// TrackerField represents a single issue field from the Tracker API.
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type TrackerField struct {
	Self            string                       `json:"self,omitzero"            jsonschema:"API URL of the field resource"`
	ID              string                       `json:"id"                       jsonschema:"Field ID used as filter key"`
	Name            string                       `json:"name"                     jsonschema:"Human-readable field name"`
	Description     string                       `json:"description,omitzero"     jsonschema:"Field description"`
	Version         int                          `json:"version,omitzero"         jsonschema:"Field version, incremented on changes"`
	Schema          TrackerFieldSchema           `json:"schema,omitzero"          jsonschema:"Data type info for the field value"`
	ReadOnly        bool                         `json:"readonly,omitzero"        jsonschema:"Whether the field value cannot be changed"`
	Options         bool                         `json:"options,omitzero"         jsonschema:"If true, values are not restricted to a fixed list"`
	Suggest         bool                         `json:"suggest,omitzero"         jsonschema:"Whether search suggestions are enabled"`
	SuggestProvider *TrackerFieldQueryProvider   `json:"suggestProvider,omitzero" jsonschema:"Suggest provider class info"`  //nolint:tagliatelle // Tracker API camelCase
	OptionsProvider *TrackerFieldOptionsProvider `json:"optionsProvider,omitzero" jsonschema:"Allowed values for the field"` //nolint:tagliatelle // Tracker API camelCase
	QueryProvider   *TrackerFieldQueryProvider   `json:"queryProvider,omitzero"   jsonschema:"Query language provider info"` //nolint:tagliatelle // Tracker API camelCase
	Order           int                          `json:"order,omitzero"           jsonschema:"Display order in the fields list"`
	Category        *TrackerFieldCategory        `json:"category,omitzero"        jsonschema:"Field category"`
}

// GetFieldsRequest is the request for getting available Tracker fields.
type GetFieldsRequest struct {
	// FieldID is an optional specific field ID to look up.
	// If empty, all fields are returned.
	FieldID string `json:"field_id,omitzero" jsonschema:"Optional field ID to fetch"`
}

// GetFieldsResponse is the response for getting available Tracker fields.
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type GetFieldsResponse struct {
	Fields []TrackerField `json:"fields,omitzero" jsonschema:"List of available issue fields with their IDs, types, and allowed values"`
}

// getFields retrieves available issue fields from the Tracker API.
func (c *client) getFields(ctx context.Context, req GetFieldsRequest) ([]TrackerField, error) {
	endpoint := "/v3/fields"
	if req.FieldID != "" {
		endpoint = "/v3/fields/" + url.PathEscape(req.FieldID)

		var field TrackerField

		_, err := c.trackerRequest(ctx, apiRequest{
			method: http.MethodGet,
			url:    c.buildTrackerURL(endpoint, url.Values(nil)),
			body:   any(nil),
			result: &field,
		})
		if err != nil {
			return nil, fmt.Errorf("get field %s: %w", req.FieldID, err)
		}

		return []TrackerField{field}, nil
	}

	var fields []TrackerField

	_, err := c.trackerRequest(ctx, apiRequest{
		method: http.MethodGet,
		url:    c.buildTrackerURL(endpoint, url.Values(nil)),
		body:   any(nil),
		result: &fields,
	})
	if err != nil {
		return nil, fmt.Errorf("get fields: %w", err)
	}

	return fields, nil
}
