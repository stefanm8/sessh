# Jira daily-work release: v0.2.0

Status: complete and installed locally as `sessh 0.2.0` on 2026-10-05. All release requirements below passed acceptance. Publishing a tag or GitHub release remains outside this scope.

## Product decision

The repository currently has one release tag, `v0.1.0`. Version `v0.2.0` adds substantial Jira functionality without removing the existing CLI. The release lets a user discover work, understand its context, and perform routine issue updates through their existing browser session. This work prepares and installs a local build; it does not publish a GitHub release or alter live Jira data during development.

Current baseline: project list/view, issue list/view, raw JQL, basic status/assignee filters, browser links, raw JSON, and a recent fix for slow expired-session detection. Existing uncommitted session and Jira verification changes belong to the baseline and must be preserved.

## Compatibility and user experience

- Keep existing command names and aliases, profile configuration, automatic sign-in, and `--no-login` behavior.
- A bare `jira issue list` still means the current user's issues; raw `--jql` still takes precedence over generated filters. Existing unlimited `--limit 0` behavior stays compatible. Document `--limit 25` prominently for fast daily queries.
- Every new paginated list and the field catalog supports `--json` and finite `--limit`; zero means all available results and negative limits fail before a browser starts. Full transition metadata is always returned because it is a single small workflow response. Empty JSON lists are `[]`, never `null`.
- Validate local arguments before acquiring a browser. IDs and issue keys are path-escaped. No account, project, transition, or issue type is selected from an ambiguous display-name match.
- Tables remain concise; IDs required by subsequent commands must be visible. Raw Jira objects remain available in JSON output. Diagnostic and authentication messages stay off JSON stdout.
- Write commands execute an explicitly requested operation without an extra interactive prompt. Every write supports `--dry-run`, which performs no mutation and prints the exact method/path/payload that would be sent. Metadata reads may be necessary to resolve the payload and must be documented.
- Plain-text descriptions and comments become valid ADF. File/stdin input enables multiline content. `--fields-json PATH` (`-` for stdin) accepts a JSON fields object for the named operation, not an arbitrary API endpoint escape hatch. A field supplied both in this object and a named flag is rejected. On edit, `--description ''` sends `null`, `--labels ''` sends `[]`, and `--parent ''` sends Jira's documented parent-removal operation: `update.parent: [{"set":{"none":true}}]`. File/stdin flags are mutually exclusive with the matching inline flag.
- A write is never retried after an uncertain timeout, transport failure, 429, or 5xx. Authentication recovery must not replay a successful earlier write in a multi-request command. Unknown outcomes are explicit so a user can inspect Jira before trying again.

## Release requirements

### P0: trust and correctness

- [x] **CORE-1** Fix generated JQL escaping for backslashes and quotes, preserve raw-JQL precedence and current-user aliases, and cover injection-shaped literals with fixtures.
- [x] **CORE-2** Pagination handles modern issue-search tokens and offset-based project/comment/metadata collections, respects the requested remaining result count, and rejects token cycles, malformed items/envelopes, or safety-cap exhaustion instead of reporting incomplete success. Issue search uses the current Cloud API rather than falling back to the retired legacy search endpoint.
- [x] **CORE-3** Jira failures retain HTTP status plus stable, deterministic field errors. Invalid JSON and unexpected response shapes are failures; 403 is a permission problem for an authenticated user; 429 includes useful retry guidance without replaying writes.
- [x] **CORE-4** Negative limits and invalid local flags fail before browser acquisition. Empty raw lists are JSON arrays. Cancellation terminates page traversal and pending requests.
- [x] **CORE-5** Preserve the recent valid-session fast path and immediate expired-login detection. No added discovery calls on ordinary issue list/view paths.
- [x] **CORE-6** Fixture tests prove no mutation replay on uncertain failures and no writes in dry-run. Real service verification remains read-only.

### P1: find and understand daily work

- [x] **SEARCH-1** Extend issue list filters with issue type, priority, label, reporter, text, and unassigned support. Filters compose predictably with existing project/status/assignee inputs. Unsupported combinations fail clearly or follow documented precedence.
- [x] **SEARCH-2** Allow requested issue fields on list/view without losing the fields needed by the human display; JSON preserves returned custom fields. Provide ordering control with an allowlisted field/direction or a documented raw-JQL alternative.
- [x] **READ-1** `jira issue comments <KEY>` lists paginated comments with author, date, readable ADF, raw JSON, and limit.
- [x] **READ-2** `jira issue history <KEY>` lists paginated changelog entries with actor, date, and from/to field changes; raw JSON and limit are available.
- [x] **READ-3** `jira issue transitions <KEY>` displays currently available transition IDs, names, destination statuses, and required-field metadata in JSON.
- [x] **READ-4** Render useful ADF attributes such as mentions, emoji, links/cards, rules, and table cell separators while retaining unknown nodes' child text. Plain text remains supported.

### P1: routine issue changes

- [x] **WRITE-1** `jira issue create <PROJECT>` supports summary, issue type, description, assignee, priority, labels, parent, and structured additional fields. Required inputs are validated. It returns the created issue key/URL or machine-readable response.
- [x] **WRITE-2** `jira issue edit <KEY>` supports changing summary, description, priority, labels, parent, and structured additional fields. Omitted fields stay untouched; explicit clearing is supported and documented where valid. Empty edits fail.
- [x] **WRITE-3** `jira issue assign <KEY> <ACCOUNT_ID|me|unassigned>` resolves `me` to the logged-in account ID and sends `null` to unassign; other IDs are used explicitly.
- [x] **WRITE-4** `jira issue transition <KEY> <TRANSITION_ID>` validates a currently available transition and supports required transition fields through structured JSON. Names may be an optional convenience only if unique; never guess a transition from a destination status.
- [x] **WRITE-5** `jira issue comment add <KEY>` supports inline text and file/stdin body input, emits valid ADF, and returns the added comment or its ID. Empty content fails.
- [x] **WRITE-6** All mutation commands share dry-run output, deterministic errors, strict input/file validation, success handling for 201/204, and safe non-replay behavior.

### P1: discover valid values

- [x] **META-1** `jira user search <QUERY>` lists account IDs, display names, and active state with limit/JSON. It does not guess identities or expose optional private profile fields in the table.
- [x] **META-2** `jira field list` lists built-in/custom field IDs, names, and schema type with limit/JSON.
- [x] **META-3** `jira project types <PROJECT>` lists create-capable issue type IDs/names using paginated create metadata, with limit/JSON.
- [x] **META-4** `jira issue create-meta <PROJECT> <TYPE_ID>` displays fields, required state, and allowed values; paginated fields and raw JSON are supported. `jira issue edit-meta <KEY>` exposes fields editable on that issue.

### P1: board and sprint exploration

- [x] **AGILE-1** `jira board list` supports project, name, type, limit, and JSON; `jira board view <ID>` shows board details. Scrum/Kanban IDs remain visible.
- [x] **AGILE-2** `jira board issues <ID>` lists board issues with limit/JSON and bounded pagination. Browser/API permissions are surfaced clearly.
- [x] **AGILE-3** `jira sprint list <BOARD_ID>` supports a validated comma-separated state filter (`active`, `future`, `closed`), limit, and JSON. `jira sprint view <ID>` shows its dates, state, goal, and origin board.
- [x] **AGILE-4** `jira sprint issues <ID>` lists sprint issues with limit/JSON. Actual 403/404 and unsupported-board responses return actionable errors. Jira can return an empty board issue list when board permissions hide its issues; preserve that valid response and document that it does not prove the board is empty.

### P1: delivery and validation

- [x] **DOC-1** README and the bundled sessh skill explain the new daily workflow, finite limits, discovery, write permissions, dry-run, advanced fields, and examples using placeholder data.
- [x] **DOC-2** Command help lists flags, defaults, ID requirements, and stdin/file semantics. Syntax in this tracker is reconciled to the implemented contract before completion.
- [x] **QA-1** Unit/fixture tests cover request methods, URLs, bodies, response parsing, pagination, empty output, local validation, ADF conversion, and errors for every command family.
- [x] **QA-2** `make check` passes. Run local browser e2e tests when shared session/login code changes. Build and install a local `0.2.0` binary, verify its reported version and representative help.
- [x] **QA-3** Adversarial review identifies concrete gaps; fixes land and regression checks pass. Re-run acceptance against the final feature matrix rather than declaring completion from tests alone.
- [x] **QA-4** Read-only signed-in smoke test checks a finite issue list and feasible new discovery commands. Never create, edit, assign, transition, or comment against the user's live tenant as part of this implementation.

## Explicitly deferred

Bulk changes; issue deletion; comment deletion/editing; attachment upload/download; worklogs; watchers/votes; issue linking; sprint creation/start/close or moving issues between sprints; workflow administration; rich Markdown-to-ADF conversion; local result caches/offline synchronization; interactive forms; saved searches; Jira Server/Data Center support; publishing a release/tag. These are separate product decisions and are not required for v0.2.0 acceptance.

## Reference API contracts

Implementation targets Jira Cloud REST v3 for issue CRUD/metadata and Jira Software Agile REST for boards/sprints. Check current endpoint pagination and payloads against Atlassian's primary documentation:

- [Issues, transitions, and create/edit metadata](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issues/)
- [Issue search](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-search/)
- [Comments](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-comments/)
- [User search](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-user-search/)
- [Issue fields](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-fields/)
- [Boards](https://developer.atlassian.com/cloud/jira/software/rest/api-group-board/)
- [Sprints](https://developer.atlassian.com/cloud/jira/software/rest/api-group-sprint/)

## Review log

- 2026-10-05: Root review approved ambitious `0.2.0` daily-work scope, preservation of old list defaults, exact dry-runs, ID-based transition discovery, and strict non-replay mutation semantics.
- 2026-10-05: Independent adversarial review added pagination cycle/malformed-record/safety-cap failures, empty-array JSON, and negative-limit validation as release gates.
- 2026-10-05: Current Atlassian contracts require enhanced `/rest/software/1.0` token-based board/sprint issue reads; project create metadata uses `issueTypes` and `fields` arrays. Deprecated platform search fallback is removed. Empty board issue responses cannot distinguish an empty board from hidden issues.

## Acceptance evidence

- META-1–4 and AGILE-1–4: implemented in `plugins/jira/discovery.go` and `plugins/jira/agile.go`; command fixtures in `discovery_test.go` and `agile_test.go` exercise routing, bodies, query encoding, metadata pagination, token traversal, visibility-empty JSON, malformed responses, account IDs, and validation before session acquisition. `go test ./plugins/jira` passed. Independent adversarial review accepted these requirements after positive type-ID and path-identifier fixes. Vendor user search is explicitly limited to 1,000 matches; Jira board visibility can produce an empty issue list.


## Implementation evidence

- Core/search: `client_test.go` covers remaining-count page sizes, server-clamped offset pages, token cycles, malformed envelopes/items, cancellation, the 2,000-page cap, deterministic field errors, empty-array output, custom fields, and generated JQL escaping. `/myself` service/shape failures now propagate without slow sign-in polling. Existing verification latency fixtures remain intact.
- Read commands and ADF: `client_test.go` verifies comments, changelog, transitions, attribute/card/link/table rendering, and stdout routing. Human list/view output uses the command's stdout writer; JSON preserves raw objects.
- Writes: `issue_write_test.go` covers all five mutation families, exact dry-run payloads with zero writes, 201/204 results, no follow-up read, malformed/ambiguous/local input rejection, file/stdin ADF, duplicate-field conflicts, precision-preserving custom numbers, explicit clear operations, and failures without repeated requests. Parent removal uses the documented `update.parent` operation. Transition-screen required fields are validated by Jira against the resulting issue state; omitted already-populated values are accepted locally.
- Independent fresh review: `release_qa_test.go` adds regressions rejecting scalar, array, and arbitrary-object success payloads for edits/assignments/transitions, while allowing documented issue-shaped edit responses. Invalid assignee input fails before metadata access. All Jira unit and fixture tests passed with race detection: `go test -race ./plugins/jira`.
- Delivery docs: README and bundled `skills/sessh/SKILL.md` cover discovery, planning, search limits/fields, permission and no-replay behavior, all mutation syntax, exact dry-runs, stdin, advanced fields, and clearing. Existing command names and list defaults are preserved.

## Final delivery evidence

- Product Owner defined the release contract, implemented discovery/Agile commands, and completed product acceptance. The issue implementation agent delivered the remaining Jira workflows. Two independent adversarial reviewers accepted the final implementation after fixes; root also reviewed the shared session and integration behavior.
- `make check` passed on the final code: formatting, shell syntax, module consistency, vet, and repository-wide race tests. The independent final reviewer also ran `go test -race ./plugins/jira -count=1` successfully.
- Full `make e2e` passed in 89.526 seconds, covering login recovery, concurrent sign-in, cancellation, daemon behavior, and synthetic Jira workflows. After the final write-response validation changes, `TestJiraWriteWorkflows` passed again in 8.167 seconds.
- The real-browser synthetic workflow suite verifies all five mutations, exact dry-run payloads with zero writes, HTTP 201/204 handling, human output on stdout, a mutation through a daemon, and an accepted write whose response is lost being sent exactly once. Shared session tests cover authentication recovery before writes, no replay after any write attempt, 403 classification, response disposal, active HTTP/navigation cancellation, and RPC deadlines/cancellation.
- Twelve signed-in, read-only smoke checks passed: finite issue/project/field/board lists, issue view/comments/history/transitions, board view/issues, project issue types, and create metadata. No live mutation command was executed. Vendor-specific write permissions and workflow rules were verified with fixtures and official contracts, not by changing live issues.
- `make install VERSION=0.2.0` installed `~/.local/bin/sessh`. The installed binary reports `sessh version 0.2.0`; create help exposes dry-run and advanced fields. A final installed `jira issue list --limit 5 --json --no-login` returned five records in 2.09 seconds.

## Warm browser follow-up — 2026-10-05

The installed CLI now needs to retain its headless browser and reusable tabs
between commands, addressing the user's follow-up performance report.

- [x] Normal browser-backed commands automatically start or reuse a detached worker per resolved profile; first callers converge on one worker.
- [x] Default inactivity timeout is 30 minutes. `--keep-alive` / `SESSH_KEEP_ALIVE` set a new worker's timeout; `0` means indefinite, `off` / `--no-keep-alive` disable automatic startup. Existing workers retain their timeout.
- [x] Preserve up to one idle tab per origin, eight total, with exclusive leases for concurrent commands. HTTP-only clients create no tabs. Caller origins survive routing through another plugin's daemon.
- [x] Connected active commands prevent idle expiry, while status probes do not. Cancelled navigation/evaluation discards the affected page; disconnect and shutdown cancel browser work.
- [x] Keep live authentication checks, while reusing a successful check's identity for metadata instead of making a second `/myself` request. Query results remain live.
- [x] Sign-in serializes concurrent callers and pauses/resumes the worker. Logout and import stop the matching managed daemon before changing the profile. Import keeps its existing cookie/localStorage overlay semantics; account switching requires clearing the target first. Mutations retain the no-replay guard.
- [x] Final full checks, browser workflow regression suite, installation, and installed performance verification.

Measured before installation: the old installed binary's five-issue read-only
query took 2.551, 1.754, and 2.110 seconds. The new build took 1.567 seconds on
first start and 0.974, 1.306, 0.563, and 1.355 seconds with the same warm daemon.
These are small live-network samples, not latency guarantees. Synthetic local
browser tests isolate startup cost and verify daemon PID and DOM reuse directly.

Final evidence:

- `make check` passed, including repository-wide race tests. Socket cancellation tests passed 30 repetitions after correcting the socket/context deadline race. The final help-text clarification passed `go test ./cmd/sessh`.
- Independent adversarial reviews drove fixes for concurrent login, shutdown/startup lock contention, acquisition-only transport retry, caller origins, cancelled page work, identity reuse, and applying imported credentials after a warm session. Concurrent sign-in passed five stress repetitions.
- The isolated full real-browser suite passed in 98.080 seconds. It covers Jira mutation non-replay, login/relogin, permission errors, cancellation, import/logout, six simultaneous cold commands, crash/stop/restart, idle leases and expiry, profile isolation, one fresh identity check per command, DOM reuse, and exclusive origin-specific tabs. No synthetic test workers remained after cleanup.
- `make install VERSION=0.2.0` installed the final binary at `/Users/stefanmihartescu/.local/bin/sessh`. It reports `sessh version 0.2.0`. Final installed five-issue reads returned five records each: cold 2.402 seconds, then warm 0.925, 0.996, 1.364, and 1.433 seconds (mean 1.179 seconds, about 45% below the old sample mean of 2.138 seconds). The default `jira issue list --no-login` completed in 0.975 seconds.
- The installed binary's managed Atlassian daemon was left running, with the automatic 30-minute idle timeout. No live Jira write was executed.
