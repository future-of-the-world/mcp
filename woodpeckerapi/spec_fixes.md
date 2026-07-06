# Woodpecker spec normalization

The vendored Woodpecker 3.15.0 OpenAPI spec has several upstream
malformations that make it invalid JSON or that `oapi-codegen` refuses
to process. The following edits are applied when the spec is vendored
into `api.swagger.json` so the generator can produce a valid client.

All edits are mechanical and target clearly broken structure; they do
not change the API surface.

| # | Location | Issue | Fix |
|---|---|---|---|
| 1 | `/signature/public-key` GET | Trailing comma after `tags` array; missing `parameters` and `responses` blocks (incomplete endpoint in upstream) | Remove the endpoint entirely (not used by any tool in this source) |
| 2 | `Forge` schema | Duplicate `oauth_host` line with stray `"type": "string"` | Remove the stray duplicate line |
| 3 | `metadata.TrustedConfiguration` schema | Stray `"object"` token between the schema opener and the `properties` block | Remove the stray token |
| 4 | `model.TrustedConfiguration` schema | Same stray `"object"` token as `metadata.TrustedConfiguration` | Remove the stray token |
| 5 | `StepType` enum | `"enum": "plugin",` is missing the opening `[` of the array; the rest of the array was concatenated to the malformed line | Rewrite as a proper enum array with all five values (`clone`, `service`, `plugin`, `commands`, `cache`) |
| 6 | `model.ForgeType` schema `x-enum-varnames` | Two of the seven entries are placeholders (`"forge_remote_id"`) instead of the proper Go-style varnames | Replace placeholders with the canonical `ForgeTypeXxx` names (`ForgeTypeBitbucket`, `ForgeTypeBitbucketDatacenter`, `ForgeTypeAddon`) |
| 7 | `POST /repos/{repo_id}/pipelines/{pipeline_number}` query params | `event` and `deploy_to` had `"schema": {"type": {"type": "string"}}` (a schema-within-a-schema typo) | Replace with `"schema": {"type": "string"}` |
| 8 | Throughout `tags` arrays and inline enum arrays | Verbose multi-line `tags: [ "Agents" ]` literals, etc. — purely cosmetic, no structural change | Left untouched; the JSON content is identical. |
| 9 | `servers[0].url` | The original spec embeds `https//ci.amidman.dev/api` (the original author's personal Woodpecker instance, with the upstream's `https//` typo). The `mcp/woodpecker` source no longer ships with a default for `api_url`; the user must set it explicitly. Clear the URL to an empty string so the embedded spec no longer references a specific host. The generated client (`api.gen.go`) is unaffected at runtime — the base URL is passed in at construction. |
| 10 | `LogEntry` schema `data` field | Spec declared `data` as `array` of `integer`, but the Woodpecker server returns it as a base64-encoded string. `oapi-codegen` produced `Data *[]int` and `json.Unmarshal` failed on every real `get_step_logs` call with `cannot unmarshal string into Go struct field LogEntry.data of type []int`. | Change to `{ "type": "string", "format": "byte" }` (matches the OpenAPI semantic for base64-encoded binary; the generator emits `*string`). Update `mcp/woodpecker/woodpecker_client.go` `decodeLogEntry`/`decodeLogBytes` to base64-decode the data into UTF-8 text. |

None of these endpoints or schemas are exercised by the seven tools
this source exposes (list_repos, list_pipelines, get_pipeline,
get_step_logs, restart_pipeline, launch_pipeline, cancel_pipeline).
The fixes are scoped to the file `api.swagger.json` and do not change
the API surface.
