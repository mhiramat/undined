# Undine Linux Kernel Mail Extension for VS Code (`undine-vscode`)

An official VS Code extension for the **Undine** Linux kernel mail workstation. It integrates Linux Kernel Mailing List (LKML) workflows, native Git diff viewing, bidirectional inline and commit description review comments, b4 series hydration, Patchwork tracking, and Copilot AI assistance into your VS Code environment.

## Features

- **Activity Bar Workspace**:
  - Vertically stacked, collapsible panes for **Folders**, **Thread Hierarchy**, and **Message Header**.
  - Fast keyboard navigation (`j` next patch, `k` previous patch, `/` search threads).
  - Unread badges and status indicators for emails.
  - Dedicated **Message Header** webview pane that shows envelope details (From, To, Cc, Date, Message-ID), clickable lore.kernel.org links, Patchwork CI checks and delegation, missing parent warning with b4 hydration, changed file shortcuts, and quick maintainer review actions (`Description`, `Apply (b4 am)`, `Compose LKML Reply`).
- **Native Diff & Patch Viewer**:
  - Virtual document provider (`undine-patch://`) supporting pre-image / post-image diff view with LKML standard 8-space tabs.
  - Dedicated virtual document for commit message body (`undine-patch://<msgid>/DESCRIPTION`).
  - Full-width editor area for diff and description inspection, keeping header metadata cleanly organized in the sidebar.
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
| `undine.authTokenPath` | `string` | `~/.local/share/undined/auth_token` | Path to Undine auth token |
| `undine.pollIntervalSeconds` | `number` | `30` | Auto-refresh interval in seconds (0 to disable) |
| `undine.tabSize` | `number` | `8` | Tab size for patch diffs and commit descriptions |
| `undine.autoOpenDiff` | `boolean` | `false` | Automatically open diff on message selection (default: false, diff is opened on-demand via the Message Header pane) |

## Building & Testing

### Using Make (Recommended)

```bash
cd vscode-extension
make         # Compiles TypeScript and packages into .vsix
make compile # Compiles TypeScript to out/
make test    # Runs unit tests
make clean   # Removes build artifacts and .vsix packages
```

### Using NPM Directly

```bash
cd vscode-extension
npm install
npm run compile
npm test
```

## Packaging & Installation

### Option 1: Package as VSIX & Install (Recommended)

To package the extension into an installable `.vsix` archive:

1. Install `@vscode/vsce` if not already installed:
   ```bash
   npm install -g @vscode/vsce
   ```

2. Package the extension:
   ```bash
   cd vscode-extension
   vsce package --allow-missing-repository
   ```
   *(This produces `undine-vscode-<version>.vsix`)*

3. Install into VS Code:
   - **Via Command Line**:
     ```bash
     code --install-extension undine-vscode-0.1.0.vsix
     ```
   - **Via VS Code GUI**:
     Open the Extensions view (`Ctrl+Shift+X`), click the `...` (More Actions) menu in the top right corner of the Extensions panel, choose **"Install from VSIX..."**, and select the generated file.

### Option 2: Live Development Symlink (For Active Development)

For rapid iteration without repackaging on every change:

1. Symlink the extension directory to your user extensions folder:
   ```bash
   cd vscode-extension
   npm run compile
   ln -s "$(pwd)" ~/.vscode/extensions/undine-vscode
   ```

2. Reload VS Code:
   Press `Ctrl+Shift+P` (or `Cmd+Shift+P`), type **"Developer: Reload Window"**, and press Enter. Any changes recompiled with `npm run compile` will be active immediately.

