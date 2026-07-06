// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package tracker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test helpers for create/update ---

// newTestTrackerUser creates a TrackerUser for tests with all fields filled.
func newTestTrackerUser(uid, login string) TrackerUser {
	return TrackerUser{
		Self:        "",
		ID:          uid,
		Display:     "",
		PassportUID: 0,
		CloudUID:    "",
		Login:       login,
	}
}

// newTestTrackerStatus creates a TrackerStatus for tests with all fields filled.
func newTestTrackerStatus(key string) TrackerStatus {
	return TrackerStatus{Self: "", ID: "", Key: key, Display: ""}
}

// --- buildCreateIssueBody ---

func TestBuildCreateIssueBody_Minimal(t *testing.T) {
	t.Parallel()

	body := buildCreateIssueBody(&CreateIssueRequest{
		Summary:     "Test issue",
		Queue:       "TREK",
		Description: "",
		Type:        "",
		Priority:    "",
		Assignee:    "",
		Parent:      "",
		Tags:        []string(nil),
		Deadline:    "",
		Start:       "",
		End:         "",
		Followers:   []string(nil),
	})

	assert.Equal(t, "Test issue", body[bodyKeySummary])
	assert.Equal(t, "TREK", body[bodyKeyQueue])

	_, hasDesc := body[bodyKeyDescription]
	assert.False(t, hasDesc)
}

func TestBuildCreateIssueBody_AllFields(t *testing.T) {
	t.Parallel()

	body := buildCreateIssueBody(&CreateIssueRequest{
		Summary:     "Full issue",
		Queue:       "TREK",
		Description: "Detailed description",
		Type:        "bug",
		Priority:    "critical",
		Assignee:    "user1",
		Parent:      "TREK-100",
		Tags:        []string{"backend", "urgent"},
		Deadline:    "2025-12-31",
		Start:       "2025-01-01",
		End:         "2025-12-31",
		Followers:   []string{"user2", "user3"},
	})

	assert.Equal(t, "Full issue", body[bodyKeySummary])
	assert.Equal(t, "TREK", body[bodyKeyQueue])
	assert.Equal(t, "Detailed description", body[bodyKeyDescription])
	assert.Equal(t, "bug", body[bodyKeyType])
	assert.Equal(t, "critical", body[bodyKeyPriority])
	assert.Equal(t, "user1", body[bodyKeyAssignee])
	assert.Equal(t, "TREK-100", body[bodyKeyParent])
	assert.Equal(t, []string{"backend", "urgent"}, body[bodyKeyTags])
	assert.Equal(t, "2025-12-31", body[bodyKeyDeadline])
	assert.Equal(t, "2025-01-01", body[bodyKeyStart])
	assert.Equal(t, "2025-12-31", body[bodyKeyEnd])
	assert.Equal(t, []string{"user2", "user3"}, body[bodyKeyFollowers])
}

// --- buildUpdateIssueBody ---

func TestBuildUpdateIssueBody_Empty(t *testing.T) {
	t.Parallel()

	body := buildUpdateIssueBody(&UpdateIssueRequest{
		KeyOrID:     "TREK-1",
		Summary:     "",
		Description: "",
		Type:        "",
		Priority:    "",
		Assignee:    "",
		Parent:      "",
		Tags:        []string(nil),
		TagsAdd:     []string(nil),
		TagsRemove:  []string(nil),
		Deadline:    "",
		Start:       "",
		End:         "",
		Followers:   []string(nil),
		Version:     0,
	})

	assert.Empty(t, body)
}

func TestBuildUpdateIssueBody_DirectTags(t *testing.T) {
	t.Parallel()

	body := buildUpdateIssueBody(&UpdateIssueRequest{
		KeyOrID:     "TREK-1",
		Summary:     "",
		Description: "",
		Type:        "",
		Priority:    "",
		Assignee:    "",
		Parent:      "",
		Tags:        []string{"new-tag"},
		TagsAdd:     []string(nil),
		TagsRemove:  []string(nil),
		Deadline:    "",
		Start:       "",
		End:         "",
		Followers:   []string(nil),
		Version:     0,
	})

	assert.Equal(t, []string{"new-tag"}, body[bodyKeyTags])
}

func TestBuildUpdateIssueBody_TagsAddRemove(t *testing.T) {
	t.Parallel()

	body := buildUpdateIssueBody(&UpdateIssueRequest{
		KeyOrID:     "TREK-1",
		Summary:     "",
		Description: "",
		Type:        "",
		Priority:    "",
		Assignee:    "",
		Parent:      "",
		Tags:        []string(nil),
		TagsAdd:     []string{"tag1"},
		TagsRemove:  []string{"tag2"},
		Deadline:    "",
		Start:       "",
		End:         "",
		Followers:   []string(nil),
		Version:     0,
	})

	tagsOps, ok := body[bodyKeyTags].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []string{"tag1"}, tagsOps["add"])
	assert.Equal(t, []string{"tag2"}, tagsOps["remove"])
}

func TestBuildUpdateIssueBody_TagsAddOnly(t *testing.T) {
	t.Parallel()

	body := buildUpdateIssueBody(&UpdateIssueRequest{
		KeyOrID:     "TREK-1",
		Summary:     "",
		Description: "",
		Type:        "",
		Priority:    "",
		Assignee:    "",
		Parent:      "",
		Tags:        []string(nil),
		TagsAdd:     []string{"tag1"},
		TagsRemove:  []string(nil),
		Deadline:    "",
		Start:       "",
		End:         "",
		Followers:   []string(nil),
		Version:     0,
	})

	tagsOps, ok := body[bodyKeyTags].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []string{"tag1"}, tagsOps["add"])

	_, hasRemove := tagsOps["remove"]
	assert.False(t, hasRemove)
}

func TestBuildUpdateIssueBody_DirectTagsOverridesAddRemove(t *testing.T) {
	t.Parallel()

	body := buildUpdateIssueBody(&UpdateIssueRequest{
		KeyOrID:     "TREK-1",
		Summary:     "",
		Description: "",
		Type:        "",
		Priority:    "",
		Assignee:    "",
		Parent:      "",
		Tags:        []string{"direct"},
		TagsAdd:     []string{"ignored-add"},
		TagsRemove:  []string{"ignored-remove"},
		Deadline:    "",
		Start:       "",
		End:         "",
		Followers:   []string(nil),
		Version:     0,
	})

	// Direct tags should take precedence.
	assert.Equal(t, []string{"direct"}, body[bodyKeyTags])
}

// --- buildUpdateIssueQueryParams ---

func TestBuildUpdateIssueQueryParams_NoVersion(t *testing.T) {
	t.Parallel()

	params := buildUpdateIssueQueryParams(&UpdateIssueRequest{
		KeyOrID:     "TREK-1",
		Summary:     "",
		Description: "",
		Type:        "",
		Priority:    "",
		Assignee:    "",
		Parent:      "",
		Tags:        []string(nil),
		TagsAdd:     []string(nil),
		TagsRemove:  []string(nil),
		Deadline:    "",
		Start:       "",
		End:         "",
		Followers:   []string(nil),
		Version:     0,
	})

	assert.Nil(t, params)
}

func TestBuildUpdateIssueQueryParams_WithVersion(t *testing.T) {
	t.Parallel()

	params := buildUpdateIssueQueryParams(&UpdateIssueRequest{
		KeyOrID:     "TREK-1",
		Summary:     "",
		Description: "",
		Type:        "",
		Priority:    "",
		Assignee:    "",
		Parent:      "",
		Tags:        []string(nil),
		TagsAdd:     []string(nil),
		TagsRemove:  []string(nil),
		Deadline:    "",
		Start:       "",
		End:         "",
		Followers:   []string(nil),
		Version:     42,
	})

	require.NotNil(t, params)
	assert.Equal(t, "42", params.Get("version"))
}

// --- createIssue ---

func TestClient_CreateIssue(t *testing.T) {
	t.Parallel()

	createdIssue := TrackerIssue{
		TrackerIssueShort: TrackerIssueShort{
			Key:         "TREK-999",
			Summary:     "New bug",
			Description: "Bug description",
			Type:        newTestTrackerStatus("bug"),
			Priority:    newTestTrackerStatus("critical"),
			Status:      newTestTrackerStatus("open"),
			Assignee: &TrackerUser{
				Self:        "",
				ID:          "12345",
				Display:     "User One",
				PassportUID: 0,
				CloudUID:    "",
				Login:       "user1",
			},
			CreatedBy:  newTestTrackerUser("12345", "user1"),
			Queue:      TrackerQueue{Self: "", ID: "", Key: "TREK", Display: "Trek"},
			Sprint:     []TrackerSprint(nil),
			Tags:       []string{"backend"},
			CreatedAt:  "2025-06-01T10:00:00Z",
			UpdatedAt:  "2025-06-01T10:00:00Z",
			ResolvedAt: "",
		},
		ID:             "abc123def",
		Version:        1,
		Votes:          0,
		Favorite:       false,
		Aliases:        []string(nil),
		PreviousStatus: TrackerStatus{Self: "", ID: "", Key: "", Display: ""},
		UpdatedBy:      newTestTrackerUser("12345", "user1"),
		Followers:      []TrackerUser(nil),
		LastCommentAt:  "",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/issues/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var body map[string]any

		err := json.NewDecoder(r.Body).Decode(&body)
		assert.NoError(t, err)

		assert.Equal(t, "New bug", body[bodyKeySummary])
		assert.Equal(t, "TREK", body[bodyKeyQueue])
		assert.Equal(t, "bug", body[bodyKeyType])
		assert.Equal(t, "critical", body[bodyKeyPriority])
		assert.Equal(t, "user1", body[bodyKeyAssignee])

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)

		err = json.NewEncoder(w).Encode(createdIssue)
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	issue, err := cli.createIssue(t.Context(), &CreateIssueRequest{
		Summary:     "New bug",
		Queue:       "TREK",
		Description: "",
		Type:        "bug",
		Priority:    "critical",
		Assignee:    "user1",
		Parent:      "",
		Tags:        []string(nil),
		Deadline:    "",
		Start:       "",
		End:         "",
		Followers:   []string(nil),
	})
	require.NoError(t, err)
	require.NotNil(t, issue)

	assert.Equal(t, "TREK-999", issue.Key)
	assert.Equal(t, "New bug", issue.Summary)
	assert.Equal(t, "abc123def", issue.ID)
	assert.Equal(t, 1, issue.Version)
}

func TestClient_CreateIssue_APIError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/issues/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)

		_, err := w.Write([]byte(`{"error": "bad request"}`))
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	issue, err := cli.createIssue(t.Context(), &CreateIssueRequest{
		Summary:     "Test",
		Queue:       "INVALID",
		Description: "",
		Type:        "",
		Priority:    "",
		Assignee:    "",
		Parent:      "",
		Tags:        []string(nil),
		Deadline:    "",
		Start:       "",
		End:         "",
		Followers:   []string(nil),
	})
	require.Error(t, err)
	assert.Nil(t, issue)
	assert.Contains(t, err.Error(), "400")
}

// --- updateIssue ---

func TestClient_UpdateIssue(t *testing.T) {
	t.Parallel()

	updatedIssue := TrackerIssue{
		TrackerIssueShort: TrackerIssueShort{
			Key:         "TREK-123",
			Summary:     "Updated summary",
			Description: "Updated description",
			Type:        newTestTrackerStatus("task"),
			Priority:    newTestTrackerStatus("normal"),
			Status: TrackerStatus{
				Self:    "",
				ID:      "",
				Key:     "in_progress",
				Display: "In Progress",
			},
			Assignee: &TrackerUser{
				Self:        "",
				ID:          "12345",
				Display:     "User One",
				PassportUID: 0,
				CloudUID:    "",
				Login:       "user1",
			},
			CreatedBy:  newTestTrackerUser("", ""),
			Queue:      TrackerQueue{Self: "", ID: "", Key: "TREK", Display: "Trek"},
			Sprint:     []TrackerSprint(nil),
			Tags:       []string{"updated-tag"},
			CreatedAt:  "2025-01-15T10:30:00Z",
			UpdatedAt:  "2025-06-05T12:00:00Z",
			ResolvedAt: "",
		},
		ID:             "5f8a1b2c3d4e5f",
		Version:        8,
		Votes:          3,
		Favorite:       false,
		Aliases:        []string(nil),
		PreviousStatus: TrackerStatus{Self: "", ID: "", Key: "", Display: ""},
		UpdatedBy:      newTestTrackerUser("12345", "user1"),
		Followers:      []TrackerUser(nil),
		LastCommentAt:  "",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /v3/issues/{key}", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "TREK-123", r.PathValue("key"))
		assert.Equal(t, "7", r.URL.Query().Get("version"))

		var body map[string]any

		err := json.NewDecoder(r.Body).Decode(&body)
		assert.NoError(t, err)

		assert.Equal(t, "Updated summary", body[bodyKeySummary])

		w.Header().Set("Content-Type", "application/json")

		err = json.NewEncoder(w).Encode(updatedIssue)
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	issue, err := cli.updateIssue(t.Context(), &UpdateIssueRequest{
		KeyOrID:     "TREK-123",
		Summary:     "Updated summary",
		Description: "",
		Type:        "",
		Priority:    "",
		Assignee:    "",
		Parent:      "",
		Tags:        []string(nil),
		TagsAdd:     []string(nil),
		TagsRemove:  []string(nil),
		Deadline:    "",
		Start:       "",
		End:         "",
		Followers:   []string(nil),
		Version:     7,
	})
	require.NoError(t, err)
	require.NotNil(t, issue)

	assert.Equal(t, "TREK-123", issue.Key)
	assert.Equal(t, "Updated summary", issue.Summary)
	assert.Equal(t, 8, issue.Version)
}

func TestClient_UpdateIssue_EmptyResponse(t *testing.T) {
	t.Parallel()

	// PATCH /v3/issues/{key} returns a single TrackerIssue object; an empty
	// array body must surface as a decode error rather than a phantom issue.
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /v3/issues/{key}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		err := json.NewEncoder(w).Encode([]TrackerIssue{})
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	issue, err := cli.updateIssue(t.Context(), &UpdateIssueRequest{
		KeyOrID:     "TREK-123",
		Summary:     "Update",
		Description: "",
		Type:        "",
		Priority:    "",
		Assignee:    "",
		Parent:      "",
		Tags:        []string(nil),
		TagsAdd:     []string(nil),
		TagsRemove:  []string(nil),
		Deadline:    "",
		Start:       "",
		End:         "",
		Followers:   []string(nil),
		Version:     0,
	})
	require.Error(t, err)
	assert.Nil(t, issue)
	assert.Contains(t, err.Error(), "decode response")
}

func TestClient_UpdateIssue_APIError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /v3/issues/{key}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)

		_, err := w.Write([]byte(`{"error": "not found"}`))
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	issue, err := cli.updateIssue(t.Context(), &UpdateIssueRequest{
		KeyOrID:     "INVALID-999",
		Summary:     "Update",
		Description: "",
		Type:        "",
		Priority:    "",
		Assignee:    "",
		Parent:      "",
		Tags:        []string(nil),
		TagsAdd:     []string(nil),
		TagsRemove:  []string(nil),
		Deadline:    "",
		Start:       "",
		End:         "",
		Followers:   []string(nil),
		Version:     0,
	})
	require.Error(t, err)
	assert.Nil(t, issue)
	assert.Contains(t, err.Error(), "404")
}

// --- createComment ---

func TestClient_CreateComment(t *testing.T) {
	t.Parallel()

	createdComment := TrackerComment{
		Self:   "https://api.tracker.yandex.net/v3/issues/TREK-123/comments/626",
		ID:     626,
		LongID: "5fa15a24ac894475abc",
		Text:   "This is a new comment",
		CreatedBy: TrackerUser{
			Self:        "",
			ID:          "11",
			Display:     "",
			PassportUID: 0,
			CloudUID:    "",
			Login:       "user1",
		},
		UpdatedBy: TrackerUser{
			Self:        "",
			ID:          "11",
			Display:     "",
			PassportUID: 0,
			CloudUID:    "",
			Login:       "user1",
		},
		CreatedAt: "2025-06-01T10:00:00.000+0000",
		UpdatedAt: "2025-06-01T10:00:00.000+0000",
		Version:   1,
		Type:      "standard",
		Transport: "internal",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/issues/{key}/comments", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "TREK-123", r.PathValue("key"))
		assert.Equal(t, "POST", r.Method)

		var body map[string]any

		err := json.NewDecoder(r.Body).Decode(&body)
		assert.NoError(t, err)

		assert.Equal(t, "This is a new comment", body[bodyKeyText])

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)

		err = json.NewEncoder(w).Encode(createdComment)
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	comment, err := cli.createComment(t.Context(), &CreateCommentRequest{
		KeyOrID: "TREK-123",
		Text:    "This is a new comment",
	})
	require.NoError(t, err)
	require.NotNil(t, comment)

	assert.Equal(t, int64(626), comment.ID)
	assert.Equal(t, "This is a new comment", comment.Text)
	assert.Equal(t, "user1", comment.CreatedBy.Login)
	assert.Equal(t, 1, comment.Version)
	assert.Equal(t, "standard", comment.Type)
	assert.Equal(t, "internal", comment.Transport)
}

func TestClient_CreateComment_APIError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/issues/{key}/comments", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)

		_, err := w.Write([]byte(`{"error": "not found"}`))
		assert.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	comment, err := cli.createComment(t.Context(), &CreateCommentRequest{
		KeyOrID: "INVALID-999",
		Text:    "comment",
	})
	require.Error(t, err)
	assert.Nil(t, comment)
	assert.Contains(t, err.Error(), "404")
}

// --- updateComment ---

func TestClient_UpdateComment(t *testing.T) {
	t.Parallel()

	updatedComment := TrackerComment{
		Self:   "https://api.tracker.yandex.net/v3/issues/TREK-123/comments/626",
		ID:     626,
		LongID: "5fa15a24ac894475abc",
		Text:   "Updated comment text",
		CreatedBy: TrackerUser{
			Self:        "",
			ID:          "11",
			Display:     "",
			PassportUID: 0,
			CloudUID:    "",
			Login:       "user1",
		},
		UpdatedBy: TrackerUser{
			Self:        "",
			ID:          "22",
			Display:     "",
			PassportUID: 0,
			CloudUID:    "",
			Login:       "user2",
		},
		CreatedAt: "2025-06-01T10:00:00.000+0000",
		UpdatedAt: "2025-06-05T14:30:00.000+0000",
		Version:   2,
		Type:      "standard",
		Transport: "internal",
	}

	mux := http.NewServeMux()
	mux.HandleFunc(
		"PATCH /v3/issues/{key}/comments/{commentID}",
		func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "TREK-123", r.PathValue("key"))
			assert.Equal(t, "626", r.PathValue("commentID"))
			assert.Equal(t, "PATCH", r.Method)

			var body map[string]any

			err := json.NewDecoder(r.Body).Decode(&body)
			assert.NoError(t, err)

			assert.Equal(t, "Updated comment text", body[bodyKeyText])

			w.Header().Set("Content-Type", "application/json")

			err = json.NewEncoder(w).Encode(updatedComment)
			assert.NoError(t, err)
		},
	)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	comment, err := cli.updateComment(t.Context(), &UpdateCommentRequest{
		KeyOrID:   "TREK-123",
		CommentID: "626",
		Text:      "Updated comment text",
	})
	require.NoError(t, err)
	require.NotNil(t, comment)

	assert.Equal(t, int64(626), comment.ID)
	assert.Equal(t, "Updated comment text", comment.Text)
	assert.Equal(t, "user2", comment.UpdatedBy.Login)
	assert.Equal(t, 2, comment.Version)
}

func TestClient_UpdateComment_APIError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc(
		"PATCH /v3/issues/{key}/comments/{commentID}",
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)

			_, err := w.Write([]byte(`{"error": "not found"}`))
			assert.NoError(t, err)
		},
	)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cli := newTestClient(server.URL)

	comment, err := cli.updateComment(t.Context(), &UpdateCommentRequest{
		KeyOrID:   "TREK-123",
		CommentID: "999999",
		Text:      "update",
	})
	require.Error(t, err)
	assert.Nil(t, comment)
	assert.Contains(t, err.Error(), "404")
}
