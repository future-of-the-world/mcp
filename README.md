# MCP Server

![coverage](.coverage/coverage-badge.svg)

A configurable MCP (Model Context Protocol) server that exposes various tools through the Model Context Protocol. It supports multiple transports (stdio, SSE, streamable HTTP) and is configured declaratively from a YAML or JSON file using a `sources:` pipeline.

## Features

- **Multiple transport support** — stdio, SSE, and streamable HTTP
- **YAML and JSON configuration** — flexible config file formats
- **Sources pipeline** — each source is a named connection that exposes one or more MCP tools, dispatched and registered at startup
- **Tool naming and filtering** — per-source `tools.prefix`, `tools.remove`, `tools.enable_only`, and `tools.read_only` adjust the exposed tool set
- **Built-in source types** — HTTP, PostgreSQL, proxy, GitLab, Yandex Tracker/Wiki, English validation, GitHub, Websearch, Woodpecker, Shell, FS, Sequential Thinking, Temporal (30 tools — workflow/activity/schedule lifecycle, query/signal, and batch operations)

## Installation

```bash
go install go.amidman.dev/mcp/cmd/mcp@latest
```

## Usage

```
mcp --config config.yaml [--transport stdio|sse|streamable] [--addr :8080]
```

Flags:

| Flag         | Description                     | Default  |
|--------------|---------------------------------|----------|
| `--config`   | Path to config file (required)  | —        |
| `--transport`| Transport type                  | `stdio`  |
| `--addr`     | Listen address (SSE/streamable) | `:8080`  |

## Configuration

Configuration is written in YAML or JSON. A config file has two parts: a set of top-level identity fields, and a `sources:` map that names each connection.

### Top-level identity

```yaml
name: my-mcp-server
title: "My MCP Server"
version: "1.0.0"
```

`name` and `version` default to `mcp` and `1.0.0` when omitted; `title` is optional.

### Sources

A source is a named connection that exposes one or more MCP tools. Each key under `sources:` is the source name you choose; the value carries the `type`, a `connect:` map (type-specific), and an optional `tools:` block.

```yaml
sources:
  forgejo:
    type: proxy
    connect:
      command: forgejo-mcp-server
    tools:
      prefix: forgejo
      remove:
        - "branch_protection"
```

The server connects each source at startup (via the per-type `Connect` implementation), applies the `tools:` adjustments, and registers the surviving tools.

### Top-level `before` section

Some setups need operator-controlled shell wiring (typically `kubectl port-forward` to expose cluster-internal services) to be in place before any source connects. The optional top-level `before:` list declares those commands. Each entry is a long-lived `sh -c` invocation spawned in parallel; the server start proceeds to `source.Apply` only after every declared healthcheck has finished (passed, failed, or timed out).

```yaml
before:
  - command: "kubectl port-forward svc/postgres-primary 5432:5432 -n primary"
    healthcheck:
      tcp: "127.0.0.1:5432"
      interval: 500ms
      timeout: 10s
  - command: "kubectl port-forward svc/postgres-events 5433:5432 -n events"
    healthcheck:
      tcp: "127.0.0.1:5433"
```

`before` fields:

| Field        | Description                                                        | Required |
|--------------|--------------------------------------------------------------------|----------|
| `command`    | Shell command string passed to `sh -c`                              | Yes      |
| `healthcheck`| Optional readiness probe (see below)                               | No       |

`healthcheck` fields:

| Field     | Description                                          | Required | Default |
|-----------|------------------------------------------------------|----------|---------|
| `tcp`     | `host:port` for the TCP-readiness probe              | Yes      | —       |
| `interval`| Per-attempt deadline and retry delay                 | No       | `1s`    |
| `timeout` | Overall healthcheck budget                           | No       | `30s`   |

**Failure semantics.** Spawn failures, non-zero exits, and timed-out healthchecks are all logged at `ERROR`. Nothing aborts the server: even when every healthcheck times out, `source.Apply` still runs through the **common path**, so any source whose dependencies happen to be reachable connects normally.

**Lifetime and cleanup.** The spawned `sh -c` processes live for the entire server lifetime. When the server receives `SIGINT` or `SIGTERM`, the `signal.NotifyContext` cancellation propagates through `exec.CommandContext` and kills every child.

**Parallel-by-default.** All commands and all healthchecks run in parallel — `shell.RunBefore`'s wall time is bounded by the slowest healthcheck, never the sum.

A `before:` entry without `healthcheck` is fine: the spawned command is fire-and-forget, and `RunBefore` does not block on it (the entry's purpose is operator boot wiring, not readiness gating).

#### Restart policy

Long-lived before-commands like `kubectl port-forward` can die during the server's lifetime — the upstream pod restarts, the network blips, the port-forward session drops. By default, a single failure ends the entry for the rest of the server's lifetime. The optional `restart:` block on each entry changes that: a non-signal-error exit triggers an automatic respawn of the same command.

```yaml
before:
  # Most common pattern: keep the tunnel alive forever, no cap.
  - command: "kubectl port-forward svc/postgres-primary 5432:5432 -n primary"
    healthcheck:
      tcp: "127.0.0.1:5432"
    # restart omitted → MaxAttempts=0 → restart forever, 5s delay
  - command: "kubectl port-forward svc/postgres-events 5433:5432 -n events"
    healthcheck:
      tcp: "127.0.0.1:5433"
    restart:
      delay: 2s               # explicit delay; still no cap
  - command: "some-flaky-tunnel"
    restart:
      max_attempts: 5         # give up after 5 total attempts
      delay: 10s
```

`restart` fields:

| Field          | Description                                                                  | Required | Default |
|----------------|------------------------------------------------------------------------------|----------|---------|
| `max_attempts` | Total spawn attempts including the first; `0` (the default) means **unbounded** | No       | `0`     |
| `delay`        | Wait between attempts; honors server-ctx cancellation (SIGINT/SIGTERM)       | No       | `5s`    |

**`max_attempts` semantics:**

| Value                       | Meaning                                                      |
|-----------------------------|--------------------------------------------------------------|
| `restart:` omitted          | No policy. Spawn exactly once, current behavior.             |
| `restart: {}` (empty) or `max_attempts: 0` | **Restart forever** until ctx cancels or clean exit. |
| `max_attempts: 1`           | Run once; never restart.                                     |
| `max_attempts: N` (N ≥ 2)   | Up to N total spawn attempts, then ERROR "restart budget exhausted". |

**Restart triggers** (only on these conditions):

- The spawn exited non-zero **and** was not terminated by a signal
- The server context has not been cancelled

**Restart does NOT trigger** on:

- Clean exit (`exit 0`) — the operator can wrap the command in `while true; do ...; done` if they want infinite lifetime
- Signal-terminated exit — that's the server's SIGINT/SIGTERM cleanup path
- Server context already cancelled — function returns immediately, no respawn

**Healthcheck is not re-run on restart.** The healthcheck is a one-shot startup readiness gate; restart is a separate "keep alive" concern. Re-verifying readiness post-restart can be added as a follow-up feature.

**Logging.** Each respawn logs at `INFO` with `index`, `command`, `attempt`, `delay`. Bounded exhaustion (when `max_attempts` is hit) logs at `ERROR` with `attempts`, `max_attempts`, and the last `error`.

### Tool naming (`tools.prefix`)

Every tool a source exposes is prefixed before registration. The effective prefix is `tools.prefix` exactly as set: there is no source-name fallback. Omit `tools.prefix` to leave tool names unchanged; set it explicitly to namespace tools across sources or to shorten long upstream names.

The prefix is concatenated to the tool's base name without a separator, so include any desired separator (e.g. `_`) in the prefix itself.

For example, an `english` source named `grammar` exposes a base tool `validate_english`; with no `tools.prefix` it stays `validate_english`. Setting `tools.prefix: en_` yields `en_validate_english`.

### Duplicate tool names are a config error

Tool names are unique across the server. If two sources (or two tools within one source) end up with the same name, `source.Apply` returns an error like:

```
source "users": duplicate tool name "http" (already registered by source "weather")
```

and the server fails to start with **no** tools registered. The MCP Go SDK silently replaces a tool of the same name on `AddTool`; the apply step surfaces this as a hard config error so the conflict is visible at startup, not at the first call to the wrong tool. To resolve, either set distinct `tools.prefix` values on the colliding sources or remove one of them.

### Tool filtering (`tools.remove`)

`tools.remove` is a list of regex patterns. Each pattern is matched against the tool's **final, prefixed** name; matching tools are dropped. Patterns are applied after prefixing, so author them against the public-facing names. A pattern that fails to compile is ignored.

```yaml
sources:
  forgejo:
    type: proxy
    connect:
      command: forgejo-mcp-server
    tools:
      remove:
        - "^forgejo_branch_protection"
```

### Tool whitelisting (`tools.enable_only`)

`tools.enable_only` is the **whitelist mirror** of `tools.remove`: a list of regex patterns matched against the tool's final, prefixed name. Tools matching **any** pattern are kept; tools that match none are dropped. The intended use is to expose a small, explicit set of upstream tools when the default tool set is too broad to safely hand to the LLM.

`tools.enable_only` and `tools.remove` are **mutually exclusive** on the same source. Setting both causes `LoadSources` to return an error of the form `source "<name>": tools.remove and tools.enable_only are mutually exclusive` and the server fails to start — the two fields have opposite intent (drop matching vs keep matching only), and combining them produces ambiguous behavior on tools that match only one of the two pattern sets. See [Tool filtering (`tools.remove`)](#tool-filtering-toolsremove) for the corresponding blacklist contract.

As with `tools.remove`, a pattern that fails to compile is ignored (not fatal) — bad regex does not abort server startup.

Middleware ordering. The full chain is `applyReadOnly → applyPrefix → applyRemove → applyEnableOnly`, so `enable_only` operates on the post-`prefix`, post-`remove` name. Author patterns against the public-facing prefixed names (the same convention as `tools.remove`):

```yaml
sources:
  forgejo:
    type: proxy
    connect:
      command: forgejo-mcp-server
    tools:
      prefix: forgejo_
      enable_only:
        - "^forgejo_(repo|issue|pull_request)_"
        # every other forgejo_* tool is dropped
```

A tool whose embedded `*mcp.Tool` is `nil` has no public name to whitelist against and is dropped (the opposite of `tools.remove`, which keeps such entries because "no match" means "keep" in a remove-only world).

### Read-only filtering (`tools.read_only`)

When `tools.read_only` is `true`, the source exposes only tools whose embedded `*mcp.Tool.Annotations.ReadOnlyHint == true`. The flag is consumed by the per-type implementer: each `Connect` sets `ReadOnlyHint` on the tools it returns (the english, sequentialthinking, fs, and HTTP-GET sources mark all their tools read-only; the http, postgres, shell, and tracker sources mix read-only and mutating tools).

The filter runs before `tools.prefix` and `tools.remove`, so a `remove` pattern written against the prefixed name only sees the read-only survivors.

If the filter yields zero tools, the apply succeeds (no tools are registered for that source) and a warning is logged identifying the source. This is a likely configuration mistake — the user asked for read-only tools but the per-type `Connect` did not produce any — so it surfaces in logs without aborting server startup.

A tool whose embedded `*mcp.Tool.Annotations` is `nil` is treated as "unknown" and dropped under `read_only: true`. The implementer signals "I don't know whether this tool is read-only" by leaving `Annotations` nil; the dispatcher honors that signal.

```yaml
sources:
  gitlab_readonly:
    type: gitlab
    connect:
      token: ${GITLAB_TOKEN}
    tools:
      read_only: true
```

### Full example

```yaml
name: my-mcp-server
title: "My MCP Server"
version: "1.0.0"

before:
  - command: "kubectl port-forward svc/postgres-primary 5432:5432 -n primary"
    healthcheck:
      tcp: "127.0.0.1:5432"
      interval: 500ms
      timeout: 10s

sources:
  maindb:
    type: postgres
    connect:
      datasource: "postgres://user:pass@localhost:5432/mydb?sslmode=disable"

  github:
    type: github
    connect:
      token: "ghp_xxxxxxxxxxxx"
      # host: github.com (default; or "github.example.com" for GitHub Enterprise)
      # toolsets: [repos, issues, pull_requests, ...] (optional; default is all)
      # read_only: false (optional; restrict to read-only tools)

  # Whitelist a narrow slice of upstream tools via enable_only:
  # every tracker_* tool NOT matching the patterns below is dropped.
  work_focused:
    type: tracker
    connect:
      token: "your-oauth-token"
      org_id: "your-org-id"
    tools:
      prefix: tracker_
      enable_only:
        - "^tracker_(get_issues|get_issue|search_issues)$"

  remote:
    type: proxy
    connect:
      url: https://mcp.example.com/mcp
      headers:
        Authorization: "Bearer token123"
      transport: sse

  work:
    type: tracker
    connect:
      token: "your-oauth-token"
      org_id: "your-org-id"

  checker:
    type: english

  myproject:
    type: gitlab
    connect:
      token: "glpat-xxxxxxxxxxxx"

  search:
    type: websearch
    connect:
      brave_api_key_env: BRAVE_API_KEY
      max_results: 10
      timeout: 10s

  weather:
    type: http
    connect:
      url: https://api.weather.example.com/v1/forecast
      method: GET
      description: Get weather forecast for a location
      headers:
        Accept: application/json

  ci:
    type: woodpecker
    connect:
      token: "ci_xxxxxxxxxxxx"
      api_url: "https://ci.example.com/api"
    tools:
      prefix: wp_

  ops:
    type: shell
    connect:
      working_dir: /home/user/project
      shell: /bin/zsh
      shell_flags: ["-lic"]
      env:
        HOME: /home/user

  project_files:
    type: fs
    connect:
      allowed_paths:
        - /home/user/project

  thinker:
    type: sequentialthinking

  workflows:
    type: temporal
    connect:
      host: my-cluster.tmprl.cloud:7233
      namespace: production
      api_key: "${TEMPORAL_API_KEY}"
    tools:
      prefix: t1
```

## Source Types

| Type       | Description                                                                                                                              |
|------------|------------------------------------------------------------------------------------------------------------------------------------------|
| HTTP       | Forwards requests to HTTP endpoints with configurable method, headers, and body.                                                         |
| PostgreSQL | Database introspection tools — list schemas, tables, execute read-only queries, and get detailed table info with recursive FK traversal. |
| Proxy      | Proxies tools from another MCP server (HTTP or stdio). Supports custom environment variables for child processes.                        |
| Tracker    | Yandex Tracker and Wiki API tools — CRUD on issues and comments, wiki pages, and retrospective reports.                                  |
| GitLab     | GitLab API tools — fetch merge request discussions (comments, review threads, system notes) and commits.                                 |
| GitHub     | Official GitHub MCP server embedded in-process (single binary, no external `github-mcp-server` needed).                                  |
| English    | English grammar and spelling validation via LanguageTool API.                                                                            |
| Websearch  | Web search (Brave, DuckDuckGo), news search, image search, URL content extraction, and provider listing.                                 |
| Woodpecker | Woodpecker CI/CD management — list repositories, inspect and manage pipeline runs, and read step logs directly via MCP.                |
| Shell      | Executes shell commands via `sh -c` and returns stdout, stderr, and exit code. A drop-in replacement for the host's built-in shell tool.  |
| FS         | Path-confined file operations (read, write, edit, list, move, copy, delete, search, stat) gated by an operator allowlist.             |
| Sequential Thinking | Dynamic, reflective problem-solving tool — break problems into steps, revise earlier thoughts, and branch into alternative reasoning paths. Go port of `@modelcontextprotocol/server-sequential-thinking`. |
| Temporal    | Temporal workflow orchestration — 30 tools covering workflow lifecycle, standalone activity lifecycle, schedule management, query/signal, and batch operations (signal/cancel/terminate with bounded concurrency). Supports mTLS and API-key auth for Temporal Cloud.                            |

### HTTP Source

Forwards MCP tool calls as HTTP requests to any HTTP endpoint. Supports all standard HTTP methods, configurable headers, query parameters, JSON bodies, and form data. The source exposes a single tool named `http`; with no `tools.prefix` override it stays `http`.

**`connect` fields:**

| Field         | Description                                          | Required |
|---------------|------------------------------------------------------|----------|
| `url`         | Target HTTP endpoint URL                             | Yes      |
| `method`      | HTTP method (`GET`, `POST`, `PUT`, `DELETE`, etc.)   | Yes      |
| `description` | Tool description exposed to the LLM                  | No       |
| `headers`     | Default HTTP headers sent with every request         | No       |

**Example:**

```yaml
sources:
  weather:
    type: http
    connect:
      url: https://api.weather.example.com/v1/forecast
      method: GET
      description: Get weather forecast for a location
      headers:
        Accept: application/json
        X-API-Key: "your-api-key"
```

### PostgreSQL Source

Database introspection tools for PostgreSQL. Connects using the `pgx` driver and exposes tools for listing schemas/tables, executing **read-only** SQL queries, and fetching detailed table information with recursive foreign key traversal. The connection is opened (and validated) at startup.

**`connect` fields:**

| Field        | Description                                                | Required |
|--------------|------------------------------------------------------------|----------|
| `datasource` | PostgreSQL connection string (pgx)                         | Yes      |

**Example:**

```yaml
sources:
  mydb:
    type: postgres
    connect:
      datasource: "postgres://user:pass@localhost:5432/mydb?sslmode=disable"
```

**Exposed tools** (base names; the `tools.prefix`, when set, is prepended at registration):

| Base tool        | Description                                                                                                  |
|------------------|--------------------------------------------------------------------------------------------------------------|
| `list_schemas`   | List all schemas in the database                                                                             |
| `list_tables`    | List all tables in a specific schema                                                                         |
| `execute_query`  | Execute a read-only SQL query with optional prepared-statement parameters                                    |
| `get_table_info` | Get comprehensive table info: columns, indexes, constraints, comments, and recursive FK relationships        |

### Proxy Source

The proxy source forwards tools from another MCP server. It supports two connection modes:

- **HTTP mode** — connect to a remote MCP server via streamable HTTP (default) or SSE transport.
- **Command (stdio) mode** — launch a local MCP server binary and communicate over stdio.

**`connect` fields:**

| Field       | Description                                                | Required (one of) |
|-------------|------------------------------------------------------------|--------------------|
| `url`       | Remote MCP server URL (HTTP mode)                          | `url` or `command` |
| `headers`   | HTTP headers sent with every request (HTTP mode)           | No                 |
| `transport` | HTTP transport: `streamable` (default) or `sse`            | No                 |
| `command`   | Path to local MCP server binary (stdio mode)               | `url` or `command` |
| `args`      | Arguments passed to the command                            | No                 |
| `env`       | Additional environment variables for the child process     | No                 |

The `env` map is merged with the parent process environment, so the child process inherits all existing env vars plus the ones you specify. Upstream tool names are preserved verbatim; only the source prefix is prepended.

**HTTP mode example:**

```yaml
sources:
  remote:
    type: proxy
    connect:
      url: https://mcp.example.com/mcp
      headers:
        Authorization: "Bearer token123"
      transport: streamable  # "streamable" (default) or "sse"
```

**Command (stdio) mode example with environment variables** — for users who prefer to run their own `github-mcp-server` binary:

```yaml
sources:
  github:
    type: proxy
    connect:
      command: github-mcp-server
      args:
        - stdio
      env:
        GITHUB_PERSONAL_ACCESS_TOKEN: "ghp_xxxxxxxxxxxx"
```

The native `type: github` source is the recommended path (see [GitHub Source](#github-source) below); the proxy example above is a fallback for users who want to pin their own `github-mcp-server` version.

### GitHub Source

The GitHub source embeds the official [github.com/github/github-mcp-server](https://github.com/github/github-mcp-server) module in-process. There is no separate `github-mcp-server` binary to install — the upstream's tool inventory is loaded into the parent server at startup, and tool calls are forwarded through an in-memory MCP transport.

The upstream's module is pinned to `v1.3.1-0.20260617160418-4f73cfd1db14` (commit `4f73cfd1...` from 2026-06-17). To upgrade, bump the version in `go.mod`.

**`connect` fields:**

| Field       | Description                                                            | Required | Default          |
|-------------|------------------------------------------------------------------------|----------|------------------|
| `token`     | GitHub personal access token (`ghp_...`, fine-grained, or GitHub App)  | Yes      | —                |
| `host`      | GitHub host (e.g. `github.com` or `github.example.com` for Enterprise)  | No       | `github.com`     |
| `toolsets`  | List of upstream toolset names to enable (e.g. `repos`, `issues`)      | No       | All toolsets     |
| `read_only` | Restrict the upstream to read-only tools                               | No       | `false`          |

**Basic example** (GitHub.com, all toolsets, read+write):

```yaml
sources:
  github:
    type: github
    connect:
      token: "ghp_xxxxxxxxxxxx"
```

**GitHub Enterprise example** (specific host, restricted toolset, read-only):

```yaml
sources:
  github:
    type: github
    connect:
      token: "ghp_xxxxxxxxxxxx"
      host: "github.example.com"
      toolsets:
        - repos
        - pull_requests
      read_only: true
```

The upstream's tool names (e.g. `create_or_update_file`, `search_repositories`, `get_me`) are passed through unchanged. Set `tools.prefix` in the source config to namespace them — for example, `tools: { prefix: "github_" }` renames `get_me` to `github_get_me`.

### Tracker Source

Yandex Tracker and Wiki API tools — search issues, get details and comments, create and update issues and comments, read wiki pages, get issue links, and generate retrospective reports.

**Authentication:** Two options are supported:
1. **Direct OAuth token** — set `token` and `org_id`.
2. **OAuth device flow** — set `client_id`, `client_secret`, and `org_id`. On startup, the source guides you through the Yandex OAuth device authorization flow automatically.

**`connect` fields:**

| Field              | Description                                             | Required                |
|--------------------|---------------------------------------------------------|-------------------------|
| `token`            | OAuth token for Yandex Tracker                          | Yes* (or OAuth flow)    |
| `client_id`        | OAuth application client ID (for device flow)           | Yes* (or direct token)  |
| `client_secret`    | OAuth application client secret (for device flow)       | Yes* (with `client_id`) |
| `org_id`           | Organization ID for Yandex Tracker                      | Yes                     |
| `base_url`         | Tracker API base URL                                    | No                      |
| `wiki_base_url`    | Wiki API base URL                                       | No                      |
| `oauth_token_url`  | OAuth token endpoint URL                                | No                      |
| `oauth_device_url` | OAuth device code endpoint URL                          | No                      |
| `cloud_org`        | Use `X-Cloud-Org-ID` header instead of `X-Org-ID`       | No                      |

**Direct token example:**

```yaml
sources:
  work:
    type: tracker
    connect:
      token: "your-oauth-token"
      org_id: "your-org-id"
```

**OAuth device flow example:**

```yaml
sources:
  work:
    type: tracker
    connect:
      client_id: "your-client-id"
      client_secret: "your-client-secret"
      org_id: "your-org-id"
```

**Exposed tools** (base names; the `tools.prefix`, when set, is prepended at registration):

| Base tool           | Description                                                                               |
|---------------------|-------------------------------------------------------------------------------------------|
| `search_issues`     | Search issues by queue, filter, or query language                                         |
| `get_issue`         | Get detailed issue info by key or ID                                                      |
| `get_links`         | Get all links (relations) for an issue                                                    |
| `get_comments`      | Get all comments for an issue                                                             |
| `get_fields`        | Get available Tracker issue fields (for building search queries)                          |
| `get_wiki_page`     | Get a Wiki page by slug or numeric ID                                                     |
| `get_wiki_subpages` | List subpages of a Wiki page                                                              |
| `list_queues`       | List available queues                                                                      |
| `my_report`         | Generate a structured retrospective report for a user in a given time period              |
| `create_issue`      | Create a new issue (task, bug, epic, etc.) in a queue                                     |
| `update_issue`      | Update an existing issue — only provided fields are changed                               |
| `create_comment`    | Create a new comment on an issue                                                          |
| `update_comment`    | Update the text of an existing comment                                                    |

### GitLab Source

GitLab API tools — fetch merge request discussions (comments, review threads, system notes) and commits. Accepts full MR URLs and supports self-hosted GitLab instances.

**`connect` fields:**

| Field      | Description                                            | Required |
|------------|--------------------------------------------------------|----------|
| `token`    | GitLab private token or personal access token           | Yes      |
| `base_url` | GitLab API base URL (optional, defaults to gitlab.com) | No       |

**GitLab.com example:**

```yaml
sources:
  myproject:
    type: gitlab
    connect:
      token: "glpat-xxxxxxxxxxxx"
```

**Self-hosted GitLab example:**

```yaml
sources:
  selfhosted:
    type: gitlab
    connect:
      token: "glpat-xxxxxxxxxxxx"
      base_url: "https://gitlab.example.com"
```

**Exposed tools** (base names; the `tools.prefix`, when set, is prepended at registration):

| Base tool             | Description                                                                                             |
|-----------------------|---------------------------------------------------------------------------------------------------------|
| `get_mr_discussions`  | Fetch all discussion threads (comments, review threads, system notes) for a merge request by full URL   |
| `get_mr_commits`      | Fetch all commits in a merge request by full URL                                                        |

### English Source

English grammar and spelling validation via the [LanguageTool](https://languagetool.org/) API. Strips code blocks and URLs before checking, filters style suggestions, and returns structured errors with corrections.

**`connect` fields:**

| Field        | Description                  | Default                           |
|--------------|------------------------------|-----------------------------------|
| `language`   | Language code for validation | `en-US`                           |
| `api_url`    | LanguageTool API base URL    | `https://api.languagetool.org/v2` |

**Example (use defaults):**

```yaml
sources:
  checker:
    type: english
```

**With a custom LanguageTool server:**

```yaml
sources:
  checker:
    type: english
    connect:
      api_url: "http://localhost:8010/v2"
      language: "en-GB"
```

**Exposed tool** (base name; the `tools.prefix`, when set, is prepended at registration):

| Base tool          | Description                                                              |
|--------------------|--------------------------------------------------------------------------|
| `validate_english` | Validate English text for grammar, spelling, punctuation, and vocabulary |

### Websearch Source

Web search and URL content extraction powered by [Brave Search](https://brave.com/search/api/) and [DuckDuckGo](https://duckduckgo.com/). Supports web, news, and image search, as well as fetching and extracting readable content from URLs.

The Brave API key is resolved from an environment variable at startup. If `brave_api_key_env` is set, the source validates that the variable exists and fails fast if it doesn't. If unset, it defaults to `BRAVE_API_KEY` and reads whatever value is present (empty is allowed — DuckDuckGo works without a key).

**`connect` fields:**

| Field              | Description                                          | Default        |
|--------------------|------------------------------------------------------|----------------|
| `brave_api_key_env`| Environment variable name holding the Brave API key  | `BRAVE_API_KEY`|
| `max_results`      | Default number of results per search                 | `10`           |
| `timeout`          | HTTP client timeout as a duration string (e.g. `10s`)| `10s`          |

**Example:**

```yaml
sources:
  search:
    type: websearch
    connect:
      brave_api_key_env: BRAVE_API_KEY
      max_results: 10
      timeout: 10s
```

**Exposed tools** (base names; the `tools.prefix`, when set, is prepended at registration):

| Base tool        | Description                                                              |
|------------------|--------------------------------------------------------------------------|
| `web_search`     | Search the web — supports provider selection, pagination, domain filters |
| `news_search`    | Search for news articles with freshness and country filters              |
| `image_search`   | Search for images with safe-search controls                              |
| `fetch_url`      | Fetch a URL and extract readable text, links, and metadata               |
| `list_providers` | List available search providers and the current default                  |

### Woodpecker Source

Woodpecker CI/CD management — list repositories, inspect and manage pipeline runs, and read step logs directly via MCP (no copy-pasting CI output into the session).

**`connect` fields:**

| Field     | Description                                                              | Required | Default |
|-----------|--------------------------------------------------------------------------|----------|---------|
| `token`   | Woodpecker personal access token                                         | Yes      | —       |
| `api_url` | Woodpecker API base URL (e.g. `https://ci.example.com/api`)              | Yes      | —       |

There is no default for `api_url` — the source refuses to start without it, so the user is always explicit about which Woodpecker instance the tools will talk to.

**Example:**

```yaml
sources:
  ci:
    type: woodpecker
    connect:
      token: "ci_xxxxxxxxxxxx"
      api_url: "https://ci.example.com/api"
```

**Headline use case — investigate a failed pipeline without copy-pasting logs:**

```yaml
sources:
  ci:
    type: woodpecker
    connect:
      token: "ci_xxxxxxxxxxxx"
      api_url: "https://ci.example.com/api"
    tools:
      prefix: "wp_"   # optional; renames list_repos → wp_list_repos, etc.
```

The model can then walk `wp_list_repos` → `wp_list_pipelines({status: "failure"})` → `wp_get_pipeline` → `wp_get_step_logs` to surface the failure logs directly, then either `wp_restart_pipeline` (if flaky) or report back to the user.

**Exposed tools** (base names; the `tools.prefix`, when set, is prepended at registration):

| Base tool             | Description                                                                  | Read-only |
|-----------------------|------------------------------------------------------------------------------|-----------|
| `list_repos`          | List repositories the token can see; discover `repo_id` from `full_name`     | yes       |
| `list_pipelines`      | List pipelines for a repository; filter by `status`, `branch`, `event`        | yes       |
| `get_pipeline`        | Get one pipeline including `workflows[].children[]` (steps)                  | yes       |
| `get_step_logs`       | Fetch decoded log entries for a step — UTF-8 `text` + `kind` per entry        | yes       |
| `restart_pipeline`    | Restart an existing pipeline; optional `event` and `deploy_to` overrides    | no        |
| `launch_pipeline`     | Trigger a new manual pipeline; optional `branch` and `variables`             | no        |
| `cancel_pipeline`     | Cancel a running pipeline; idempotent                                        | no        |

### Shell Source

The source exposes one tool:

| Base tool       | Description                                                              |
|-----------------|--------------------------------------------------------------------------|
| `run_command`   | Execute a shell command string and return its captured output            |

**`connect` fields:**

| Field              | Description                                                          | Required | Default       |
|--------------------|----------------------------------------------------------------------|----------|---------------|
| `working_dir`      | Working directory for every invocation                               | Yes      | —             |
| `timeout`          | Per-call timeout applied when the per-call `timeout` is absent      | No       | `30s`         |
| `max_output_bytes` | Cap on combined stdout+stderr bytes per invocation                 | No       | `1048576`     |
| `shell`            | Absolute path to the shell binary                                     | No       | `/bin/sh`     |
| `shell_flags`      | Argv flags inserted between shell and command (e.g. `["-lic"]`)      | No       | `["-c"]`      |
| `env`              | Explicit env var map passed to every invocation                    | No       | `{}`          |

The argv is always `[shell, shell_flags..., command]`. The default `shell_flags: ["-c"]` is the POSIX command-string convention; operators who want rc-file sourcing override it (see examples below).

**Example — strict default (no rc sourcing):**

```yaml
sources:
  shell:
    type: shell
    connect:
      working_dir: /home/user
      timeout: 30s
      max_output_bytes: 1048576
      env:
        LANG: C.UTF-8
        PATH: /usr/local/bin:/usr/bin:/bin
        HOME: /home/user
```

**Example — zsh with `.zshrc` sourced** (login + interactive + command; sources `.zprofile` then `.zshrc`):

```yaml
sources:
  shell:
    type: shell
    connect:
      shell: /bin/zsh
      shell_flags: ["-lic"]
      working_dir: /home/user
      env:
        HOME: /home/user
```

**Example — bash with `.bash_profile` sourced:**

```yaml
connect:
  shell: /bin/bash
  shell_flags: ["-l"]
```

**Per-call overrides** (`directory` is required; `timeout` and `env` are optional):

| Field       | Required | Description                                                  |
|-------------|----------|--------------------------------------------------------------|
| `directory` | **Yes**  | Absolute path inside `connect.working_dir` to chdir to before running the command. Must exist and be a directory. Required on every call — empty or omitted values are rejected with a tool error. Non-absolute paths, paths outside `working_dir`, missing paths, and paths reached through a symlink that points outside `working_dir` are rejected with a tool error. |
| `timeout`   | No       | Override the source's per-call timeout (e.g. `'5m'`, `'30s'`)|
| `env`       | No       | Map of per-call env overrides; merged on top of `connect.env`, per-call values win on key collision |

**Security model.** This source can execute arbitrary code. The MVP trusts the operator (same trust model as the `postgres` source for SQL) and the LLM (same trust model as the host's built-in shell tool). The guardrails narrow the blast radius but do not eliminate it:

- The command runs as `<shell> [shell_flags...] <command>` — the meta-server does not parse the flags, it passes them through to `exec.CommandContext` as-is. With the default `shell_flags: ["-c"]`, `sh` itself performs all shell syntax the LLM writes. With `shell_flags: ["-lic"]`, the shell sources rc files first, which can change which executables and aliases are available.
- The per-call `directory` is **confined to `connect.working_dir`** via `os.Root`. The meta-server opens an `os.Root` at the canonical working directory once at startup, and every `directory` request is validated against it: missing or empty values, paths outside the root, non-absolute paths, paths the LLM does not have permission to traverse, and paths reached only via a symlink that points outside the root are rejected with a tool error before the child is spawned. The field is also declared as required in the input schema so the MCP host enforces its presence before arguments reach the handler. The validation is backed by `openat2(RESOLVE_BENEATH)` on Linux (and equivalents on macOS / Windows) so symlink attacks cannot escape. `os.Root` does not protect against traversal across filesystem boundaries (bind mounts, `/proc`), access to Unix device files, or `..` across mount boundaries — operators who mount attacker-controlled content inside `working_dir` have already crossed the trust boundary.
- `connect.env` is the **only** source of env vars passed to the child. The child process does **not** inherit the parent process's environment. Operators who want `PATH` must declare it. (Note: when `shell_flags` enables rc-file sourcing, the rc files can set additional env vars; this is the documented behavior of the chosen shell.)
- No stdin is opened. The child process's stdin is `/dev/null`.
- No PTY; output is plain pipes.
- A hard timeout kills the child on expiry; the result reports `exit_code: -1` and a wrapped error.
- Output is capped per `connect.max_output_bytes`; past the cap, output is dropped and `truncated: true` is set.
- Non-UTF-8 output is base64-encoded and prefixed with `b64:` (same convention as the woodpecker source's step logs).
- The tool advertises `DestructiveHint=true` and `ReadOnlyHint=false` so hosts that gate on these hints treat shell correctly as write/destructive.

**Output schema:**

```json
{
  "stdout":      "captured stdout (UTF-8 text, or 'b64:<...>' for binary)",
  "stderr":      "captured stderr (UTF-8 text, or 'b64:<...>' for binary)",
  "exit_code":   0,
  "duration_ms": 42,
  "truncated":   false
}
```

`exit_code` is `-1` when the process was killed by a signal or by the timeout (and a tool error is returned alongside).

### FS Source

Exposes twelve file-operation tools gated by an operator-configured `allowed_paths` allowlist. Every tool argument that names a path is routed through a resolver that rejects `..` traversal, symlink escapes, and any path that does not normalize to a child of one of the configured roots. The LLM cannot escape the allowlist regardless of how it phrases a path.

The source exposes the following tools (with no `tools.prefix` override):

| Base tool                   | Description                                                                                                          |
|-----------------------------|----------------------------------------------------------------------------------------------------------------------|
| `list_allowed_directories`  | Return the operator-configured allowlist                                                                             |
| `read_file`                 | Read a file (UTF-8 text or `b64:<base64>` for binary)                                                                |
| `write_file`                | Create or overwrite a file; `encoding: "base64"` for binary                                                          |
| `edit_file`                 | Find/replace edit, rejected unless `old_text` occurs exactly once                                                     |
| `create_directory`          | `mkdir -p` semantics                                                                                                |
| `list_directory`            | List immediate children                                                                                             |
| `directory_tree`            | Recursive JSON tree (depth bounded by `max_depth`, default 8)                                                        |
| `move_file`                 | Rename or move within the allowlist                                                                                 |
| `copy_file`                 | Copy a regular file (directories refused)                                                                            |
| `delete_file`               | Remove a file or empty directory (non-empty directories refused)                                                    |
| `search_files`              | Recursive glob-style match within a root                                                                             |
| `get_file_info`             | Stat a path (size, mode, mtime, is_dir)                                                                             |
| `grep`                      | Regex content search (literal prefilter, .gitignore honoring, default-ignore list)                                  |

**`connect` fields:**

| Field             | Description                                                       | Required | Default     |
|-------------------|-------------------------------------------------------------------|----------|-------------|
| `allowed_paths`   | Absolute paths the LLM is permitted to read, write, or delete      | Yes      | —           |
| `max_read_bytes`  | Hard cap on file size returned by `read_file`                     | No       | `1048576`   |
| `max_write_bytes` | Hard cap on size of content written by `write_file` / `edit_file` | No       | `10485760`  |
| `follow_symlinks` | Follow symlinks during path resolution (default: deny)             | No       | `false`     |

**Example:**

```yaml
sources:
  work:
    type: fs
    connect:
      allowed_paths:
        - /home/user/projects
        - /tmp/work
      max_read_bytes: 524288
      max_write_bytes: 5242880
```

**Security note:** the operator's allowlist is the contract. The source never executes subprocesses, never reads `/proc`, never touches the network; the only side effects are file mutations, all gated by `allowed_paths`. Use `follow_symlinks: false` (the default) to reject any symlink under the allowlist that points outside; permissive mode follows symlinks but rejects any post-follow real path that exits the allowlist.

### Sequential Thinking Source

Exposes a single `sequentialthinking` tool for dynamic, reflective problem-solving. The LLM breaks a problem into numbered thoughts, can revise earlier thoughts (re-running a step with `is_revision: true`), and can branch into alternative reasoning paths (using `branch_from_thought` + `branch_id`). The meta-server keeps the in-memory thought history per source instance and returns the current `thought_number`, `total_thoughts`, `next_thought_needed`, the sorted set of active branch IDs, and the running history length on every call.

The source exposes one tool:

| Base tool             | Description                                                                                          |
|-----------------------|------------------------------------------------------------------------------------------------------|
| `sequentialthinking`  | Record one step of a multi-step reasoning process; supports revisions and branches. Read-only, idempotent. |

This is a Go port of the upstream [`@modelcontextprotocol/server-sequential-thinking`](https://github.com/modelcontextprotocol/servers/tree/main/src/sequentialthinking) (MIT, modelcontextprotocol org). The tool name, input/output schema, and annotations match upstream 1:1 so existing client code, docs, and prompts keep working.

**`connect` fields:**

| Field                     | Description                                            | Required | Default |
|---------------------------|--------------------------------------------------------|----------|---------|
| `disable_thought_logging` | Suppress the per-thought operator log line              | No       | `false` |

**Example:**

```yaml
sources:
  think:
    type: sequentialthinking
    connect:
      disable_thought_logging: false
```

**Input schema** (mirrors upstream's Zod schema):

| Field                | Type      | Required | Description                                                |
|----------------------|-----------|----------|------------------------------------------------------------|
| `thought`            | string    | Yes      | The current thinking step                                  |
| `next_thought_needed`| boolean   | Yes      | Whether another thought step is needed                     |
| `thought_number`     | integer   | Yes      | Current thought number (≥ 1)                               |
| `total_thoughts`     | integer   | Yes      | Estimated total thoughts needed (≥ 1); the tool bumps this up if `thought_number` ever exceeds it |
| `is_revision`        | boolean   | No       | Whether this revises previous thinking                     |
| `revises_thought`    | integer   | No       | Which thought number is being reconsidered (≥ 1)          |
| `branch_from_thought`| integer   | No       | Branching point thought number (≥ 1)                      |
| `branch_id`          | string    | No       | Branch identifier                                          |
| `needs_more_thoughts`| boolean   | No       | If more thoughts are needed                                |

**Output schema:**

| Field                   | Type    | Description                                            |
|-------------------------|---------|--------------------------------------------------------|
| `thought_number`        | integer | The (possibly bumped) thought number for this call     |
| `total_thoughts`        | integer | The (possibly bumped) total thoughts estimate          |
| `next_thought_needed`   | boolean | Echoed from the input                                  |
| `branches`              | array of strings | Sorted set of branch IDs currently in the map |
| `thought_history_length`| integer | Number of thoughts recorded so far (including this one) |

**State and concurrency.** Each `sequentialthinking` source owns its own thought history (one instance per `Connect` call). State is in-memory and mutex-protected so concurrent tool calls on the same source see consistent history. Thought history is reset on meta-server restart; persistence is a future enhancement.

The tool advertises `ReadOnlyHint=true`, `DestructiveHint=false`, `IdempotentHint=true`, `OpenWorldHint=false` — matching upstream's annotations.

### Temporal Source

Exposes **30 tools** for Temporal workflow orchestration, mapped onto the upstream Python [`temporal-mcp`](https://github.com/GethosTheWalrus/temporal-mcp). The source uses `go.temporal.io/sdk/client` directly and connects to a Temporal frontend (local dev server, Temporal Cloud, or self-hosted cluster).

The 30 tools split into 5 feature groups:

| Group           | Count | Lifecycle                                                                                                                                                                                                          |
|-----------------|-------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `*_workflow`    | 8     | `start_workflow`, `cancel_workflow`, `terminate_workflow`, `get_workflow_result`, `describe_workflow`, `list_workflows`, `get_workflow_history`, `continue_as_new` (signal-based; pair with a workflow that calls `workflow.NewContinueAsNewError`).       |
| `*_activity`    | 8     | `start_activity`, `execute_activity`, `get_activity_result`, `describe_activity`, `list_activities`, `count_activities`, `cancel_activity`, `terminate_activity` (Experimental standalone activities on a task queue).  |
| `*_schedule`    | 7     | `create_schedule`, `list_schedules`, `pause_schedule`, `unpause_schedule`, `delete_schedule`, `trigger_schedule`, `describe_schedule`                                                                                |
| `query_/signal_`| 2     | `query_workflow` (read-only), `signal_workflow` (mutating, NOT idempotent on the receiver side)                                                                                                                       |
| `batch_*`       | 5     | `batch_signal`, `batch_cancel`, `batch_terminate`, `batch_cancel_activities`, `batch_terminate_activities` (visibility-query fan-out with bounded concurrency via `errgroup.Group`; default 50, cap 100).              |

The source uses **`client.NewLazyClient`** under the hood — the binary never crashes on startup when Temporal is offline. RPC failures surface only when a tool is actually invoked; `tools/list` always succeeds.

**`connect` fields:**

| Field                  | Description                                                                         | Required | Default              |
|------------------------|-------------------------------------------------------------------------------------|----------|----------------------|
| `host`                 | `host:port` of the Temporal frontend                                                | No       | `localhost:7233`     |
| `namespace`            | Temporal namespace                                                                  | No       | `default`            |
| `tls_enabled`          | Tri-state: `true` force-on, `false` force-off, absent → auto-detect from `host`/`api_key` | No       | auto-detect          |
| `tls_client_cert_path` | Path to mTLS client cert (for Temporal Cloud / self-hosted with mTLS)                 | No       | —                    |
| `tls_client_key_path`  | Path to mTLS client private key                                                     | No       | —                    |
| `api_key`              | Temporal Cloud API key (forces TLS on automatically)                                | No       | —                    |

**Examples:**

```yaml
sources:
  workflows:
    type: temporal
    connect:
      host: localhost:7233
      namespace: default
    tools:
      prefix: t_
```

```yaml
sources:
  workflows:
    type: temporal
    connect:
      host: my-cluster.tmprl.cloud:7233
      namespace: production
      api_key: "${TEMPORAL_API_KEY}"
      tls_enabled: true
    tools:
      prefix: t1
```

mTLS example:

```yaml
sources:
  workflows:
    type: temporal
    connect:
      host: my-cluster.tmprl.cloud:7233
      namespace: production
      tls_client_cert_path: /etc/temporal/cert.pem
      tls_client_key_path: /etc/temporal/key.pem
    tools:
      prefix: t1
```

The full input/output JSON Schemas for all 30 tools live in `temporal/schemas/*.json` (embedded via `//go:embed` in the per-feature handler files). `cmd/testdata/sources_with_temporal.{yaml,json}` provides a complete configuration example that `cmd/binary_integration_test.go::TestBinary_TemporalSource_RegistersAll30Tools` exercises end-to-end.

## Environment Variables

| Variable        | Description                                  | Default |
|-----------------|----------------------------------------------|---------|
| `LOG_LEVEL`     | Log level (`debug`, `info`, `warn`, `error`, `disabled`) | `info`  |
| `LOG_JSON`      | Enable JSON log output (`true`/`false`)      | `false` |
| `LOG_ADD_SOURCE`| Add source file/line to logs (`true`/`false`)| `false` |

## License

MIT License — see [LICENSE](LICENSE) for details.

## Publishing a release

This module is served from a release-only public mirror. To publish a new release:

1. Tag the source commit: `git tag v0.x.y`
2. Push the tag: `git push origin v0.x.y`
3. Run `./mirror/mirror.sh <public-mirror-url>` to rebuild the public mirror from scratch.

The mirror script auto-discovers all semver `v*` tags from this repo and creates one commit per tag in the public mirror, with the excluded paths stripped. Authentication is the caller's responsibility — pass an SSH URL or an HTTPS URL with embedded credentials.
