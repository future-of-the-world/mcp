// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package gitlab: request and response types for the GitLab MCP tools.
package gitlab

import (
	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// --- Request types ---

// GetMRDiscussionsRequest is the request for the get_mr_discussions tool.
type GetMRDiscussionsRequest struct {
	// URL is the full GitLab merge request URL, e.g.
	// "https://gitlab.example.com/group/project/-/merge_requests/42"
	URL     string `json:"url"                yaml:"url"`
	Page    int64  `json:"page,omitempty"     yaml:"page,omitempty"`
	PerPage int64  `json:"per_page,omitempty" yaml:"per_page,omitempty"`
}

// GetMRCommitsRequest is the request for the get_mr_commits tool.
type GetMRCommitsRequest struct {
	// URL is the full GitLab merge request URL.
	URL     string `json:"url"                yaml:"url"`
	Page    int64  `json:"page,omitempty"     yaml:"page,omitempty"`
	PerPage int64  `json:"per_page,omitempty" yaml:"per_page,omitempty"`
}

// --- Response types ---

// GetMRDiscussionsResponse wraps the list of discussions returned by
// the GitLab API.
type GetMRDiscussionsResponse struct {
	Discussions []*gitlab.Discussion `json:"discussions" yaml:"discussions"`
}

// GetMRCommitsResponse wraps the list of commits returned by the
// GitLab API.
type GetMRCommitsResponse struct {
	Commits []*gitlab.Commit `json:"commits" yaml:"commits"`
}
