// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package tracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// HTTP query parameter names, header keys, and numeric base constants.
const (
	queryParamFields      = "fields"
	queryParamSlug        = "slug"
	queryParamIncludeSelf = "include_self"
	searchBodyFilter      = "filter"
	base10                = 10

	headerHost  = "Host"
	headerOrgID = "X-Org-Id"

	// JSON body field keys for issue create/update requests.
	bodyKeyAssignee    = "assignee"
	bodyKeyDeadline    = "deadline"
	bodyKeyDescription = "description"
	bodyKeyEnd         = "end"
	bodyKeyFollowers   = "followers"
	bodyKeyParent      = "parent"
	bodyKeyPriority    = "priority"
	bodyKeyQueue       = "queue"
	bodyKeyStart       = "start"
	bodyKeySummary     = "summary"
	bodyKeyTags        = "tags"
	bodyKeyText        = "text"
	bodyKeyType        = "type"
)

// client is the HTTP client for Yandex Tracker and Wiki APIs.
type client struct {
	httpClient *http.Client
	token      string
	orgID      string
	trackerURL *url.URL
	wikiURL    *url.URL
	cloudOrg   bool
}

// clientConfig holds configuration for creating a new API client.
type clientConfig struct {
	Token       string
	OrgID       string
	BaseURL     string
	WikiBaseURL string
	CloudOrg    bool
}

// apiRequest holds parameters for an HTTP API request.
type apiRequest struct {
	method string
	url    *url.URL
	body   any
	result any
}

// newClient creates a new API client using the provided credentials.
func newClient(cfg clientConfig) (*client, error) {
	token := cfg.Token
	if token == "" {
		return nil, errTokenEmpty
	}

	orgID := cfg.OrgID
	if orgID == "" {
		return nil, errOrgIDEmpty
	}

	trackerURL, err := parseBaseURL(cfg.BaseURL, defaultBaseURL)
	if err != nil {
		return nil, fmt.Errorf("tracker base URL: %w", err)
	}

	wikiURL, err := parseBaseURL(cfg.WikiBaseURL, defaultWikiBaseURL)
	if err != nil {
		return nil, fmt.Errorf("wiki base URL: %w", err)
	}

	return &client{
		httpClient: &http.Client{},
		token:      token,
		orgID:      orgID,
		trackerURL: trackerURL,
		wikiURL:    wikiURL,
		cloudOrg:   cfg.CloudOrg,
	}, nil
}

// parseBaseURL parses the given URL string, falling back to defaultIfEmpty.
func parseBaseURL(raw, defaultIfEmpty string) (*url.URL, error) {
	if raw == "" {
		raw = defaultIfEmpty
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse URL %q: %w", raw, err)
	}

	return parsed, nil
}

// --- Tracker API methods ---

// buildSearchBody constructs the JSON body for a search issues request.
func (*client) buildSearchBody(req *SearchIssuesRequest) map[string]any {
	body := make(map[string]any)

	switch {
	case req.Query != "":
		body["query"] = req.Query

	case len(req.Filter) > 0:
		body[searchBodyFilter] = req.Filter

		if req.Order != "" {
			body["order"] = req.Order
		}

	case req.Queue != "":
		body["queue"] = req.Queue

	case len(req.Keys) > 0:
		body["keys"] = req.Keys

	default:
		body[searchBodyFilter] = make(map[string]string)
	}

	return body
}

// searchIssues searches for issues using the Tracker search API.
func (c *client) searchIssues(
	ctx context.Context,
	req *SearchIssuesRequest,
) (*SearchIssuesResponse, error) {
	body := c.buildSearchBody(req)

	requestURL := c.buildTrackerURL("/v3/issues/_search", buildSearchQueryParams(searchQueryParams{
		fields:  req.Fields,
		perPage: req.PerPage,
		page:    req.Page,
	}))

	var issues []TrackerIssueShort

	respHeaders, err := c.trackerRequest(ctx, apiRequest{
		method: http.MethodPost,
		url:    requestURL,
		body:   body,
		result: &issues,
	})
	if err != nil {
		return nil, fmt.Errorf("search issues: %w", err)
	}

	response := &SearchIssuesResponse{
		Issues:     issues,
		TotalCount: 0,
		PerPage:    req.PerPage,
		Page:       req.Page,
	}

	// Extract total count from response header.
	if totalCount := respHeaders.Get("X-Total-Count"); totalCount != "" {
		count, atoiErr := strconv.Atoi(totalCount)
		if atoiErr == nil {
			response.TotalCount = count
		}
	}

	return response, nil
}

// getIssue retrieves a single issue by key or ID.
func (c *client) getIssue(
	ctx context.Context,
	req GetIssueRequest,
) (*TrackerIssue, error) {
	requestURL := c.buildTrackerURL(
		"/v3/issues/"+url.PathEscape(req.KeyOrID),
		buildSearchQueryParams(searchQueryParams{
			fields:  req.Fields,
			perPage: 0,
			page:    0,
		}),
	)

	var issue TrackerIssue

	_, err := c.trackerRequest(ctx, apiRequest{
		method: http.MethodGet,
		url:    requestURL,
		body:   any(nil),
		result: &issue,
	})
	if err != nil {
		return nil, fmt.Errorf("get issue %s: %w", req.KeyOrID, err)
	}

	return &issue, nil
}

// getLinks retrieves links for an issue.
func (c *client) getLinks(
	ctx context.Context,
	req GetLinksRequest,
) ([]TrackerLink, error) {
	requestURL := c.buildTrackerURL(
		"/v3/issues/"+url.PathEscape(req.KeyOrID)+"/links",
		url.Values(nil),
	)

	var links []TrackerLink

	_, err := c.trackerRequest(ctx, apiRequest{
		method: http.MethodGet,
		url:    requestURL,
		body:   any(nil),
		result: &links,
	})
	if err != nil {
		return nil, fmt.Errorf("get links for %s: %w", req.KeyOrID, err)
	}

	return links, nil
}

// getComments retrieves comments for an issue.
func (c *client) getComments(
	ctx context.Context,
	req GetCommentsRequest,
) ([]TrackerComment, error) {
	requestURL := c.buildTrackerURL(
		"/v3/issues/"+url.PathEscape(req.KeyOrID)+"/comments",
		url.Values(nil),
	)

	var comments []TrackerComment

	_, err := c.trackerRequest(ctx, apiRequest{
		method: http.MethodGet,
		url:    requestURL,
		body:   any(nil),
		result: &comments,
	})
	if err != nil {
		return nil, fmt.Errorf("get comments for %s: %w", req.KeyOrID, err)
	}

	return comments, nil
}

// --- Create / Update methods ---

// buildCreateIssueBody constructs the JSON body for creating an issue.
func buildCreateIssueBody(req *CreateIssueRequest) map[string]any {
	body := map[string]any{
		bodyKeySummary: req.Summary,
		bodyKeyQueue:   req.Queue,
	}

	if req.Description != "" {
		body[bodyKeyDescription] = req.Description
	}

	if req.Type != "" {
		body[bodyKeyType] = req.Type
	}

	if req.Priority != "" {
		body[bodyKeyPriority] = req.Priority
	}

	if req.Assignee != "" {
		body[bodyKeyAssignee] = req.Assignee
	}

	if req.Parent != "" {
		body[bodyKeyParent] = req.Parent
	}

	if len(req.Tags) > 0 {
		body[bodyKeyTags] = req.Tags
	}

	if req.Deadline != "" {
		body[bodyKeyDeadline] = req.Deadline
	}

	if req.Start != "" {
		body[bodyKeyStart] = req.Start
	}

	if req.End != "" {
		body[bodyKeyEnd] = req.End
	}

	if len(req.Followers) > 0 {
		body[bodyKeyFollowers] = req.Followers
	}

	return body
}

// createIssue creates a new issue in the specified queue.
func (c *client) createIssue(
	ctx context.Context,
	req *CreateIssueRequest,
) (*TrackerIssue, error) {
	body := buildCreateIssueBody(req)

	requestURL := c.buildTrackerURL("/v3/issues/", url.Values(nil))

	var issue TrackerIssue

	_, err := c.trackerRequest(ctx, apiRequest{
		method: http.MethodPost,
		url:    requestURL,
		body:   body,
		result: &issue,
	})
	if err != nil {
		return nil, fmt.Errorf("create issue in queue %s: %w", req.Queue, err)
	}

	return &issue, nil
}

// setBodyString adds a non-empty string field to the JSON body.
func setBodyString(body map[string]any, key, val string) {
	if val != "" {
		body[key] = val
	}
}

// buildUpdateIssueBody constructs the JSON body for updating an issue.
func buildUpdateIssueBody(req *UpdateIssueRequest) map[string]any {
	body := make(map[string]any)

	setBodyString(body, bodyKeySummary, req.Summary)
	setBodyString(body, bodyKeyDescription, req.Description)
	setBodyString(body, bodyKeyType, req.Type)
	setBodyString(body, bodyKeyPriority, req.Priority)
	setBodyString(body, bodyKeyAssignee, req.Assignee)
	setBodyString(body, bodyKeyParent, req.Parent)
	setBodyString(body, bodyKeyDeadline, req.Deadline)
	setBodyString(body, bodyKeyStart, req.Start)
	setBodyString(body, bodyKeyEnd, req.End)

	// Handle tags: direct set takes precedence over add/remove.
	switch {
	case len(req.Tags) > 0:
		body[bodyKeyTags] = req.Tags

	case len(req.TagsAdd) > 0 || len(req.TagsRemove) > 0:
		tagsOps := make(map[string]any)

		if len(req.TagsAdd) > 0 {
			tagsOps["add"] = req.TagsAdd
		}

		if len(req.TagsRemove) > 0 {
			tagsOps["remove"] = req.TagsRemove
		}

		body[bodyKeyTags] = tagsOps
	}

	if len(req.Followers) > 0 {
		body[bodyKeyFollowers] = req.Followers
	}

	return body
}

// buildUpdateIssueQueryParams constructs query params for the update issue request.
func buildUpdateIssueQueryParams(req *UpdateIssueRequest) url.Values {
	params := url.Values{}

	if req.Version > 0 {
		params.Set("version", strconv.Itoa(req.Version))
	}

	if len(params) == 0 {
		return nil
	}

	return params
}

// updateIssue updates an existing issue.
func (c *client) updateIssue(
	ctx context.Context,
	req *UpdateIssueRequest,
) (*TrackerIssue, error) {
	body := buildUpdateIssueBody(req)

	requestURL := c.buildTrackerURL(
		"/v3/issues/"+url.PathEscape(req.KeyOrID),
		buildUpdateIssueQueryParams(req),
	)

	var issue TrackerIssue

	_, err := c.trackerRequest(ctx, apiRequest{
		method: http.MethodPatch,
		url:    requestURL,
		body:   body,
		result: &issue,
	})
	if err != nil {
		return nil, fmt.Errorf("update issue %s: %w", req.KeyOrID, err)
	}

	return &issue, nil
}

// createComment creates a new comment on an issue.
func (c *client) createComment(
	ctx context.Context,
	req *CreateCommentRequest,
) (*TrackerComment, error) {
	body := map[string]any{
		bodyKeyText: req.Text,
	}

	requestURL := c.buildTrackerURL(
		"/v3/issues/"+url.PathEscape(req.KeyOrID)+"/comments",
		url.Values(nil),
	)

	var comment TrackerComment

	_, err := c.trackerRequest(ctx, apiRequest{
		method: http.MethodPost,
		url:    requestURL,
		body:   body,
		result: &comment,
	})
	if err != nil {
		return nil, fmt.Errorf("create comment on %s: %w", req.KeyOrID, err)
	}

	return &comment, nil
}

// updateComment updates an existing comment on an issue.
func (c *client) updateComment(
	ctx context.Context,
	req *UpdateCommentRequest,
) (*TrackerComment, error) {
	body := map[string]any{
		bodyKeyText: req.Text,
	}

	requestURL := c.buildTrackerURL(
		"/v3/issues/"+url.PathEscape(req.KeyOrID)+
			"/comments/"+url.PathEscape(req.CommentID),
		url.Values(nil),
	)

	var comment TrackerComment

	_, err := c.trackerRequest(ctx, apiRequest{
		method: http.MethodPatch,
		url:    requestURL,
		body:   body,
		result: &comment,
	})
	if err != nil {
		return nil, fmt.Errorf("update comment %s on %s: %w", req.CommentID, req.KeyOrID, err)
	}

	return &comment, nil
}

// --- Wiki API methods ---

// getWikiPage retrieves a wiki page by slug or ID.
func (c *client) getWikiPage(
	ctx context.Context,
	req GetWikiPageRequest,
) (*WikiPage, error) {
	var requestURL *url.URL

	wikiFields := "content,attributes"

	switch {
	case req.Slug != "":
		params := url.Values{}
		params.Set(queryParamSlug, req.Slug)
		params.Set(queryParamFields, wikiFields)

		requestURL = c.buildWikiURL("/v1/pages", params)

	case req.PageID > 0:
		params := url.Values{}
		params.Set(queryParamFields, wikiFields)

		requestURL = c.buildWikiURL(
			"/v1/pages/"+strconv.FormatInt(req.PageID, base10),
			params,
		)

	default:
		return nil, errWikiSlugOrIDEmpty
	}

	var page WikiPage

	_, err := c.wikiRequest(ctx, apiRequest{
		method: http.MethodGet,
		url:    requestURL,
		body:   any(nil),
		result: &page,
	})
	if err != nil {
		return nil, fmt.Errorf("get wiki page: %w", err)
	}

	return &page, nil
}

// getWikiSubpages retrieves subpages of a wiki page.
func (c *client) getWikiSubpages(
	ctx context.Context,
	req GetWikiSubpagesRequest,
) ([]WikiPage, error) {
	params := url.Values{}
	params.Set(queryParamSlug, req.Slug)
	params.Set(queryParamIncludeSelf, strconv.FormatBool(req.IncludeSelf))

	if req.PageSize > 0 {
		params.Set("page_size", strconv.Itoa(req.PageSize))
	}

	requestURL := c.buildWikiURL("/v1/pages/descendants", params)

	// Wiki returns results wrapped in {"results": [...]}
	var wrapper struct {
		Results []WikiPage `json:"results"`
	}

	_, err := c.wikiRequest(ctx, apiRequest{
		method: http.MethodGet,
		url:    requestURL,
		body:   any(nil),
		result: &wrapper,
	})
	if err != nil {
		return nil, fmt.Errorf("get wiki subpages: %w", err)
	}

	return wrapper.Results, nil
}

// --- URL building helpers ---

// buildTrackerURL constructs a full URL for a Tracker API endpoint.
func (c *client) buildTrackerURL(endpoint string, params url.Values) *url.URL {
	return buildURL(c.trackerURL, endpoint, params)
}

// buildWikiURL constructs a full URL for a Wiki API endpoint.
func (c *client) buildWikiURL(endpoint string, params url.Values) *url.URL {
	return buildURL(c.wikiURL, endpoint, params)
}

// buildURL joins the base URL with the endpoint path and attaches query params.
func buildURL(base *url.URL, endpoint string, params url.Values) *url.URL {
	joined, err := base.Parse(endpoint)
	if err != nil {
		// Fallback: should never happen with validated URLs.
		joined = &url.URL{
			Scheme:      base.Scheme,
			Opaque:      "",
			User:        (*url.Userinfo)(nil),
			Host:        base.Host,
			Path:        endpoint,
			RawPath:     "",
			ForceQuery:  false,
			RawQuery:    "",
			Fragment:    "",
			RawFragment: "",
			OmitHost:    false,
		}
	}

	if len(params) > 0 {
		joined.RawQuery = params.Encode()
	}

	return joined
}

// searchQueryParams holds optional parameters for search query strings.
type searchQueryParams struct {
	fields  string
	perPage int
	page    int
}

// buildSearchQueryParams constructs url.Values from search parameters.
func buildSearchQueryParams(params searchQueryParams) url.Values {
	result := url.Values{}

	if params.fields != "" {
		result.Set(queryParamFields, params.fields)
	}

	if params.perPage > 0 {
		result.Set("perPage", strconv.Itoa(params.perPage))
	}

	if params.page > 0 {
		result.Set("page", strconv.Itoa(params.page))
	}

	if len(result) == 0 {
		return nil
	}

	return result
}

// --- Generic request helpers ---

// trackerRequest makes an authenticated request to the Tracker API.
func (c *client) trackerRequest(ctx context.Context, req apiRequest) (http.Header, error) {
	return c.doRequest(ctx, req, c.setTrackerAuthHeaders)
}

// wikiRequest makes an authenticated request to the Wiki API.
func (c *client) wikiRequest(ctx context.Context, req apiRequest) (http.Header, error) {
	return c.doRequest(ctx, req, c.setWikiAuthHeaders)
}

// setTrackerAuthHeaders sets authentication headers for Tracker API requests.
func (c *client) setTrackerAuthHeaders(req *http.Request) {
	c.setAuthHeadersForHost(req, "api.tracker.yandex.net")
}

// setWikiAuthHeaders sets authentication headers for Wiki API requests.
func (c *client) setWikiAuthHeaders(req *http.Request) {
	c.setAuthHeadersForHost(req, "api.wiki.yandex.net")
}

// setAuthHeadersForHost sets authentication and host headers on the request.
func (c *client) setAuthHeadersForHost(req *http.Request, host string) {
	req.Header.Set(headerHost, host)
	req.Header.Set("Authorization", "OAuth "+c.token)

	if c.cloudOrg {
		req.Header.Set("X-Cloud-Org-Id", c.orgID)
	} else {
		req.Header.Set(headerOrgID, c.orgID)
	}
}

// handleResponse reads and processes an HTTP response body.
func handleResponse(resp *http.Response, result any) (http.Header, error) {
	//nolint:errcheck // body close error is not critical
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf(
			"api returned status %d: %s",
			resp.StatusCode,
			string(respBody),
		)
	}

	if result != nil && len(respBody) > 0 {
		err = json.Unmarshal(respBody, result)
		if err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
	}

	return resp.Header, nil
}

// doRequest makes an authenticated HTTP request and decodes the JSON response.
func (c *client) doRequest(
	ctx context.Context,
	req apiRequest,
	setAuth func(*http.Request),
) (http.Header, error) {
	var reqBody io.Reader

	if req.body != nil {
		jsonData, err := json.Marshal(req.body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}

		reqBody = bytes.NewBuffer(jsonData)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.method, req.url.String(), reqBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	setAuth(httpReq)

	if req.body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}

	return handleResponse(resp, req.result)
}

// listQueues lists all Yandex Tracker queues available to the
// authenticated user. It is a thin wrapper over the standard
// /queues endpoint, returning a flat list of TrackerQueue values.
func (c *client) listQueues(ctx context.Context) ([]TrackerQueue, error) {
	var queues []TrackerQueue

	_, err := c.trackerRequest(ctx, apiRequest{
		method: http.MethodGet,
		url:    c.buildTrackerURL("/queues", url.Values(nil)),
		body:   any(nil),
		result: &queues,
	})
	if err != nil {
		return nil, fmt.Errorf("list queues: %w", err)
	}

	return queues, nil
}
