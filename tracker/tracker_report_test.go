// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package tracker

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// isCompletedStatus
// ---------------------------------------------------------------------------

func TestIsCompletedStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		key  string
		want bool
	}{
		{"resolved", true},
		{"closed", true},
		{"open", false},
		{"inProgress", false},
		{"", false},
		{"RESOLVED", false}, // case-sensitive
	}

	for _, testCase := range cases {
		t.Run(testCase.key, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, testCase.want, isCompletedStatus(testCase.key))
		})
	}
}

// ---------------------------------------------------------------------------
// extractMonth
// ---------------------------------------------------------------------------

func TestExtractMonth(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"full ISO 8601", "2025-03-10T08:00:00Z", "2025-03"},
		{"YYYY-MM only", "2025-03", "2025-03"},
		{"too short", "2025", ""},
		{"empty", "", ""},
		{"just under threshold", "2025-0", ""},
		{"exactly at threshold", "2025-03", "2025-03"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, testCase.want, extractMonth(testCase.input))
		})
	}
}

// ---------------------------------------------------------------------------
// truncate
// ---------------------------------------------------------------------------

func TestTruncate_NoTruncation(t *testing.T) {
	t.Parallel()

	require.Equal(t, "hello", truncate("hello", 10))
	require.Equal(t, "hello", truncate("hello", 5))
}

func TestTruncate_Truncated(t *testing.T) {
	t.Parallel()

	got := truncate("hello world this is long", 10)
	require.Equal(t, "hello worl...", got)
}

func TestTruncate_Empty(t *testing.T) {
	t.Parallel()

	require.Empty(t, truncate("", 10))
}

// ---------------------------------------------------------------------------
// formatPeriod
// ---------------------------------------------------------------------------

func TestFormatPeriod_BothSet(t *testing.T) {
	t.Parallel()

	require.Equal(t, "2025-01-01 — 2025-12-31", formatPeriod("2025-01-01", "2025-12-31"))
}

func TestFormatPeriod_FromOnly(t *testing.T) {
	t.Parallel()

	require.Equal(t, "from 2025-01-01", formatPeriod("2025-01-01", ""))
}

func TestFormatPeriod_ToOnly(t *testing.T) {
	t.Parallel()

	require.Equal(t, "until 2025-12-31", formatPeriod("", "2025-12-31"))
}

func TestFormatPeriod_NeitherSet(t *testing.T) {
	t.Parallel()

	require.Equal(t, "all time", formatPeriod("", ""))
}

// ---------------------------------------------------------------------------
// buildSearchQuery
// ---------------------------------------------------------------------------

func TestBuildSearchQuery_AssigneeOnly(t *testing.T) {
	t.Parallel()

	got := buildSearchQuery(emptyReportRequest("alice"))
	require.Equal(t, "Assignee: alice", got)
}

func TestBuildSearchQuery_FullFilters(t *testing.T) {
	t.Parallel()

	req := emptyReportRequest("alice")

	req.DateFrom = "2025-01-01"

	req.DateTo = "2025-12-31"

	req.Queue = "TREK"

	got := buildSearchQuery(req)
	require.Equal(t,
		"Assignee: alice Updated: >=2025-01-01 Updated: <=2025-12-31 Queue: TREK",
		got,
	)
}

func TestBuildSearchQuery_DateFromOnly(t *testing.T) {
	t.Parallel()

	req := emptyReportRequest("alice")

	req.DateFrom = "2025-01-01"

	got := buildSearchQuery(req)
	require.Equal(t, "Assignee: alice Updated: >=2025-01-01", got)
}

func TestBuildSearchQuery_DateToOnly(t *testing.T) {
	t.Parallel()

	req := emptyReportRequest("alice")

	req.DateTo = "2025-12-31"

	got := buildSearchQuery(req)
	require.Equal(t, "Assignee: alice Updated: <=2025-12-31", got)
}

func TestBuildSearchQuery_QueueOnly(t *testing.T) {
	t.Parallel()

	req := emptyReportRequest("alice")

	req.Queue = "DEV"

	got := buildSearchQuery(req)
	require.Equal(t, "Assignee: alice Queue: DEV", got)
}

// ---------------------------------------------------------------------------
// categorizeIssue
// ---------------------------------------------------------------------------

//nolint:unparam // helper kept for future per-test assignee variants
func emptyReportRequest(assignee string) *ReportRequest {
	return &ReportRequest{
		Assignee: assignee,
		DateFrom: "",
		DateTo:   "",
		Queue:    "",
		WikiSlug: "",
	}
}

// ---------------------------------------------------------------------------
// generateReport (httptest-backed: exercises categorizeIssue, issueToReportTask)
// ---------------------------------------------------------------------------

// stubSearchResponse writes a SearchIssuesResponse-shaped body for the
// search endpoint so generateReport can iterate over the issues.
func stubSearchResponse(t *testing.T, issues []TrackerIssueShort) *httptest.Server {
	t.Helper()

	return stubSearchWith(t, issues, "[]")
}

// stubSearchWith registers a stub search endpoint and a configurable
// comments body. The comments handler responds to any GET on a
// /v3/issues/.../comments path; the returned body is the raw JSON
// array (e.g. "[]", or a populated array).
func stubSearchWith(
	t *testing.T,
	issues []TrackerIssueShort,
	commentsBody string,
) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/issues/_search", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, marshalSearchResponse(issues))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// /v3/issues/<key>/comments
		if strings.HasPrefix(r.URL.Path, "/v3/issues/") &&
			strings.HasSuffix(r.URL.Path, "/comments") {

			w.Header().Set("Content-Type", "application/json")
			//nolint:errcheck // hard-coded response; write error is not actionable
			_, _ = io.WriteString(w, commentsBody)

			return
		}

		// /v3/issues/<key>
		if strings.HasPrefix(r.URL.Path, "/v3/issues/") {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		w.WriteHeader(http.StatusNotFound)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

// marshalSearchResponse serializes the given issues as a Tracker
// search-issues response body. The Tracker search endpoint returns
// a JSON array of issues (not an object), and the total count is
// conveyed via the X-Total-Count header. The report helpers do not
// consult the headers.
func marshalSearchResponse(issues []TrackerIssueShort) string {
	body, _ := json.Marshal(issues)

	return string(body)
}

// issueFixtureFields is the set of field values an issueFixture
// caller is most likely to vary between tests. Anything left at the
// zero value is documented on the struct.
type issueFixtureFields struct {
	key           string
	typeKey       string
	typeDisplay   string
	statusKey     string
	statusDisplay string
}

// issueFixture builds a fully-populated TrackerIssueShort from the
// required fields. All optional fields are left at their zero value.
// Centralizing the literal avoids lint noise on every test that
// needs an issue.
func issueFixture(fields *issueFixtureFields) TrackerIssueShort {
	return TrackerIssueShort{
		Key:         fields.key,
		Summary:     "",
		Description: "",
		Type: TrackerStatus{
			Self:    "",
			ID:      "",
			Key:     fields.typeKey,
			Display: fields.typeDisplay,
		},
		Priority: TrackerStatus{},
		Status: TrackerStatus{
			Self:    "",
			ID:      "",
			Key:     fields.statusKey,
			Display: fields.statusDisplay,
		},
		Assignee:  (*TrackerUser)(nil),
		CreatedBy: TrackerUser{},
		Queue: TrackerQueue{
			Self:    "",
			ID:      "",
			Key:     "TREK",
			Display: "Trek",
		},
		Sprint:     []TrackerSprint(nil),
		Tags:       []string(nil),
		CreatedAt:  "",
		UpdatedAt:  "",
		ResolvedAt: "",
	}
}

// TestGenerateReport_NoWikiContext exercises the happy path: one
// resolved issue, no wiki context. The report should classify the
// issue as completed and update metrics accordingly.
func TestGenerateReport_NoWikiContext(t *testing.T) {
	t.Parallel()

	issues := []TrackerIssueShort{
		issueFixture(&issueFixtureFields{
			key: "TREK-1", typeKey: "task", typeDisplay: "Task",
			statusKey: "resolved", statusDisplay: "Resolved",
		}),
	}

	srv := stubSearchResponse(t, issues)
	cli := newTestClient(srv.URL)

	report, err := generateReport(t.Context(), cli, &ReportRequest{
		Assignee: "alice",
		DateFrom: "",
		DateTo:   "",
		Queue:    "",
		WikiSlug: "",
	})
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, 1, report.Metrics.TotalIssues)
	assert.Equal(t, 1, report.Metrics.CompletedCount)
	assert.Equal(t, 0, report.Metrics.InProgressCount)
	assert.Equal(t, 1, report.Metrics.ByType["Task"])
	assert.Equal(t, 1, report.Metrics.ByQueue["TREK"])
	assert.Len(t, report.CompletedTasks, 1)
	assert.Empty(t, report.InProgressTasks)
	assert.Equal(t, "alice", report.Assignee)
	assert.Nil(t, report.WikiContext)
}

// TestGenerateReport_MixedStatus covers the categorizeIssue branch
// where some issues are completed and others are in-progress.
func TestGenerateReport_MixedStatus(t *testing.T) {
	t.Parallel()

	issues := []TrackerIssueShort{
		issueFixture(&issueFixtureFields{
			key: "TREK-1", typeKey: "task", typeDisplay: "Task",
			statusKey: "resolved", statusDisplay: "Resolved",
		}),
		issueFixture(&issueFixtureFields{
			key: "TREK-2", typeKey: "bug", typeDisplay: "Bug",
			statusKey: "open", statusDisplay: "Open",
		}),
		issueFixture(&issueFixtureFields{
			key: "TREK-3", typeKey: "task", typeDisplay: "Task",
			statusKey: "closed", statusDisplay: "Closed",
		}),
	}

	srv := stubSearchResponse(t, issues)
	cli := newTestClient(srv.URL)

	report, err := generateReport(t.Context(), cli, &ReportRequest{
		Assignee: "alice",
		DateFrom: "",
		DateTo:   "",
		Queue:    "",
		WikiSlug: "",
	})
	require.NoError(t, err)
	assert.Equal(t, 3, report.Metrics.TotalIssues)
	assert.Equal(t, 2, report.Metrics.CompletedCount)
	assert.Equal(t, 1, report.Metrics.InProgressCount)
	assert.Len(t, report.CompletedTasks, 2)
	assert.Len(t, report.InProgressTasks, 1)
}

// TestGenerateReport_WithWikiContext exercises the fetchWikiContext
// branch.
func TestGenerateReport_WithWikiContext(t *testing.T) {
	t.Parallel()

	issues := []TrackerIssueShort{
		issueFixture(&issueFixtureFields{
			key: "TREK-1", typeKey: "task", typeDisplay: "Task",
			statusKey: "resolved", statusDisplay: "Resolved",
		}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/issues/_search", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, marshalSearchResponse(issues))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v3/issues/") &&
			strings.HasSuffix(r.URL.Path, "/comments") {

			w.Header().Set("Content-Type", "application/json")
			//nolint:errcheck // hard-coded response; write error is not actionable
			_, _ = io.WriteString(w, "[]")

			return
		}

		if strings.HasPrefix(r.URL.Path, "/v1/pages") {
			w.Header().Set("Content-Type", "application/json")
			//nolint:errcheck // hard-coded response; write error is not actionable
			_, _ = io.WriteString(w, `{
				"id":42,
				"slug":"epic-2025",
				"title":"Epic 2025",
				"content":"Some context"
			}`)

			return
		}

		w.WriteHeader(http.StatusNotFound)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := newTestClient(srv.URL)

	report, err := generateReport(t.Context(), cli, &ReportRequest{
		Assignee: "alice",
		DateFrom: "",
		DateTo:   "",
		Queue:    "",
		WikiSlug: "epic-2025",
	})
	require.NoError(t, err)
	require.NotNil(t, report.WikiContext)
	assert.Equal(t, "Epic 2025", report.WikiContext.Title)
	assert.Equal(t, "epic-2025", report.WikiContext.Slug)
	assert.Equal(t, "Some context", report.WikiContext.Content)
}

// TestGenerateReport_WithDateRange verifies that the buildSearchQuery
// call inside generateReport picks up the date filters.
func TestGenerateReport_WithDateRange(t *testing.T) {
	t.Parallel()

	issues := []TrackerIssueShort{
		issueFixture(&issueFixtureFields{
			key: "TREK-1", typeKey: "task", typeDisplay: "Task",
			statusKey: "resolved", statusDisplay: "Resolved",
		}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/issues/_search", func(w http.ResponseWriter, r *http.Request) {
		//nolint:errcheck // body is best-effort; we only inspect the parsed query below
		body, _ := io.ReadAll(r.Body)
		// The request body is JSON; the query is inside it. Decode
		// the body to confirm the user-typed filters made it through,
		// since JSON encoding escapes the > and < characters.
		var payload struct {
			Query string `json:"query"`
		}

		_ = json.Unmarshal(body, &payload) //nolint:errcheck // best-effort query inspection
		assert.Contains(t, payload.Query, "Assignee: alice")
		assert.Contains(t, payload.Query, "Updated: >=2025-01-01")
		assert.Contains(t, payload.Query, "Updated: <=2025-12-31")
		assert.Contains(t, payload.Query, "Queue: TREK")

		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, marshalSearchResponse(issues))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v3/issues/") &&
			strings.HasSuffix(r.URL.Path, "/comments") {

			w.Header().Set("Content-Type", "application/json")
			//nolint:errcheck // hard-coded response; write error is not actionable
			_, _ = io.WriteString(w, "[]")

			return
		}

		w.WriteHeader(http.StatusNotFound)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := newTestClient(srv.URL)

	report, err := generateReport(t.Context(), cli, &ReportRequest{
		Assignee: "alice",
		DateFrom: "2025-01-01",
		DateTo:   "2025-12-31",
		Queue:    "TREK",
		WikiSlug: "",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Metrics.TotalIssues)
}

// TestGenerateReport_SearchAPIError covers the path where the
// search endpoint returns 5xx; the report is nil and the error
// is wrapped.
func TestGenerateReport_SearchAPIError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/issues/_search", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, "boom")
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := newTestClient(srv.URL)

	report, err := generateReport(t.Context(), cli, &ReportRequest{
		Assignee: "alice",
		DateFrom: "",
		DateTo:   "",
		Queue:    "",
		WikiSlug: "",
	})
	require.Error(t, err)
	require.Nil(t, report)
	assert.Contains(t, err.Error(), "search issues for report")
}

// TestGenerateReport_WikiErrorIsIgnored covers the path where the
// wiki context fetch fails; the report should still be returned,
// just without WikiContext.
func TestGenerateReport_WikiErrorIsIgnored(t *testing.T) {
	t.Parallel()

	issues := []TrackerIssueShort{
		issueFixture(&issueFixtureFields{
			key: "TREK-1", typeKey: "task", typeDisplay: "Task",
			statusKey: "resolved", statusDisplay: "Resolved",
		}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/issues/_search", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // hard-coded response; write error is not actionable
		_, _ = io.WriteString(w, marshalSearchResponse(issues))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v3/issues/") &&
			strings.HasSuffix(r.URL.Path, "/comments") {

			w.Header().Set("Content-Type", "application/json")
			//nolint:errcheck // hard-coded response; write error is not actionable
			_, _ = io.WriteString(w, "[]")

			return
		}

		// All other paths (including /v2/pages/) return 404 to
		// exercise the wiki-error path.
		w.WriteHeader(http.StatusNotFound)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := newTestClient(srv.URL)

	report, err := generateReport(t.Context(), cli, &ReportRequest{
		Assignee: "alice",
		DateFrom: "",
		DateTo:   "",
		Queue:    "",
		WikiSlug: "missing-page",
	})
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Nilf(t, report.WikiContext, "wiki fetch failure must not set WikiContext")
}

// TestCategorizeIssue_ResolvedMonth covers the by-month metric in
// categorizeIssue (i.e. the resolved issue has a ResolvedAt).
func TestCategorizeIssue_ResolvedMonth(t *testing.T) {
	t.Parallel()

	report := &ReportResponse{
		Period:          "",
		Assignee:        "",
		CompletedTasks:  []ReportTask{},
		InProgressTasks: []ReportTask{},
		Metrics: ReportMetrics{
			TotalIssues:     0,
			CompletedCount:  0,
			InProgressCount: 0,
			ByType:          make(map[string]int),
			ByQueue:         make(map[string]int),
			ByMonth:         make(map[string]int),
		},
		WikiContext: (*WikiContext)(nil),
	}

	issue := issueFixture(&issueFixtureFields{
		key: "TREK-1", typeKey: "task", typeDisplay: "Task",
		statusKey: "resolved", statusDisplay: "Resolved",
	})

	issue.ResolvedAt = "2025-03-15T10:00:00Z"

	task := ReportTask{
		Key:          "TREK-1",
		Summary:      "",
		Description:  "",
		Type:         "",
		Status:       "",
		Queue:        "",
		QueueName:    "",
		Tags:         []string(nil),
		CreatedAt:    "",
		UpdatedAt:    "",
		ResolvedAt:   "",
		Sprint:       "",
		CommentCount: 0,
		LastComments: []string(nil),
	}

	categorizeIssue(report, &issue, &task)
	assert.Equal(t, 1, report.Metrics.ByMonth["2025-03"])
	assert.Len(t, report.CompletedTasks, 1)
}

// TestCategorizeIssue_NotResolved covers the not-completed branch of
// categorizeIssue; the by-month metric must NOT be incremented.
func TestCategorizeIssue_NotResolved(t *testing.T) {
	t.Parallel()

	report := &ReportResponse{
		Period:          "",
		Assignee:        "",
		CompletedTasks:  []ReportTask{},
		InProgressTasks: []ReportTask{},
		Metrics: ReportMetrics{
			TotalIssues:     0,
			CompletedCount:  0,
			InProgressCount: 0,
			ByType:          make(map[string]int),
			ByQueue:         make(map[string]int),
			ByMonth:         make(map[string]int),
		},
		WikiContext: (*WikiContext)(nil),
	}

	issue := issueFixture(&issueFixtureFields{
		key: "TREK-1", typeKey: "task", typeDisplay: "Task",
		statusKey: "open", statusDisplay: "Open",
	})

	task := ReportTask{
		Key:          "TREK-1",
		Summary:      "",
		Description:  "",
		Type:         "",
		Status:       "",
		Queue:        "",
		QueueName:    "",
		Tags:         []string(nil),
		CreatedAt:    "",
		UpdatedAt:    "",
		ResolvedAt:   "",
		Sprint:       "",
		CommentCount: 0,
		LastComments: []string(nil),
	}

	categorizeIssue(report, &issue, &task)
	assert.Emptyf(t, report.Metrics.ByMonth, "unresolved issue should not bump by-month")
	assert.Len(t, report.InProgressTasks, 1)
	assert.Empty(t, report.CompletedTasks)
}

// TestCategorizeIssue_ResolvedNoDate covers the branch where
// ResolvedAt is empty on a resolved issue (e.g. legacy data) — the
// by-month metric is skipped, the issue is still classified as
// completed.
func TestCategorizeIssue_ResolvedNoDate(t *testing.T) {
	t.Parallel()

	report := &ReportResponse{
		Period:          "",
		Assignee:        "",
		CompletedTasks:  []ReportTask{},
		InProgressTasks: []ReportTask{},
		Metrics: ReportMetrics{
			TotalIssues:     0,
			CompletedCount:  0,
			InProgressCount: 0,
			ByType:          make(map[string]int),
			ByQueue:         make(map[string]int),
			ByMonth:         make(map[string]int),
		},
		WikiContext: (*WikiContext)(nil),
	}

	issue := issueFixture(&issueFixtureFields{
		key: "TREK-1", typeKey: "task", typeDisplay: "Task",
		statusKey: "resolved", statusDisplay: "Resolved",
	})

	task := ReportTask{
		Key:          "TREK-1",
		Summary:      "",
		Description:  "",
		Type:         "",
		Status:       "",
		Queue:        "",
		QueueName:    "",
		Tags:         []string(nil),
		CreatedAt:    "",
		UpdatedAt:    "",
		ResolvedAt:   "",
		Sprint:       "",
		CommentCount: 0,
		LastComments: []string(nil),
	}

	categorizeIssue(report, &issue, &task)
	assert.Empty(t, report.Metrics.ByMonth)
	assert.Len(t, report.CompletedTasks, 1)
}

// TestIssueToReportTask_AllFields exercises every branch of
// issueToReportTask by populating all the optional fields on the
// issue.
func TestIssueToReportTask_AllFields(t *testing.T) {
	t.Parallel()

	issues := []TrackerIssueShort{
		func() TrackerIssueShort {
			issue := issueFixture(&issueFixtureFields{
				key: "TREK-1", typeKey: "task", typeDisplay: "Task",
				statusKey: "open", statusDisplay: "Open",
			})

			issue.Summary = "Fix bug"

			issue.Description = strings.Repeat("A long description that needs truncation. ", 30)

			issue.Tags = []string{"a", "b"}

			issue.CreatedAt = "2025-01-01T00:00:00Z"

			issue.UpdatedAt = "2025-06-01T00:00:00Z"

			issue.ResolvedAt = "2025-06-15T10:00:00Z"

			issue.Sprint = []TrackerSprint{
				{Self: "", ID: "", Display: "Sprint 1"},
			}

			return issue
		}(),
	}

	commentsBody := `[
		{"id":1,"text":"first comment"},
		{"id":2,"text":"second comment"},
		{"id":3,"text":"third comment"},
		{"id":4,"text":"fourth comment"}
	]`

	srv := stubSearchWith(t, issues, commentsBody)
	cli := newTestClient(srv.URL)

	task := issueToReportTask(t.Context(), cli, &issues[0])
	assert.Equal(t, "TREK-1", task.Key)
	assert.Equal(t, "Fix bug", task.Summary)
	assert.Equal(t, "Task", task.Type)
	assert.Equal(t, "Open", task.Status)
	assert.Equal(t, "TREK", task.Queue)
	assert.Equal(t, "Trek", task.QueueName)
	assert.Equal(t, []string{"a", "b"}, task.Tags)
	assert.Equal(t, "2025-01-01T00:00:00Z", task.CreatedAt)
	assert.Equal(t, "2025-06-01T00:00:00Z", task.UpdatedAt)
	assert.Equal(t, "2025-06-15T10:00:00Z", task.ResolvedAt)
	assert.Equal(t, "Sprint 1", task.Sprint)
	assert.Len(t, task.Description, maxDescriptionLength+3) // includes "..."
	assert.Equal(t, 4, task.CommentCount)
	// Only the last 3 comments are kept.
	assert.Len(t, task.LastComments, 3)
}

// TestIssueToReportTask_CommentsFetchFails covers the path where
// getComments returns an error — the task is returned with no
// comment context (graceful degradation).
func TestIssueToReportTask_CommentsFetchFails(t *testing.T) {
	t.Parallel()

	issue := issueFixture(&issueFixtureFields{
		key: "TREK-1", typeKey: "task", typeDisplay: "Task",
		statusKey: "open", statusDisplay: "Open",
	})

	issue.Summary = "Fix bug"

	issue.Description = "x"

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := newTestClient(srv.URL)

	task := issueToReportTask(t.Context(), cli, &issue)
	assert.Equal(t, "TREK-1", task.Key)
	assert.Equal(t, 0, task.CommentCount)
	assert.Empty(t, task.LastComments)
}

// TestFetchWikiContext_Success exercises the happy path of
// fetchWikiContext.
func TestFetchWikiContext_Success(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/pages") {
			w.Header().Set("Content-Type", "application/json")
			//nolint:errcheck // hard-coded response; write error is not actionable
			_, _ = io.WriteString(w, `{
				"id":42,
				"slug":"epic-2025",
				"title":"Epic 2025",
				"content":"Some context"
			}`)

			return
		}

		w.WriteHeader(http.StatusNotFound)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := newTestClient(srv.URL)

	ctx, err := fetchWikiContext(t.Context(), cli, "epic-2025")
	require.NoError(t, err)
	require.NotNil(t, ctx)
	assert.Equal(t, "Epic 2025", ctx.Title)
	assert.Equal(t, "Some context", ctx.Content)
	assert.Equal(t, "epic-2025", ctx.Slug)
}

// TestFetchWikiContext_APIError covers the path where the wiki
// endpoint returns 5xx; the error is wrapped.
func TestFetchWikiContext_APIError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(_ http.ResponseWriter, _ *http.Request) {
		// The wiki path returns 5xx; all other paths are not used
		// in this test.
	})

	mux.HandleFunc("/v1/pages", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := newTestClient(srv.URL)

	ctx, err := fetchWikiContext(t.Context(), cli, "epic-2025")
	require.Error(t, err)
	assert.Nil(t, ctx)
	assert.Contains(t, err.Error(), "fetch wiki context")
}
