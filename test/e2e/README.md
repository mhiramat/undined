# UndineD End-to-End (E2E) Test Suite

This directory contains the automated, hermetic End-to-End (E2E) test suite for the UndineD Linux kernel mail workstation.

---

## 1. What It Is

The E2E test suite simulates the entire lifecycle of patch review and email management in UndineD: from upstream IMAP ingestion and lore archive fetching to Patchwork tracking, SideDiff dual-column review, quote composition, SMTP email delivery, MCP AI review agent execution, and daemon process lifecycle management.

Rather than relying on live network services or third-party email accounts, the suite executes against self-contained in-memory mock servers running on dynamic local loopback ports (`127.0.0.1:0`).

---

## 2. Why It Was Introduced

UndineD operates as a mission-critical bridge between kernel mail streams (IMAP/SMTP/lore) and maintainer workflows (Git/Patchwork/review agents). Unit tests verify isolated packages in separation, but cannot catch cross-subsystem regressions such as:

* **Two-Stage Hydration Failure**: Mismatches between lightweight IMAP header ingestion (`BODY.PEEK[HEADER.FIELDS]`) and on-demand full-body hydration.
* **Mailing List Invariants**: Broken RFC `> ` reply quotes, missing patch hunks, or corrupted dual-column SideDiff paired rows.
* **Security & Network Portability**: Leaked absolute machine paths, unescaped non-ASCII characters, or header injection in outgoing SMTP transmissions.
* **Process Lifecycle Desynchronization**: Background daemon detach failures, stale PID files, or broken config CLI manipulation.
* **Hermetic CI & Pre-Commit Verification**: Developers and AI agents need a 100% reproducible test suite that runs in seconds without internet connectivity or credentials.

Under `AGENTS.md`, passing this E2E test suite (`make test-e2e`) is a mandatory requirement prior to creating any git commit.

---

## 3. Architecture & Directory Layout

```
test/e2e/
├── README.md               # This documentation
├── mocks/                  # In-memory mock upstream services
│   ├── imap_server.go      # Mock IMAP4rev1 server (LOGIN, SELECT, UID SEARCH, UID FETCH)
│   ├── smtp_server.go      # Mock SMTP server capturing dispatched emails and headers
│   ├── patchwork_server.go # Mock Patchwork v1.4 REST API (patches, series, users, checks)
│   └── b4_runner.go        # Executable mock b4 script generating RFC 5322 mbox streams
├── helpers_test.go         # Test harness (E2EEnv, isolated workspace, ephemeral ports)
├── full_stack_test.go      # End-to-end integration scenarios (S1 through S8)
└── daemon_cli_test.go      # Daemon process lifecycle, PID file, config CLI (S9)
```

### Mock Upstream Services

* **Mock IMAP Server** (`mocks/imap_server.go`): Listens on an ephemeral TCP port. Handles RFC 3501 commands: `CAPABILITY`, `LOGIN`, `SELECT`, `UID SEARCH`, and both header-only and full-body `UID FETCH` (`BODY[]`, `BODY.PEEK[]`, `BODY[HEADER.FIELDS ...]`).
* **Mock SMTP Server** (`mocks/smtp_server.go`): Thread-safe SMTP server implementing RFC 5321 commands (`EHLO`, `8BITMIME`, `MAIL FROM:<...>`, `RCPT TO:<...>`, `DATA`). Parses outgoing email headers, extracts message bodies, and records transmissions in memory.
* **Mock Patchwork Server** (`mocks/patchwork_server.go`): Simulates the Patchwork REST API v1.4 (`/api/1.4/patches/`, `/checks/`, `/mbox/`, `/series/`, `/users/`). Handles patch delegation updates (`PATCH`) and user lookups (`/users/?q=...`).
* **Mock b4 Runner** (`mocks/b4_runner.go`): Generates an executable shell script that logs invocations and outputs valid RFC 5322 mbox streams to stdout for `b4 mbox -o -` and `b4 am -o -`, piping directly into `git am`.

---

## 4. Test Scenarios Matrix

| Scenario | Test Function | Description |
| :--- | :--- | :--- |
| **S1 & S2** | `TestE2E_IngestionSearchAndThreading` | Ingests patch emails via IMAP into Maildir and SQLite index. Verifies SQLite FTS5 search (`GET /api/search?q=probes`) and conversation hierarchy (`GET /api/messages?threaded=true`). |
| **S3 & S4** | `TestE2E_PatchworkEnrichmentAndTodoSync` | Queries patch metadata (`GET /api/patchwork/info`), syncs delegated items to `TODO` folder (`POST /api/patchwork/sync-todo`), and transfers patch delegation via `PATCH /api/patchwork/patches/101`. |
| **S5** | `TestE2E_DiffCommentsAndSideDiff` | Verifies parsed diff hunks and dual-column SideDiff paired rows (`paired_rows`). Posts line-specific comments via `POST /api/messages/{id}/comments` and reads them back. |
| **S6** | `TestE2E_ReviewQuotingAndSMTPSending` | Previews RFC `> ` review quote via `POST /api/composer/preview-quote`. Dispatches review email via `POST /api/smtp/send`. Verifies mock SMTP captured headers and Sent folder recorded the message. |
| **S7** | `TestE2E_B4SeriesApply` | Applies a patch series using `POST /api/patch/apply` into an isolated Git repository, asserting `b4 am` executed and `git am` created a clean commit. |
| **S8** | `TestE2E_MCPAgentReview` | Boots Model Context Protocol (MCP) server over JSON-RPC 2.0 STDIO. Tests `tools/list`, `list_recent_emails`, `search_emails`, and `post_review_comment` with `FILE:LINE` coordinates and audit logging. |
| **S9** | `TestE2E_DaemonLifecycleAndCLIConfig` | Verifies `undined config get/set` on `patchwork.delegate-users`. Boots background daemon with `undined --daemon -C <config>`, checks PID file and `/login` health, and stops via `undined --quit`. |

---

## 5. How It Is Used

### Running the Complete E2E Suite

Execute the dedicated Makefile target:
```bash
make test-e2e
```

Or invoke `go test` directly with the required `fts5` build tag:
```bash
GOTOOLCHAIN=local go test -v -tags "fts5" ./test/e2e/...
```

### Running Specific Scenarios

Run a single test scenario:
```bash
GOTOOLCHAIN=local go test -v -tags "fts5" -run TestE2E_MCPAgentReview ./test/e2e/...
```

Run uncached (force fresh run):
```bash
GOTOOLCHAIN=local go test -count=1 -v -tags "fts5" ./test/e2e/...
```

---

## 6. Development & Coding Invariants

When extending or maintaining this E2E test suite, the following invariants defined in `AGENTS.md` must be preserved:

1. **Privacy & Anonymization**: Never use personal names or real email addresses in test emails, fixtures, or logs. Always use placeholders such as `"Kernel Developer"`, `"dev@example.com"`, and `"dev@kernel.org"`.
2. **No Absolute Paths**: Never hardcode machine-specific paths (e.g. `/home/...`). Use `t.TempDir()`, relative paths, or configuration variables.
3. **FTS5 Build Tag**: All test and build commands must specify `-tags "fts5"`.
4. **Clean Process Termination**: All background processes and mock listeners must register cleanup routines (`t.Cleanup()`) to ensure no orphan daemons or open ports persist after test completion.
