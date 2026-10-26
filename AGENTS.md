# UndineD Development & Coding Guidelines

Guidelines and invariants for agentic development on the Undine Linux kernel mail workstation.

## 1. Privacy, Anonymity, and Path Portability
- **Anonymization in Code and Tests**: Never use the developer's real name (Masami Hiramatsu) or personal email (`mhiramat@kernel.org`) in sample data, tests, mock emails, or documentation. Always use neutral placeholders such as `"Kernel Developer"` and `"dev@example.com"` (or `"dev@kernel.org"` for mock lore emails).
- **No Absolute Paths**: Avoid exposing machine-specific absolute filesystem paths (e.g., `/home/...`). Use relative paths (e.g., `./bin/undined`) or configuration variables for storage and binary paths.

## 2. Build and Test Invariants
- **FTS5 Build Tag**: Undine relies on SQLite FTS5 for full-text search. All test and build invocations must include the `fts5` tag:
  - `make test` or `go test -tags "fts5" ./...`
  - `make test-e2e` or `go test -tags "fts5" ./test/e2e/...`
  - `make build` or `go build -tags "fts5" -o bin/undined ./cmd/undined`
- **Pre-Commit E2E Testing Requirement**: Always run the End-to-End test suite (`make test-e2e`) and full test suite (`make test`) before making any git commit to verify that no end-to-end user workflows or daemon processes are broken.

## 3. Git Commit Hygiene
- **Run E2E Tests Before Committing**: Run `make test-e2e` and ensure all tests pass prior to committing any changes.
- **Separate Commits**: When addressing multiple distinct bugs, vulnerabilities, or enhancements, commit each logical change separately with an explanatory commit message. Avoid bundling unrelated changes into a single commit.
- **Message Wrapping**: Commit messages must be wrapped before the 80-character mark (recommend 72 characters for body text and 50–72 characters for subjects). Never exceed 80 characters per line.

## 4. Process and Command Safety
- **Input Sanitization**: Strictly sanitize user-controlled parameters (Message-IDs, Git refs, target coordinates) before passing them to subprocesses or queries.
- **Configurable Tool Execution**: For external review agents or runners, allow user-configured wrapper scripts rather than hardcoding raw CLI commands.

## 5. Patch Integrity & Non-ASCII Handling Policy
- **No Silent Normalization**: Never silently convert or sanitize non-ASCII characters (e.g., `\u00a0` NBSP, `\u2002` en-space) in patch diffs into ASCII whitespace.
- **Mark as Corrupted**: Treat any email with non-ASCII characters in its diff lines as corrupted (`is_corrupted = true`).
- **Reject SideDiff**: Do not render corrupted patches in the SideDiff dual-column view. Display them as plain text with non-ASCII characters visually escaped (e.g. `<U+00A0>`) and warn the user.
- **Reference**: See `docs/POLICY_PATCH_INTEGRITY.md` for full background, security rationale, and presentation rules.

## 6. Reply Quoting & Body Retention Invariant
- **Full Quoting on Zero Comments**: When composing a reply or generating review quotes without inline comments, always quote the entire body of the original email with standard RFC `> ` quoting prefixes. Never emit an empty or truncated quote.

## 7. API Serialization & Nil-Slice Safety
- **No Null Arrays in JSON**: Slices representing collections (such as `paired_rows`, `unified_lines`, `hunks`, `attachments`, `comments`) must always be initialized to empty slices (`make(..., 0)` or `[]*T{}`) instead of `nil`. This guarantees they serialize as `[]` rather than `null`, preventing frontend iteration exceptions (`TypeError: ... is not iterable`).

## 8. MIME Attachment Security & Download Invariants
- **Filename Sanitization**: Always sanitize attachment filenames with `filepath.Base()` and strip directory traversal components (`../`), control characters, and quotes.
- **Secure Download Headers**: Attachment download endpoints must enforce `Content-Disposition: attachment; filename="..."; filename*=UTF-8''...` along with `X-Content-Type-Options: nosniff` and `Content-Security-Policy: default-src 'none'`.

## 9. IMAP Sync & Checkpoint Progression Invariant
- **Contiguous Checkpoint Progression**: A folder's `last_synced_uid`
  checkpoint must only advance up to the highest contiguous server UID
  verified to be present in the local database. Never unconditionally
  advance the checkpoint to the maximum server UID if messages remain
  undownloaded or header fetches fail.
- **Strict Error Propagation**: Sync operations must never report
  completion (`status = "completed"`) or discard fetch errors when message
  summaries fail to download or return empty payloads. They must set
  `status = "error"`, record the error detail, and halt batch processing so
  subsequent automatic polling does not skip missing messages.

## 10. Active Configuration Presentation Invariant
- **No Raw Config File Echoing**: Never display configuration files
  directly as-is to the user or CLI. Raw files may contain obsolete,
  unparsed, or deprecated keys from previous versions.
- **Sectional Organization & Default Annotations**: Always present active
  configuration settings grouped by section. Any setting that has not been
  explicitly configured must be displayed with its key followed by its
  default value, preceded by a `#` comment symbol.

## 11. Inline Script Restrictions
- NEVER run inline scripts such as `python3 -c "..."`, `python -c "..."`, or arbitrary shell one-liners (`sh -c`, `bash -c`) for debugging, smoke testing, or verification.
- Do not request permission to execute embedded multiline code blocks via CLI flags.

## 12. Preferred Verification Procedures
Always verify changes using one of the following methods, in order of priority:

1. **Test Suite (Primary)**:
   - Run existing tests using the project's standard test runner:
     `pytest tests/path/to/test_file.py -k <test_name>`
   - If no relevant test exists, add a formal test case to the test suite.

2. **Temporary Script File (Secondary)**:
   - If an ad-hoc check is strictly necessary and cannot be integrated into a test suite, create a standalone temporary script file (e.g., `tests/tmp_verify.py` or `.tmp/scratch.py`).
   - Execute the file directly:
     `python3 tests/tmp_verify.py`
   - Clean up (delete) the temporary script as soon as the verification is complete.

## 13. Command Execution Rules
- Always execute commands against existing or newly created file paths.
- Ensure terminal commands are short, deterministic, and easily auditable in diffs.
