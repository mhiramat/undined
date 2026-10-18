# UndineD Development & Coding Guidelines

Guidelines and invariants for agentic development on the Undine Linux kernel mail workstation.

## 1. Privacy, Anonymity, and Path Portability
- **Anonymization in Code and Tests**: Never use the developer's real name (Masami Hiramatsu) or personal email (`mhiramat@kernel.org`) in sample data, tests, mock emails, or documentation. Always use neutral placeholders such as `"Kernel Developer"` and `"dev@example.com"` (or `"dev@kernel.org"` for mock lore emails).
- **No Absolute Paths**: Avoid exposing machine-specific absolute filesystem paths (e.g., `/home/...`). Use relative paths (e.g., `./bin/undined`) or configuration variables for storage and binary paths.

## 2. Build and Test Invariants
- **FTS5 Build Tag**: Undine relies on SQLite FTS5 for full-text search. All test and build invocations must include the `fts5` tag:
  - `make test` or `go test -tags "fts5" ./...`
  - `make build` or `go build -tags "fts5" -o bin/undined ./cmd/undined`

## 3. Git Commit Hygiene
- **Separate Commits**: When addressing multiple distinct bugs, vulnerabilities, or enhancements, commit each logical change separately with an explanatory commit message. Avoid bundling unrelated changes into a single commit.

## 4. Process and Command Safety
- **Input Sanitization**: Strictly sanitize user-controlled parameters (Message-IDs, Git refs, target coordinates) before passing them to subprocesses or queries.
- **Configurable Tool Execution**: For external review agents or runners, allow user-configured wrapper scripts rather than hardcoding raw CLI commands.
