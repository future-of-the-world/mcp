// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

// Package tracker implements MCP tools for Yandex Tracker and Yandex Wiki APIs.
// It provides tools for searching issues, getting issue details and comments,
// reading wiki pages, and generating retrospective reports.

package tracker

import "errors"

const (
	// defaultBaseURL is the default Yandex Tracker API endpoint.
	defaultBaseURL = "https://api.tracker.yandex.net"

	// defaultWikiBaseURL is the default Yandex Wiki API endpoint.
	defaultWikiBaseURL = "https://api.wiki.yandex.net/v1"

	// defaultOAuthTokenURL is the default Yandex OAuth token endpoint.
	//nolint:gosec // G101: this is an endpoint URL, not a credential
	defaultOAuthTokenURL = "https://oauth.yandex.com/token"

	// defaultOAuthDeviceURL is the default Yandex OAuth device code endpoint.
	defaultOAuthDeviceURL = "https://oauth.yandex.com/device/code"
)

var (
	errTokenEmpty        = errors.New("tracker tool: token is empty")
	errOrgIDEmpty        = errors.New("tracker tool: org_id is empty")
	errIssueKeyOrIDEmpty = errors.New("tracker tool: issue key or ID is required")
	errWikiSlugOrIDEmpty = errors.New("tracker tool: wiki page slug or ID is required")
	errReportAssignee    = errors.New("tracker tool: assignee is required for report")

	errClientSecretEmpty = errors.New("tracker tool: client_secret is empty when using OAuth")
	errOAuthFailed       = errors.New("tracker tool: OAuth token exchange failed")

	errSummaryEmpty     = errors.New("tracker tool: summary is required")
	errQueueEmpty       = errors.New("tracker tool: queue is required")
	errCommentTextEmpty = errors.New("tracker tool: comment text is required")
	errCommentIDEmpty   = errors.New("tracker tool: comment ID is required")

	errCloudOrgMustBeBool = errors.New("tracker: connect.cloud_org must be a bool")
)

// --- Shared types ---

// TrackerUser represents a user in the Tracker API.
//
//nolint:lll // struct tags with nolint directives exceed line length
type TrackerUser struct {
	Self        string `json:"self,omitzero"        jsonschema:"API URL of the user resource"`
	ID          string `json:"id,omitzero"          jsonschema:"User ID"`
	Display     string `json:"display,omitzero"     jsonschema:"User display name"`
	PassportUID int64  `json:"passportUid,omitzero" jsonschema:"Passport user ID"`     //nolint:tagliatelle // Tracker API camelCase
	CloudUID    string `json:"cloudUid,omitzero"    jsonschema:"Yandex Cloud user ID"` //nolint:tagliatelle // Tracker API camelCase
	Login       string `json:"login,omitzero"       jsonschema:"User login name"`
}

// TrackerStatus represents an issue status/type/priority with key+display.
type TrackerStatus struct {
	Self    string `json:"self,omitzero"    jsonschema:"API URL of the status resource"`
	ID      string `json:"id,omitzero"      jsonschema:"Status ID"`
	Key     string `json:"key,omitzero"     jsonschema:"Status key (e.g. open, resolved, closed)"`
	Display string `json:"display,omitzero" jsonschema:"Human-readable status name"`
}

// TrackerQueue represents the queue an issue belongs to.
type TrackerQueue struct {
	Self    string `json:"self,omitzero"    jsonschema:"API URL of the queue resource"`
	ID      string `json:"id,omitzero"      jsonschema:"Queue ID"`
	Key     string `json:"key,omitzero"     jsonschema:"Queue key (e.g. TREK, FRONT)"`
	Display string `json:"display,omitzero" jsonschema:"Human-readable queue name"`
}

// TrackerSprint represents a sprint.
type TrackerSprint struct {
	Self    string `json:"self,omitzero"    jsonschema:"API URL of the sprint resource"`
	ID      string `json:"id,omitzero"      jsonschema:"Sprint ID"`
	Display string `json:"display,omitzero" jsonschema:"Sprint name"`
}

// TrackerIssueShort is a flat issue representation for search results.
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type TrackerIssueShort struct {
	Key         string          `json:"key"                  jsonschema:"Issue key (e.g. TREK-123)"`
	Summary     string          `json:"summary"              jsonschema:"Issue title"`
	Description string          `json:"description,omitzero" jsonschema:"Issue description (may contain HTML)"`
	Type        TrackerStatus   `json:"type,omitzero"        jsonschema:"Issue type (task, bug, epic, etc.)"`
	Priority    TrackerStatus   `json:"priority,omitzero"    jsonschema:"Issue priority"`
	Status      TrackerStatus   `json:"status,omitzero"      jsonschema:"Current issue status"`
	Assignee    *TrackerUser    `json:"assignee,omitzero"    jsonschema:"User assigned to the issue"`
	CreatedBy   TrackerUser     `json:"createdBy,omitzero"   jsonschema:"User who created the issue"` //nolint:tagliatelle // Tracker API camelCase
	Queue       TrackerQueue    `json:"queue"                jsonschema:"Queue the issue belongs to"`
	Sprint      []TrackerSprint `json:"sprint,omitzero"      jsonschema:"Sprints the issue belongs to"`
	Tags        []string        `json:"tags,omitzero"        jsonschema:"Issue tags"`
	CreatedAt   string          `json:"createdAt,omitzero"   jsonschema:"ISO 8601 creation datetime"`    //nolint:tagliatelle // Tracker API camelCase
	UpdatedAt   string          `json:"updatedAt,omitzero"   jsonschema:"ISO 8601 last update datetime"` //nolint:tagliatelle // Tracker API camelCase
	ResolvedAt  string          `json:"resolvedAt,omitzero"  jsonschema:"ISO 8601 resolution datetime"`  //nolint:tagliatelle // Tracker API camelCase
}

// TrackerIssue is the full issue representation with additional fields.
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type TrackerIssue struct {
	TrackerIssueShort

	ID             string        `json:"id,omitzero"                   jsonschema:"Unique issue ID"`
	Version        int           `json:"version,omitzero"              jsonschema:"Issue version (incremented on each edit)"`
	Votes          int           `json:"votes,omitzero"                jsonschema:"Number of votes on the issue"`
	Favorite       bool          `json:"favorite,omitzero"             jsonschema:"Whether the issue is starred by the user"`
	Aliases        []string      `json:"aliases,omitzero"              jsonschema:"Alternative issue keys"`
	PreviousStatus TrackerStatus `json:"previousStatus,omitzero"       jsonschema:"Previous issue status"`           //nolint:tagliatelle // Tracker API camelCase
	UpdatedBy      TrackerUser   `json:"updatedBy,omitzero"            jsonschema:"User who last updated the issue"` //nolint:tagliatelle // Tracker API camelCase
	Followers      []TrackerUser `json:"followers,omitzero"            jsonschema:"Users following the issue"`
	LastCommentAt  string        `json:"lastCommentUpdatedAt,omitzero" jsonschema:"ISO 8601 datetime of last comment"` //nolint:tagliatelle // Tracker API camelCase
}

// TrackerLinkType represents the type of a link between issues.
type TrackerLinkType struct {
	Self    string `json:"self,omitzero"    jsonschema:"API URL of the link type resource"`
	ID      string `json:"id,omitzero"      jsonschema:"Link type ID"`
	Inward  string `json:"inward,omitzero"  jsonschema:"Inward name of the link type"`
	Outward string `json:"outward,omitzero" jsonschema:"Outward name of the link type"`
}

// TrackerLinkedIssue represents a linked issue in a Tracker link.
type TrackerLinkedIssue struct {
	Self    string `json:"self,omitzero"    jsonschema:"API URL of the linked issue"`
	ID      string `json:"id,omitzero"      jsonschema:"Issue ID"`
	Key     string `json:"key,omitzero"     jsonschema:"Issue key (e.g. TREK-9844)"`
	Display string `json:"display,omitzero" jsonschema:"Display name of the issue"`
}

// TrackerLink represents a link (relation) between Tracker issues.
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type TrackerLink struct {
	Self      string             `json:"self,omitzero"      jsonschema:"API URL of the link resource"`
	ID        int64              `json:"id,omitzero"        jsonschema:"Link ID"`
	Type      TrackerLinkType    `json:"type,omitzero"      jsonschema:"Link type information"`
	Direction string             `json:"direction,omitzero" jsonschema:"Relation direction"`
	Object    TrackerLinkedIssue `json:"object,omitzero"    jsonschema:"Linked issue information"`
	CreatedBy TrackerUser        `json:"createdBy,omitzero" jsonschema:"User who created the link"`      //nolint:tagliatelle // Tracker API camelCase
	UpdatedBy TrackerUser        `json:"updatedBy,omitzero" jsonschema:"User who last updated the link"` //nolint:tagliatelle // Tracker API camelCase
	CreatedAt string             `json:"createdAt,omitzero" jsonschema:"ISO 8601 creation datetime"`     //nolint:tagliatelle // Tracker API camelCase
	UpdatedAt string             `json:"updatedAt,omitzero" jsonschema:"ISO 8601 last update datetime"`  //nolint:tagliatelle // Tracker API camelCase
	Assignee  *TrackerUser       `json:"assignee,omitzero"  jsonschema:"Assignee of the linked issue"`
	Status    *TrackerStatus     `json:"status,omitzero"    jsonschema:"Status of the linked issue"`
}

// TrackerComment represents a comment on an issue.
//
//nolint:lll // struct tags with nolint directives exceed line length
type TrackerComment struct {
	Self      string      `json:"self,omitzero"      jsonschema:"API URL of the comment resource"`
	ID        int64       `json:"id"                 jsonschema:"Comment ID"`
	LongID    string      `json:"longId,omitzero"    jsonschema:"String-formatted comment ID"` //nolint:tagliatelle // Tracker API camelCase
	Text      string      `json:"text,omitzero"      jsonschema:"Comment text"`
	CreatedBy TrackerUser `json:"createdBy,omitzero" jsonschema:"User who created the comment"`      //nolint:tagliatelle // Tracker API camelCase
	UpdatedBy TrackerUser `json:"updatedBy,omitzero" jsonschema:"User who last updated the comment"` //nolint:tagliatelle // Tracker API camelCase
	UpdatedAt string      `json:"updatedAt,omitzero" jsonschema:"ISO 8601 last update datetime"`     //nolint:tagliatelle // Tracker API camelCase
	CreatedAt string      `json:"createdAt,omitzero" jsonschema:"ISO 8601 creation datetime"`        //nolint:tagliatelle // Tracker API camelCase
	Version   int         `json:"version,omitzero"   jsonschema:"Comment version, incremented on each edit"`
	Type      string      `json:"type,omitzero"      jsonschema:"Comment type (standard, incoming, outcoming)"`
	Transport string      `json:"transport,omitzero" jsonschema:"Transport type (internal, email)"`
}

// WikiPage represents a Yandex Wiki page.
type WikiPage struct {
	ID        int64           `json:"id"                  jsonschema:"Unique page ID"`
	Slug      string          `json:"slug,omitzero"       jsonschema:"Page slug (URL path)"`
	Title     string          `json:"title,omitzero"      jsonschema:"Page title"`
	Content   string          `json:"content,omitzero"    jsonschema:"Page body in wiki markup"`
	CreatedAt string          `json:"created_at,omitzero" jsonschema:"ISO 8601 creation datetime"`
	UpdatedAt string          `json:"updated_at,omitzero" jsonschema:"Last update datetime"`
	CreatedBy *TrackerUser    `json:"created_by,omitzero" jsonschema:"User who created the page"`
	UpdatedBy *TrackerUser    `json:"updated_by,omitzero" jsonschema:"User who last updated"`
	Parent    *WikiPageParent `json:"parent,omitzero"     jsonschema:"Parent page info (if any)"`
}

// WikiPageParent contains identifying information about a parent wiki page.
type WikiPageParent struct {
	ID    int64  `json:"id,omitzero"    jsonschema:"Parent page ID"`
	Slug  string `json:"slug,omitzero"  jsonschema:"Parent page slug (URL path)"`
	Title string `json:"title,omitzero" jsonschema:"Parent page title"`
}

// GetLinksRequest is the request for getting issue links.
type GetLinksRequest struct {
	KeyOrID string `json:"key_or_id" jsonschema:"Issue key (e.g. TREK-123) or numeric ID"`
}

// --- Request types ---

// SearchIssuesRequest is the request for searching Tracker issues.
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type SearchIssuesRequest struct {
	Query   string            `json:"query,omitzero"    jsonschema:"Tracker query language. Example: 'Assignee: user123 Updated: >=2025-01-01'. Use get_fields to discover available field names."`
	Filter  map[string]string `json:"filter,omitzero"   jsonschema:"Field-value filter pairs. Keys MUST be valid Tracker field IDs (use get_fields to discover them). Examples: queue=TREK, assignee=user123, status=open, type=bug. Never use 'issue' as a filter key."`
	Queue   string            `json:"queue,omitzero"    jsonschema:"Queue key to search in. Uses relative pagination. Not combinable with filter/query/keys."`
	Keys    []string          `json:"keys,omitzero"     jsonschema:"List of issue keys to fetch (e.g. TREK-1, TREK-2)"`
	Order   string            `json:"order,omitzero"    jsonschema:"Sort direction and field (e.g. +status or -updatedAt), only with filter"`
	Fields  string            `json:"fields,omitzero"   jsonschema:"Comma-separated Tracker field IDs to include in response. Use get_fields to discover valid IDs."`
	PerPage int               `json:"per_page,omitzero" jsonschema:"Number of issues per page (default: 50, max: 10000)"`
	Page    int               `json:"page,omitzero"     jsonschema:"Page number for standard pagination (default: 1)"`
}

// GetIssueRequest is the request for getting a single Tracker issue.
type GetIssueRequest struct {
	KeyOrID string `json:"key_or_id"       jsonschema:"Issue key (e.g. TREK-123) or numeric ID"`
	Fields  string `json:"fields,omitzero" jsonschema:"Comma-separated list of fields to include in response"` //nolint:lll // jsonschema description
}

// GetCommentsRequest is the request for getting issue comments.
type GetCommentsRequest struct {
	KeyOrID string `json:"key_or_id" jsonschema:"Issue key (e.g. TREK-123) or numeric ID"`
}

// GetWikiPageRequest is the request for getting a Wiki page.
type GetWikiPageRequest struct {
	Slug   string `json:"slug,omitzero"    jsonschema:"Page slug (URL path, e.g. users/test/page). Mutually exclusive with page_id."` //nolint:lll // jsonschema description
	PageID int64  `json:"page_id,omitzero" jsonschema:"Numeric page ID"`
}

// GetWikiSubpagesRequest is the request for listing Wiki subpages.
type GetWikiSubpagesRequest struct {
	Slug        string `json:"slug"                  jsonschema:"Parent page slug (URL path)"`
	IncludeSelf bool   `json:"include_self,omitzero" jsonschema:"Include parent in results"`
	PageSize    int    `json:"page_size,omitzero"    jsonschema:"Results per page (default 100)"`
}

// ReportRequest is the request for generating a retrospective report.
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type ReportRequest struct {
	Assignee string `json:"assignee"           jsonschema:"Login of the user to generate report for (required)"`
	DateFrom string `json:"date_from,omitzero" jsonschema:"Start date in YYYY-MM-DD format (e.g. 2025-01-01)"`
	DateTo   string `json:"date_to,omitzero"   jsonschema:"End date in YYYY-MM-DD format (e.g. 2025-12-31)"`
	Queue    string `json:"queue,omitzero"     jsonschema:"Optional queue key to filter issues by specific queue"`
	WikiSlug string `json:"wiki_slug,omitzero" jsonschema:"Optional wiki page slug to include as context for stories/epics"`
}

// --- Response types ---

// SearchIssuesResponse is the response for issue search.
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type SearchIssuesResponse struct {
	Issues     []TrackerIssueShort `json:"issues"               jsonschema:"List of issues matching the search criteria"`
	TotalCount int                 `json:"total_count,omitzero" jsonschema:"Total number of issues found"`
	Page       int                 `json:"page,omitzero"        jsonschema:"Current page number"`
	PerPage    int                 `json:"per_page,omitzero"    jsonschema:"Number of issues per page"`
}

// GetIssueResponse is the response for getting a single issue.
type GetIssueResponse struct {
	Issue TrackerIssue `json:"issue" jsonschema:"Full issue details"`
}

// GetLinksResponse is the response for getting issue links.
type GetLinksResponse struct {
	Links []TrackerLink `json:"links,omitzero" jsonschema:"List of links for the issue"`
}

// GetCommentsResponse is the response for getting issue comments.
type GetCommentsResponse struct {
	Comments []TrackerComment `json:"comments,omitzero" jsonschema:"List of comments on the issue"`
}

// GetWikiPageResponse is the response for getting a Wiki page.
type GetWikiPageResponse struct {
	Page WikiPage `json:"page" jsonschema:"Wiki page details"`
}

// GetWikiSubpagesResponse is the response for listing Wiki subpages.
type GetWikiSubpagesResponse struct {
	Pages []WikiPage `json:"pages,omitzero" jsonschema:"List of subpages"`
}

// ReportResponse is the structured retrospective report.
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type ReportResponse struct {
	Period          string        `json:"period"                   jsonschema:"Report period description"`
	Assignee        string        `json:"assignee"                 jsonschema:"User the report is generated for"`
	CompletedTasks  []ReportTask  `json:"completed_tasks,omitzero" jsonschema:"Issues resolved or closed during the period"`
	InProgressTasks []ReportTask  `json:"in_progress,omitzero"     jsonschema:"Issues still open or in progress"`
	Metrics         ReportMetrics `json:"metrics,omitzero"         jsonschema:"Summary metrics about the work done"`
	WikiContext     *WikiContext  `json:"wiki_context,omitzero"    jsonschema:"Optional wiki page context for stories/epics"`
}

// ReportTask is a single task within the report, enriched with comments context.
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type ReportTask struct {
	Key          string   `json:"key"                    jsonschema:"Issue key (e.g. TREK-123)"`
	Summary      string   `json:"summary,omitzero"       jsonschema:"Issue title"`
	Description  string   `json:"description,omitzero"   jsonschema:"Issue description"`
	Type         string   `json:"type,omitzero"          jsonschema:"Issue type display name"`
	Status       string   `json:"status,omitzero"        jsonschema:"Issue status display name"`
	Queue        string   `json:"queue,omitzero"         jsonschema:"Queue key"`
	QueueName    string   `json:"queue_name,omitzero"    jsonschema:"Human-readable queue name"`
	Tags         []string `json:"tags,omitzero"          jsonschema:"Issue tags"`
	CreatedAt    string   `json:"created_at,omitzero"    jsonschema:"When the issue was created"`
	UpdatedAt    string   `json:"updated_at,omitzero"    jsonschema:"When the issue was last updated"`
	ResolvedAt   string   `json:"resolved_at,omitzero"   jsonschema:"When the issue was resolved, empty if not"`
	Sprint       string   `json:"sprint,omitzero"        jsonschema:"Sprint name if the issue belongs to one"`
	CommentCount int      `json:"comment_count,omitzero" jsonschema:"Number of comments on the issue"`
	LastComments []string `json:"last_comments,omitzero" jsonschema:"Texts of the last few comments for context"`
}

// ReportMetrics contains summary statistics.
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type ReportMetrics struct {
	TotalIssues     int            `json:"total_issues,omitzero"      jsonschema:"Total number of issues found for the period"`
	CompletedCount  int            `json:"completed_count,omitzero"   jsonschema:"Number of issues resolved or closed"`
	InProgressCount int            `json:"in_progress_count,omitzero" jsonschema:"Number of issues still open"`
	ByType          map[string]int `json:"by_type,omitzero"           jsonschema:"Count of issues by type (task, bug, epic, etc.)"`
	ByQueue         map[string]int `json:"by_queue,omitzero"          jsonschema:"Count of issues by queue"`
	ByMonth         map[string]int `json:"by_month,omitzero"          jsonschema:"Count of resolved issues by month (YYYY-MM)"`
}

// WikiContext holds optional wiki page content for story/epic context.
type WikiContext struct {
	Title   string `json:"title,omitzero"   jsonschema:"Wiki page title"`
	Content string `json:"content,omitzero" jsonschema:"Wiki page body in wiki markup"`
	Slug    string `json:"slug,omitzero"    jsonschema:"Wiki page slug (URL path)"`
}

// --- Create / Update issue request types ---

// CreateIssueRequest is the request for creating a new Tracker issue.
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type CreateIssueRequest struct {
	Summary     string   `json:"summary"              jsonschema:"Issue title (required)"`
	Queue       string   `json:"queue"                jsonschema:"Queue key to create the issue in, e.g. TREK (required)"`
	Description string   `json:"description,omitzero" jsonschema:"Issue description (supports HTML or YFM markup)"`
	Type        string   `json:"type,omitzero"        jsonschema:"Issue type key (e.g. task, bug, epic, subtask)"`
	Priority    string   `json:"priority,omitzero"    jsonschema:"Priority key (e.g. blocker, critical, major, normal, minor, trivial)"`
	Assignee    string   `json:"assignee,omitzero"    jsonschema:"Assignee username or login"`
	Parent      string   `json:"parent,omitzero"      jsonschema:"Parent issue key (e.g. TREK-100)"`
	Tags        []string `json:"tags,omitzero"        jsonschema:"Issue tags"`
	Deadline    string   `json:"deadline,omitzero"    jsonschema:"Deadline in YYYY-MM-DD format"`
	Start       string   `json:"start,omitzero"       jsonschema:"Start date in YYYY-MM-DD format"`
	End         string   `json:"end,omitzero"         jsonschema:"Completion date in YYYY-MM-DD format"`
	Followers   []string `json:"followers,omitzero"   jsonschema:"Follower usernames or IDs"`
}

// UpdateIssueRequest is the request for updating an existing Tracker issue.
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type UpdateIssueRequest struct {
	KeyOrID     string   `json:"key_or_id"            jsonschema:"Issue key (e.g. TREK-123) or numeric ID (required)"`
	Summary     string   `json:"summary,omitzero"     jsonschema:"Updated issue title"`
	Description string   `json:"description,omitzero" jsonschema:"Updated issue description"`
	Type        string   `json:"type,omitzero"        jsonschema:"Updated issue type key (e.g. task, bug, epic, subtask)"`
	Priority    string   `json:"priority,omitzero"    jsonschema:"Updated priority key"`
	Assignee    string   `json:"assignee,omitzero"    jsonschema:"Updated assignee username or login"`
	Parent      string   `json:"parent,omitzero"      jsonschema:"Updated parent issue key"`
	Tags        []string `json:"tags,omitzero"        jsonschema:"Updated issue tags (replaces existing tags)"`
	TagsAdd     []string `json:"tags_add,omitzero"    jsonschema:"Tags to add to the issue"`
	TagsRemove  []string `json:"tags_remove,omitzero" jsonschema:"Tags to remove from the issue"`
	Deadline    string   `json:"deadline,omitzero"    jsonschema:"Updated deadline in YYYY-MM-DD format"`
	Start       string   `json:"start,omitzero"       jsonschema:"Updated start date in YYYY-MM-DD format"`
	End         string   `json:"end,omitzero"         jsonschema:"Updated completion date in YYYY-MM-DD format"`
	Followers   []string `json:"followers,omitzero"   jsonschema:"Updated follower list (replaces existing)"`
	Version     int      `json:"version,omitzero"     jsonschema:"Issue version for conflict resolution. Only applied if it matches the current version."`
}

// CreateCommentRequest is the request for creating a comment on an issue.
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type CreateCommentRequest struct {
	KeyOrID string `json:"key_or_id" jsonschema:"Issue key (e.g. TREK-123) or numeric ID (required)"`
	Text    string `json:"text"      jsonschema:"Comment text (required)"`
}

// UpdateCommentRequest is the request for updating an existing comment.
//
//nolint:lll // struct tags with jsonschema descriptions exceed line length
type UpdateCommentRequest struct {
	KeyOrID   string `json:"key_or_id"  jsonschema:"Issue key (e.g. TREK-123) or numeric ID (required)"`
	CommentID string `json:"comment_id" jsonschema:"Comment ID to update (required). Can be numeric or longId string."`
	Text      string `json:"text"       jsonschema:"Updated comment text (required)"`
}

// CreateIssueResponse is the response for creating an issue.
type CreateIssueResponse struct {
	Issue TrackerIssue `json:"issue" jsonschema:"Created issue details"`
}

// UpdateIssueResponse is the response for updating an issue.
type UpdateIssueResponse struct {
	Issue TrackerIssue `json:"issue" jsonschema:"Updated issue details"`
}

// CreateCommentResponse is the response for creating a comment.
type CreateCommentResponse struct {
	Comment TrackerComment `json:"comment" jsonschema:"Created comment details"`
}

// UpdateCommentResponse is the response for updating a comment.
type UpdateCommentResponse struct {
	Comment TrackerComment `json:"comment" jsonschema:"Updated comment details"`
}
