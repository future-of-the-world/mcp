// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package tracker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient creates a client pointed at the given test server URL.
func newTestClient(serverURL string) *client {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		panic("test server URL parse: " + err.Error())
	}

	return &client{
		httpClient: &http.Client{},
		token:      "test-token",
		orgID:      "test-org",
		trackerURL: parsed,
		wikiURL:    parsed,
		cloudOrg:   false,
	}
}

// --- SearchIssues ---

func TestClient_SearchIssues(t *testing.T) {
	t.Parallel()

	issues := []TrackerIssueShort{
		{
			Key:         "TREK-1",
			Summary:     "First issue",
			Description: "",
			Type:        TrackerStatus{Self: "", ID: "", Key: "task", Display: "Task"},
			Priority:    TrackerStatus{Self: "", ID: "", Key: "", Display: ""},
			Status:      TrackerStatus{Self: "", ID: "", Key: "open", Display: "Open"},
			Assignee:    nil,
			CreatedBy: TrackerUser{
				Self:        "",
				ID:          "",
				Display:     "",
				PassportUID: 0,
				CloudUID:    "",
				Login:       "",
			},
			Queue:      TrackerQueue{Self: "", ID: "", Key: "TREK", Display: "Trek"},
			Sprint:     nil,
			Tags:       nil,
			CreatedAt:  "",
			UpdatedAt:  "",
			ResolvedAt: "",
		},
		{
			Key:         "TREK-2",
			Summary:     "Second issue",
			Description: "",
			Type:        TrackerStatus{Self: "", ID: "", Key: "bug", Display: "Bug"},
			Priority:    TrackerStatus{Self: "", ID: "", Key: "", Display: ""},
			Status:      TrackerStatus{Self: "", ID: "", Key: "resolved", Display: "Resolved"},
			Assignee:    nil,
			CreatedBy: TrackerUser{
				Self:        "",
				ID:          "",
				Display:     "",
				PassportUID: 0,
				CloudUID:    "",
				Login:       "",
			},
			Queue:      TrackerQueue{Self: "", ID: "", Key: "TREK", Display: "Trek"},
			Sprint:     nil,
			Tags:       nil,
			CreatedAt:  "",
			UpdatedAt:  "",
			ResolvedAt: "",
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/issues/_search", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Total-Count", "42")
		w.Header().Set("Content-Type", "application/json")

		err := json.NewEncoder(w).Encode(issues)
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	resp, err := cli.searchIssues(t.Context(), &SearchIssuesRequest{
		Query:   "Assignee: user123",
		Filter:  map[string]string(nil),
		Queue:   "",
		Keys:    []string(nil),
		Order:   "",
		Fields:  "",
		PerPage: 50,
		Page:    1,
	})
	require.NoError(t, err)

	require.Len(t, resp.Issues, 2)
	assert.Equal(t, "TREK-1", resp.Issues[0].Key)
	assert.Equal(t, "First issue", resp.Issues[0].Summary)
	assert.Equal(t, "TREK-2", resp.Issues[1].Key)
	assert.Equal(t, 42, resp.TotalCount)
	assert.Equal(t, 50, resp.PerPage)
	assert.Equal(t, 1, resp.Page)
}

func TestClient_SearchIssues_APIError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/issues/_search", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)

		_, err := w.Write([]byte(`{"error": "unauthorized"}`))
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	resp, err := cli.searchIssues(t.Context(), &SearchIssuesRequest{
		Query:   "Assignee: user123",
		Filter:  map[string]string(nil),
		Queue:   "",
		Keys:    []string(nil),
		Order:   "",
		Fields:  "",
		PerPage: 0,
		Page:    0,
	})
	require.Error(t, err)
	require.Nil(t, resp)
	assert.Contains(t, err.Error(), "401")
}

// --- GetIssue ---

func TestClient_GetIssue(t *testing.T) {
	t.Parallel()

	issue := TrackerIssue{
		TrackerIssueShort: TrackerIssueShort{
			Key:         "TREK-123",
			Summary:     "Test issue",
			Description: "",
			Type:        TrackerStatus{Self: "", ID: "", Key: "task", Display: "Task"},
			Priority:    TrackerStatus{Self: "", ID: "", Key: "", Display: ""},
			Status: TrackerStatus{
				Self:    "",
				ID:      "",
				Key:     "in_progress",
				Display: "In Progress",
			},
			Assignee: &TrackerUser{
				Self:        "",
				ID:          "",
				Display:     "User One",
				PassportUID: 0,
				CloudUID:    "",
				Login:       "user1",
			},
			CreatedBy: TrackerUser{
				Self:        "",
				ID:          "",
				Display:     "",
				PassportUID: 0,
				CloudUID:    "",
				Login:       "",
			},
			Queue:      TrackerQueue{Self: "", ID: "", Key: "TREK", Display: "Trek"},
			Sprint:     []TrackerSprint(nil),
			Tags:       []string{"backend", "urgent"},
			CreatedAt:  "2025-01-15T10:30:00Z",
			UpdatedAt:  "2025-06-01T14:20:00Z",
			ResolvedAt: "",
		},
		ID:             "5f8a1b2c3d4e5f",
		Version:        7,
		Votes:          3,
		Favorite:       false,
		Aliases:        []string(nil),
		PreviousStatus: TrackerStatus{Self: "", ID: "", Key: "", Display: ""},
		UpdatedBy: TrackerUser{
			Self:        "",
			ID:          "",
			Display:     "",
			PassportUID: 0,
			CloudUID:    "",
			Login:       "",
		},
		Followers:     []TrackerUser(nil),
		LastCommentAt: "",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v3/issues/{key}", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "TREK-123", r.PathValue("key"))

		w.Header().Set("Content-Type", "application/json")

		err := json.NewEncoder(w).Encode(issue)
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	result, err := cli.getIssue(t.Context(), GetIssueRequest{
		KeyOrID: "TREK-123",
		Fields:  "",
	})
	require.NoError(t, err)

	require.NotNil(t, result)
	assert.Equal(t, "TREK-123", result.Key)
	assert.Equal(t, "Test issue", result.Summary)
	assert.Equal(t, "5f8a1b2c3d4e5f", result.ID)
	assert.Equal(t, 7, result.Version)
	assert.Equal(t, 3, result.Votes)
	assert.Equal(t, "task", result.Type.Key)
	assert.Equal(t, "in_progress", result.Status.Key)
	require.NotNil(t, result.Assignee)
	assert.Equal(t, "user1", result.Assignee.Login)
	assert.ElementsMatch(t, []string{"backend", "urgent"}, result.Tags)
}

// --- GetComments ---

func TestClient_GetComments(t *testing.T) {
	t.Parallel()

	comments := []TrackerComment{
		{
			Self:   "",
			ID:     1,
			LongID: "",
			Text:   "First comment",
			CreatedBy: TrackerUser{
				Self:        "",
				ID:          "",
				Display:     "Alice",
				PassportUID: 0,
				CloudUID:    "",
				Login:       "alice",
			},
			UpdatedBy: TrackerUser{
				Self:        "",
				ID:          "",
				Display:     "",
				PassportUID: 0,
				CloudUID:    "",
				Login:       "",
			},
			UpdatedAt: "2025-03-10T08:00:00Z",
			CreatedAt: "2025-03-10T08:00:00Z",
			Version:   1,
			Type:      "standard",
			Transport: "internal",
		},
		{
			Self:   "",
			ID:     2,
			LongID: "",
			Text:   "Second comment",
			CreatedBy: TrackerUser{
				Self:        "",
				ID:          "",
				Display:     "Bob",
				PassportUID: 0,
				CloudUID:    "",
				Login:       "bob",
			},
			UpdatedBy: TrackerUser{
				Self:        "",
				ID:          "",
				Display:     "",
				PassportUID: 0,
				CloudUID:    "",
				Login:       "",
			},
			UpdatedAt: "2025-03-11T09:30:00Z",
			CreatedAt: "2025-03-11T09:30:00Z",
			Version:   1,
			Type:      "standard",
			Transport: "internal",
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v3/issues/{key}/comments", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "TREK-99", r.PathValue("key"))

		w.Header().Set("Content-Type", "application/json")

		err := json.NewEncoder(w).Encode(comments)
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	result, err := cli.getComments(t.Context(), GetCommentsRequest{
		KeyOrID: "TREK-99",
	})
	require.NoError(t, err)

	require.Len(t, result, 2)
	assert.Equal(t, int64(1), result[0].ID)
	assert.Equal(t, "First comment", result[0].Text)
	assert.Equal(t, "alice", result[0].CreatedBy.Login)
	assert.Equal(t, int64(2), result[1].ID)
	assert.Equal(t, "Second comment", result[1].Text)
}

// --- GetLinks ---

func TestClient_GetLinks(t *testing.T) {
	t.Parallel()

	links := []TrackerLink{
		{
			Self:      "",
			ID:        101,
			Type:      TrackerLinkType{Self: "", ID: "relates", Inward: "in", Outward: "out"},
			Direction: "outward",
			Object: TrackerLinkedIssue{
				Self:    "",
				ID:      "issue-200",
				Key:     "TREK-200",
				Display: "Linked issue",
			},
			CreatedBy: TrackerUser{
				Self: "", ID: "", Display: "Alice",
				PassportUID: 0, CloudUID: "", Login: "alice",
			},
			UpdatedBy: TrackerUser{
				Self: "", ID: "", Display: "",
				PassportUID: 0, CloudUID: "", Login: "",
			},
			Assignee:  (*TrackerUser)(nil),
			Status:    (*TrackerStatus)(nil),
			CreatedAt: "2025-03-10T08:00:00Z",
			UpdatedAt: "2025-03-10T08:00:00Z",
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v3/issues/{key}/links", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "TREK-100", r.PathValue("key"))

		w.Header().Set("Content-Type", "application/json")

		err := json.NewEncoder(w).Encode(links)
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	result, err := cli.getLinks(t.Context(), GetLinksRequest{KeyOrID: "TREK-100"})
	require.NoError(t, err)

	require.Len(t, result, 1)
	assert.Equal(t, int64(101), result[0].ID)
	assert.Equal(t, "relates", result[0].Type.ID)
	assert.Equal(t, "outward", result[0].Direction)
	assert.Equal(t, "TREK-200", result[0].Object.Key)
}

func TestClient_GetLinks_APIError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v3/issues/{key}/links", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)

		//nolint:errcheck // hard-coded error body; write error is not actionable
		_, _ = w.Write([]byte(`{"errors":["Issue not found"]}`))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	_, err := cli.getLinks(t.Context(), GetLinksRequest{KeyOrID: "TREK-999"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "get links for TREK-999")
}

// --- ListQueues ---

func TestClient_ListQueues(t *testing.T) {
	t.Parallel()

	queues := []TrackerQueue{
		{Self: "", ID: "1", Key: "TREK", Display: "Trek"},
		{Self: "", ID: "2", Key: "DEV", Display: "Dev"},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /queues", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		err := json.NewEncoder(w).Encode(queues)
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	result, err := cli.listQueues(t.Context())
	require.NoError(t, err)

	require.Len(t, result, 2)
	assert.Equal(t, "TREK", result[0].Key)
	assert.Equal(t, "DEV", result[1].Key)
}

func TestClient_ListQueues_APIError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /queues", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)

		//nolint:errcheck // hard-coded error body; write error is not actionable
		_, _ = w.Write([]byte(`{"errors":["Forbidden"]}`))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	_, err := cli.listQueues(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "list queues")
}

// --- GetWikiPage ---

func TestClient_GetWikiPage_BySlug(t *testing.T) {
	t.Parallel()

	page := WikiPage{
		ID:        100,
		Slug:      "users/test/readme",
		Title:     "Test Page",
		Content:   "= Hello World =",
		CreatedAt: "2025-01-01T00:00:00Z",
		UpdatedAt: "2025-06-15T12:00:00Z",
		CreatedBy: (*TrackerUser)(nil),
		UpdatedBy: (*TrackerUser)(nil),
		Parent:    (*WikiPageParent)(nil),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/pages", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "users/test/readme", r.URL.Query().Get("slug"))
		assert.Equal(t, "content,attributes", r.URL.Query().Get("fields"))

		w.Header().Set("Content-Type", "application/json")

		err := json.NewEncoder(w).Encode(page)
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	result, err := cli.getWikiPage(t.Context(), GetWikiPageRequest{
		Slug:   "users/test/readme",
		PageID: 0,
	})
	require.NoError(t, err)

	require.NotNil(t, result)
	assert.Equal(t, int64(100), result.ID)
	assert.Equal(t, "users/test/readme", result.Slug)
	assert.Equal(t, "Test Page", result.Title)
	assert.Equal(t, "= Hello World =", result.Content)
}

func TestClient_GetWikiPage_ByPageID(t *testing.T) {
	t.Parallel()

	page := WikiPage{
		ID:        200,
		Slug:      "docs/api",
		Title:     "API Docs",
		Content:   "Some API documentation",
		CreatedAt: "2025-02-01T00:00:00Z",
		UpdatedAt: "2025-07-20T10:00:00Z",
		CreatedBy: (*TrackerUser)(nil),
		UpdatedBy: (*TrackerUser)(nil),
		Parent:    (*WikiPageParent)(nil),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/pages/{id}", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "200", r.PathValue("id"))
		assert.Equal(t, "content,attributes", r.URL.Query().Get("fields"))

		w.Header().Set("Content-Type", "application/json")

		err := json.NewEncoder(w).Encode(page)
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	result, err := cli.getWikiPage(t.Context(), GetWikiPageRequest{
		Slug:   "",
		PageID: 200,
	})
	require.NoError(t, err)

	require.NotNil(t, result)
	assert.Equal(t, int64(200), result.ID)
	assert.Equal(t, "docs/api", result.Slug)
	assert.Equal(t, "API Docs", result.Title)
}

// --- GetWikiSubpages ---

func TestClient_GetWikiSubpages(t *testing.T) {
	t.Parallel()

	pages := []WikiPage{
		{
			ID:        10,
			Slug:      "docs/sub1",
			Title:     "Subpage 1",
			Content:   "",
			CreatedAt: "",
			UpdatedAt: "",
			CreatedBy: (*TrackerUser)(nil),
			UpdatedBy: (*TrackerUser)(nil),
			Parent:    (*WikiPageParent)(nil),
		},
		{
			ID:        11,
			Slug:      "docs/sub2",
			Title:     "Subpage 2",
			Content:   "",
			CreatedAt: "",
			UpdatedAt: "",
			CreatedBy: (*TrackerUser)(nil),
			UpdatedBy: (*TrackerUser)(nil),
			Parent:    (*WikiPageParent)(nil),
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/pages/descendants", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "docs/guide", r.URL.Query().Get("slug"))
		assert.Equal(t, "true", r.URL.Query().Get("include_self"))

		wrapper := map[string]any{
			"results": pages,
		}

		w.Header().Set("Content-Type", "application/json")

		err := json.NewEncoder(w).Encode(wrapper)
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	result, err := cli.getWikiSubpages(t.Context(), GetWikiSubpagesRequest{
		Slug:        "docs/guide",
		IncludeSelf: true,
		PageSize:    50,
	})
	require.NoError(t, err)

	require.Len(t, result, 2)
	assert.Equal(t, int64(10), result[0].ID)
	assert.Equal(t, "Subpage 1", result[0].Title)
	assert.Equal(t, int64(11), result[1].ID)
	assert.Equal(t, "Subpage 2", result[1].Title)
}

// --- Auth Headers ---

func TestClient_AuthHeaders(t *testing.T) {
	t.Parallel()

	var receivedHeaders http.Header

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/issues/_search", func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()

		w.Header().Set("Content-Type", "application/json")

		_, err := w.Write([]byte("[]"))
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	_, err := cli.searchIssues(t.Context(), &SearchIssuesRequest{
		Query:   "test",
		Filter:  map[string]string(nil),
		Queue:   "",
		Keys:    []string(nil),
		Order:   "",
		Fields:  "",
		PerPage: 0,
		Page:    0,
	})
	require.NoError(t, err)

	assert.Equal(t, "OAuth test-token", receivedHeaders.Get("Authorization"))
	assert.Equal(t, "test-org", receivedHeaders.Get("X-Org-Id"))
	assert.Empty(t, receivedHeaders.Get("X-Cloud-Org-Id"))
}

func TestClient_CloudOrgHeaders(t *testing.T) {
	t.Parallel()

	var receivedHeaders http.Header

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/issues/_search", func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()

		w.Header().Set("Content-Type", "application/json")

		_, err := w.Write([]byte("[]"))
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	cli.cloudOrg = true

	_, err := cli.searchIssues(t.Context(), &SearchIssuesRequest{
		Query:   "test",
		Filter:  map[string]string(nil),
		Queue:   "",
		Keys:    []string(nil),
		Order:   "",
		Fields:  "",
		PerPage: 0,
		Page:    0,
	})
	require.NoError(t, err)

	assert.Equal(t, "OAuth test-token", receivedHeaders.Get("Authorization"))
	assert.Equal(t, "test-org", receivedHeaders.Get("X-Cloud-Org-Id"))
	assert.Empty(t, receivedHeaders.Get("X-Org-Id"))
}

// --- newClient ---

func TestNewClient_MissingToken(t *testing.T) {
	t.Parallel()

	_, err := newClient(clientConfig{
		Token:       "",
		OrgID:       "some-org-id",
		BaseURL:     "",
		WikiBaseURL: "",
		CloudOrg:    false,
	})
	require.ErrorIs(t, err, errTokenEmpty)
}

func TestNewClient_MissingOrgID(t *testing.T) {
	_, err := newClient(clientConfig{
		Token:       "some-token",
		OrgID:       "",
		BaseURL:     "",
		WikiBaseURL: "",
		CloudOrg:    false,
	})
	require.ErrorIs(t, err, errOrgIDEmpty)
}

func TestNewClient_DefaultURLs(t *testing.T) {
	cli, err := newClient(clientConfig{
		Token:       "tok",
		OrgID:       "org123",
		BaseURL:     "",
		WikiBaseURL: "",
		CloudOrg:    false,
	})
	require.NoError(t, err)

	assert.Equal(t, defaultBaseURL, cli.trackerURL.String())
	assert.Equal(t, defaultWikiBaseURL, cli.wikiURL.String())
}

// --- GetFields ---

func TestClient_GetFields_All(t *testing.T) {
	t.Parallel()

	fields := []TrackerField{
		{
			Self:        "",
			ID:          "assignee",
			Name:        "Assignee",
			Description: "User assigned to the issue",
			Version:     0,
			Schema: TrackerFieldSchema{
				Type: "string", Items: "", Required: false,
			},
			ReadOnly:        false,
			Options:         true,
			Suggest:         true,
			SuggestProvider: (*TrackerFieldQueryProvider)(nil),
			OptionsProvider: (*TrackerFieldOptionsProvider)(nil),
			QueryProvider:   (*TrackerFieldQueryProvider)(nil),
			Order:           0,
			Category:        &TrackerFieldCategory{Self: "", ID: "cat1", Display: "System"},
		},
		{
			Self:        "",
			ID:          "status",
			Name:        "Status",
			Description: "Issue status",
			Version:     0,
			Schema: TrackerFieldSchema{
				Type: "string", Items: "", Required: true,
			},
			ReadOnly:        false,
			Options:         false,
			Suggest:         false,
			SuggestProvider: (*TrackerFieldQueryProvider)(nil),
			QueryProvider:   (*TrackerFieldQueryProvider)(nil),
			Order:           0,
			OptionsProvider: &TrackerFieldOptionsProvider{
				Type:   "FixedListOptionsProvider",
				Values: []string{"open", "in_progress", "resolved", "closed"},
			},
			Category: &TrackerFieldCategory{Self: "", ID: "cat1", Display: "System"},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v3/fields", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)

		w.Header().Set("Content-Type", "application/json")

		err := json.NewEncoder(w).Encode(fields)
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	result, err := cli.getFields(t.Context(), GetFieldsRequest{FieldID: ""})
	require.NoError(t, err)
	require.Len(t, result, 2)

	assert.Equal(t, "assignee", result[0].ID)
	assert.Equal(t, "Assignee", result[0].Name)
	assert.Equal(t, "status", result[1].ID)
	assert.Equal(t, "Status", result[1].Name)
	assert.True(t, result[0].Options)
	assert.False(t, result[1].Options)
	require.NotNil(t, result[1].OptionsProvider)
	assert.Equal(t,
		[]string{"open", "in_progress", "resolved", "closed"},
		result[1].OptionsProvider.Values,
	)
}

func TestClient_GetFields_Single(t *testing.T) {
	t.Parallel()

	field := TrackerField{
		Self:        "",
		ID:          "priority",
		Name:        "Priority",
		Description: "Issue priority",
		Version:     0,
		Schema: TrackerFieldSchema{
			Type: "string", Items: "", Required: false,
		},
		ReadOnly:        false,
		Options:         false,
		Suggest:         false,
		SuggestProvider: (*TrackerFieldQueryProvider)(nil),
		QueryProvider:   (*TrackerFieldQueryProvider)(nil),
		Order:           0,
		Category:        (*TrackerFieldCategory)(nil),
		OptionsProvider: &TrackerFieldOptionsProvider{
			Type:   "FixedListOptionsProvider",
			Values: []string{"critical", "high", "normal", "low"},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v3/fields/{id}", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "priority", r.PathValue("id"))

		w.Header().Set("Content-Type", "application/json")

		err := json.NewEncoder(w).Encode(field)
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	result, err := cli.getFields(t.Context(), GetFieldsRequest{FieldID: "priority"})
	require.NoError(t, err)
	require.Len(t, result, 1)

	assert.Equal(t, "priority", result[0].ID)
	assert.Equal(t, "Priority", result[0].Name)
	require.NotNil(t, result[0].OptionsProvider)
	assert.Equal(t, []string{"critical", "high", "normal", "low"}, result[0].OptionsProvider.Values)
}

func TestClient_GetFields_APIError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v3/fields", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)

		_, err := w.Write([]byte(`{"error": "not found"}`))
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	result, err := cli.getFields(t.Context(), GetFieldsRequest{FieldID: ""})
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "404")
}
