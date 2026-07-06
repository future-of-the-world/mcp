// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package tracker

import (
	"context"
	"fmt"
	"strings"
)

const (
	// maxReportCommentsPerIssue limits the number of last comments included per issue.
	maxReportCommentsPerIssue = 3

	// defaultSearchQueryCapacity is the initial capacity for search query parts.
	defaultSearchQueryCapacity = 4

	// maxDescriptionLength is the maximum length for truncated issue descriptions.
	maxDescriptionLength = 500

	// maxCommentLength is the maximum length for truncated comment text.
	maxCommentLength = 200

	// isoMonthLength is the minimum length of a string to extract YYYY-MM from.
	isoMonthLength = 7
)

// generateReport creates a retrospective report by aggregating data from
// Tracker issues, comments, and optionally a Wiki page.
func generateReport(ctx context.Context, cli *client,
	req *ReportRequest,
) (*ReportResponse, error) {
	// Build the search query for issues assigned to the user in the date range.
	query := buildSearchQuery(req)

	searchResp, err := cli.searchIssues(ctx, &SearchIssuesRequest{
		Query:   query,
		Filter:  map[string]string(nil),
		Queue:   "",
		Keys:    []string(nil),
		Order:   "",
		Fields:  "",
		PerPage: 100,
		Page:    1,
	})
	if err != nil {
		return nil, fmt.Errorf("search issues for report: %w", err)
	}

	// Categorize issues into completed and in-progress.
	report := &ReportResponse{
		Period:          formatPeriod(req.DateFrom, req.DateTo),
		Assignee:        req.Assignee,
		CompletedTasks:  make([]ReportTask, 0),
		InProgressTasks: make([]ReportTask, 0),
		Metrics: ReportMetrics{
			TotalIssues:     len(searchResp.Issues),
			CompletedCount:  0,
			InProgressCount: 0,
			ByType:          make(map[string]int),
			ByQueue:         make(map[string]int),
			ByMonth:         make(map[string]int),
		},
		WikiContext: (*WikiContext)(nil),
	}

	for i := range searchResp.Issues {
		task := issueToReportTask(ctx, cli, &searchResp.Issues[i])

		categorizeIssue(
			report,
			&searchResp.Issues[i],
			&task,
		)
	}

	// Fetch wiki context if requested.
	if req.WikiSlug != "" {
		wikiCtx, err := fetchWikiContext(ctx, cli, req.WikiSlug)
		if err != nil {
			// Wiki context is optional — log but don't fail.
			_ = err
		} else {
			report.WikiContext = wikiCtx
		}
	}

	return report, nil
}

// buildSearchQuery constructs a Tracker query language string from the report request.
func buildSearchQuery(req *ReportRequest) string {
	parts := make([]string, 0, defaultSearchQueryCapacity)

	parts = append(parts, fmt.Sprintf("Assignee: %s", req.Assignee))

	if req.DateFrom != "" {
		parts = append(parts, fmt.Sprintf("Updated: >=%s", req.DateFrom))
	}

	if req.DateTo != "" {
		parts = append(parts, fmt.Sprintf("Updated: <=%s", req.DateTo))
	}

	if req.Queue != "" {
		parts = append(parts, fmt.Sprintf("Queue: %s", req.Queue))
	}

	return strings.Join(parts, " ")
}

// categorizeIssue classifies a single issue as completed or in-progress
// and appends it to the appropriate slice in the report while updating metrics.
func categorizeIssue(report *ReportResponse, issue *TrackerIssueShort, task *ReportTask) {
	report.Metrics.ByType[issue.Type.Display]++
	report.Metrics.ByQueue[issue.Queue.Key]++

	if isCompletedStatus(issue.Status.Key) {
		report.CompletedTasks = append(report.CompletedTasks, *task)
		report.Metrics.CompletedCount++

		if month := extractMonth(issue.ResolvedAt); month != "" {
			report.Metrics.ByMonth[month]++
		}
	} else {
		report.InProgressTasks = append(report.InProgressTasks, *task)
		report.Metrics.InProgressCount++
	}
}

// issueToReportTask converts a TrackerIssueShort to a ReportTask, fetching
// comments for context.
func issueToReportTask(
	ctx context.Context,
	cli *client,
	issue *TrackerIssueShort,
) ReportTask {
	task := ReportTask{
		Key:          issue.Key,
		Summary:      issue.Summary,
		Description:  "",
		Type:         issue.Type.Display,
		Status:       issue.Status.Display,
		Queue:        issue.Queue.Key,
		QueueName:    issue.Queue.Display,
		Tags:         issue.Tags,
		CreatedAt:    issue.CreatedAt,
		UpdatedAt:    issue.UpdatedAt,
		ResolvedAt:   "",
		Sprint:       "",
		CommentCount: 0,
		LastComments: []string(nil),
	}

	if issue.Description != "" {
		task.Description = truncate(issue.Description, maxDescriptionLength)
	}

	if issue.ResolvedAt != "" {
		task.ResolvedAt = issue.ResolvedAt
	}

	if len(issue.Sprint) > 0 {
		task.Sprint = issue.Sprint[0].Display
	}

	// Fetch comments for context.
	comments, err := cli.getComments(ctx, GetCommentsRequest{KeyOrID: issue.Key})
	if err != nil {
		// Comments are optional enrichment — don't fail.
		return task
	}

	task.CommentCount = len(comments)

	// Take the last N comments.
	start := max(0, len(comments)-maxReportCommentsPerIssue)

	task.LastComments = make([]string, 0, len(comments)-start)
	for i := start; i < len(comments); i++ {
		task.LastComments = append(task.LastComments, truncate(comments[i].Text, maxCommentLength))
	}

	return task
}

// fetchWikiContext fetches a wiki page and returns it as WikiContext.
func fetchWikiContext(
	ctx context.Context,
	cli *client,
	slug string,
) (*WikiContext, error) {
	page, err := cli.getWikiPage(ctx, GetWikiPageRequest{Slug: slug, PageID: 0})
	if err != nil {
		return nil, fmt.Errorf("fetch wiki context: %w", err)
	}

	return &WikiContext{
		Title:   page.Title,
		Content: page.Content,
		Slug:    page.Slug,
	}, nil
}

// --- Helpers ---

// isCompletedStatus returns true if the status key indicates a resolved or closed issue.
func isCompletedStatus(key string) bool {
	switch key {
	case "resolved", "closed":
		return true

	default:
		return false
	}
}

// extractMonth extracts YYYY-MM from an ISO 8601 datetime string.
func extractMonth(isoDate string) string {
	if len(isoDate) < isoMonthLength {
		return ""
	}

	return isoDate[:isoMonthLength]
}

// truncate shortens a string to maxLen, adding "..." if truncated.
func truncate(str string, maxLen int) string {
	if len(str) <= maxLen {
		return str
	}

	return str[:maxLen] + "..."
}

// formatPeriod creates a human-readable period description.
func formatPeriod(from, toDate string) string {
	if from != "" && toDate != "" {
		return from + " — " + toDate
	}

	if from != "" {
		return "from " + from
	}

	if toDate != "" {
		return "until " + toDate
	}

	return "all time"
}
