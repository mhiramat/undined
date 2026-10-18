# Undine Linux Kernel Mail Extension for VS Code (`undine-vscode`)

An official VS Code extension for the **Undine** Linux kernel mail workstation. It integrates Linux Kernel Mailing List (LKML) workflows, native Git diff viewing, bidirectional inline and commit description review comments, b4 series hydration, Patchwork tracking, and Copilot AI assistance into your VS Code environment.

## Features

- **Activity Bar Workspace**:
  - Vertically stacked, collapsible panes for **Folders**, **Thread Hierarchy**, and **Patchwork**.
  - Fast keyboard navigation (`j` next patch, `k` previous patch, `/` search threads).
  - Unread badges and status indicators for emails and Patchwork CI checks.
- **Native Diff & Patch Viewer**:
  - Virtual document provider (`undine-patch://`) supporting pre-image / post-image diff view with LKML standard 8-space tabs.
  - Dedicated virtual document for commit message body (`undine-patch://<msgid>/DESCRIPTION`).
  - Option A Message Header view docked alongside diffs with metadata, lore.kernel.org hyperlinks, and action buttons (`Hydrate via b4`, `Apply to Git`, `Delegate to Me`, `Reply`).
- **Bidirectional Review Comments**:
  - Integration with VS Code Comments API (`vscode.comments`).
  - Comment on diff lines or commit descriptions (`DESCRIPTION:LINE`).
  - Comments sync bidirectionally with UndineD's REST API and MCP review tools.
- **Reply Composer & b4 Integration**:
  - Previews LKML replies with quotes and inline notes (`POST /api/composer/preview-quote`).
  - Direct series hydration via b4 (`POST /api/threads/import-b4`) and Git branch application (`POST /api/patch/apply`).
- **Copilot Chat Participant (`@undine`)**:
  - `/review`: Analyze current patch for LKML guidelines, memory safety, and concurrency.
  - `/summarize`: High-level summary of the patch or thread.
  - `/draft-reply`: Assist in drafting responses incorporating inline review comments.

## Configuration Settings

| Setting | Type | Default | Description |
|---|---|---|---|
| `undine.serverUrl` | `string` | `http://127.0.0.1:8080` | UndineD REST API server URL |
| `undine.tokenPath` | `string` | `""` (auto-detect) | Path to Undine auth token (defaults to `~/.local/share/undined/auth_token`) |
| `undine.pollIntervalSeconds` | `number` | `30` | Auto-refresh interval in seconds (0 to disable) |
| `undine.tabSize` | `number` | `8` | Tab size for patch diffs and commit descriptions |

## Building & Testing

```bash
cd vscode-extension
npm install
npm run compile
npm test
```
