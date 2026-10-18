/**
 * MessageHeaderViewProvider: WebviewView provider for the Message Header sidebar pane.
 * Replaces the patchwork vertical pane to display envelope headers, lore links,
 * Patchwork status, and quick maintainer review actions in the Undine sidebar.
 */

import * as vscode from "vscode";
import { MessageDetail } from "../client/types";
import { UndineRestClient } from "../client/restClient";

export class MessageHeaderViewProvider implements vscode.WebviewViewProvider {
  public static readonly viewType = "undine.headerView";
  public static instance?: MessageHeaderViewProvider;

  private _view?: vscode.WebviewView;
  private _currentDetail: MessageDetail | null = null;
  private _patchworkInfo: any = null;
  private disposables: vscode.Disposable[] = [];

  constructor(
    private readonly extensionUri: vscode.Uri,
    private readonly client: UndineRestClient
  ) {
    MessageHeaderViewProvider.instance = this;
  }

  public static createOrShow(
    extensionUri: vscode.Uri,
    detail: MessageDetail,
    client: UndineRestClient
  ): MessageHeaderViewProvider {
    if (MessageHeaderViewProvider.instance) {
      MessageHeaderViewProvider.instance.show(true);
      MessageHeaderViewProvider.instance.setMessage(detail);
      return MessageHeaderViewProvider.instance;
    }
    const provider = new MessageHeaderViewProvider(extensionUri, client);
    provider.setMessage(detail);
    return provider;
  }

  public resolveWebviewView(
    webviewView: vscode.WebviewView,
    _context: vscode.WebviewViewResolveContext,
    _token: vscode.CancellationToken
  ): void {
    this._view = webviewView;

    webviewView.webview.options = {
      enableScripts: true,
      localResourceRoots: [this.extensionUri],
    };

    webviewView.webview.html = this.getHtmlForWebview(webviewView.webview);

    webviewView.webview.onDidReceiveMessage(
      async (message) => {
        switch (message.command) {
          case "openDescription":
            vscode.commands.executeCommand(
              "undine.openCommitDescription",
              message.msgId || this._currentDetail?.message.message_id
            );
            break;
          case "openDiff":
            vscode.commands.executeCommand(
              "undine.openPatchDiff",
              message.msgId || this._currentDetail?.message.message_id,
              message.file
            );
            break;
          case "importB4":
            vscode.commands.executeCommand(
              "undine.importB4",
              message.msgId || this._currentDetail?.message.message_id
            );
            break;
          case "applyPatch":
            vscode.commands.executeCommand(
              "undine.applyPatch",
              message.msgId || this._currentDetail?.message.message_id
            );
            break;
          case "generateReply":
            vscode.commands.executeCommand(
              "undine.generateReplyDraft",
              message.msgId || this._currentDetail?.message.message_id
            );
            break;
          case "delegateToMe":
            if (message.patchId) {
              vscode.commands.executeCommand("undine.delegateToMe", message.patchId);
            }
            break;
          case "openLore":
            if (message.url) {
              vscode.env.openExternal(vscode.Uri.parse(message.url));
            }
            break;
          case "copyText":
            if (message.text) {
              await vscode.env.clipboard.writeText(message.text);
              vscode.window.showInformationMessage(`Copied to clipboard: ${message.text}`);
            }
            break;
        }
      },
      null,
      this.disposables
    );

    webviewView.onDidDispose(() => {
      this._view = undefined;
    });
  }

  public async setMessage(detail: MessageDetail | null): Promise<void> {
    this._currentDetail = detail;
    this._patchworkInfo = null;
    this.updateContent();

    if (detail?.message?.message_id) {
      const cleanMsgId = detail.message.message_id.replace(/^<|>$/g, "");
      try {
        const pw = await this.client.getPatchworkInfo(cleanMsgId);
        // Only update if current message hasn't changed while awaiting API response
        if (this._currentDetail?.message?.message_id === detail.message.message_id) {
          this._patchworkInfo = pw;
          this.updateContent();
        }
      } catch {
        // Patchwork is optional; ignore network/config errors
      }
    }
  }

  public getMessage(): MessageDetail | null {
    return this._currentDetail;
  }

  public getPatchworkInfo(): any {
    return this._patchworkInfo;
  }

  public clear(): void {
    this._currentDetail = null;
    this._patchworkInfo = null;
    this.updateContent();
  }

  public show(preserveFocus = true): void {
    this._view?.show?.(preserveFocus);
  }

  public updateContent(): void {
    if (!this._view) {
      return;
    }
    const isPatch = this._currentDetail?.message?.is_patch;
    this._view.description = isPatch ? "Patch" : this._currentDetail ? "Email" : "";
    this._view.webview.html = this.getHtmlForWebview(this._view.webview);
  }

  public dispose(): void {
    MessageHeaderViewProvider.instance = undefined;
    while (this.disposables.length) {
      const x = this.disposables.pop();
      if (x) x.dispose();
    }
  }

  public getHtmlForWebview(_webview?: vscode.Webview): string {
    if (!this._currentDetail) {
      return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline';">
  <style>
    ${this.getStyles()}
  </style>
</head>
<body>
  <div class="empty-container">
    <div class="empty-icon">✉️</div>
    <div class="empty-title">No Message Selected</div>
    <div class="empty-desc">Select an email or patch from Thread Hierarchy to inspect headers, lore links, and patch actions.</div>
  </div>
</body>
</html>`;
    }

    const m = this._currentDetail.message;
    const cleanMsgId = m.message_id.replace(/^<|>$/g, "");
    const loreUrl = `https://lore.kernel.org/r/${encodeURIComponent(cleanMsgId)}`;

    // Missing parent banner
    let missingParentHtml = "";
    if (this._currentDetail.parent_info && !this._currentDetail.parent_info.found && m.in_reply_to) {
      missingParentHtml = `
        <div class="warning-banner">
          <span>⚠️ Parent email missing from local store.</span>
          <button class="btn btn-warning btn-sm" onclick="sendMessage('importB4')">Hydrate via b4</button>
        </div>
      `;
    }

    // Patchwork status card (if available)
    let patchworkHtml = "";
    if (this._patchworkInfo) {
      const pw = this._patchworkInfo;
      const delegateName = pw.delegate?.username || "unassigned";
      const checkBadge = this.renderPatchworkCheck(pw);
      patchworkHtml = `
        <div class="patchwork-card">
          <div class="patchwork-header">
            <span><strong>Patchwork:</strong> #${pw.id} · ${this.escapeHtml(pw.state || "new")}</span>
            ${checkBadge}
          </div>
          <div class="patchwork-body">
            <div class="field-row">
              <span class="field-label">Delegate:</span>
              <span class="field-value">${this.escapeHtml(delegateName)}</span>
            </div>
            ${pw.id ? `
              <div style="margin-top: 4px;">
                <button class="btn btn-secondary btn-sm" onclick="delegateToMe(${pw.id})">Delegate to Me</button>
              </div>
            ` : ""}
          </div>
        </div>
      `;
    }

    // Changed files list
    let filesHtml = "";
    if (this._currentDetail.parsed_diff?.files && this._currentDetail.parsed_diff.files.length > 0) {
      const files = this._currentDetail.parsed_diff.files;
      const fileButtons = files
        .map((f) => {
          const fn = f.new_path || f.old_path;
          return `<button class="btn-file" onclick="openFileDiff('${this.escapeHtml(fn)}')">📄 ${this.escapeHtml(fn)}</button>`;
        })
        .join("");

      filesHtml = `
        <div class="files-section">
          <span class="files-title">Changed Files (${files.length}):</span>
          <div class="files-list">${fileButtons}</div>
        </div>
      `;
    }

    return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline';">
  <style>
    ${this.getStyles()}
  </style>
</head>
<body>
  <div class="header-card">
    ${missingParentHtml}

    <div class="badges-row">
      ${m.is_patch ? '<span class="badge badge-patch">PATCH</span>' : '<span class="badge badge-email">EMAIL</span>'}
      ${!m.is_read ? '<span class="badge badge-unread">UNREAD</span>' : ""}
      ${m.is_replied ? '<span class="badge badge-replied">REPLIED</span>' : ""}
      ${m.tag ? `<span class="badge badge-tag">${this.escapeHtml(m.tag)}</span>` : ""}
    </div>

    <div class="subject-title">${this.escapeHtml(m.subject || "(No Subject)")}</div>

    <div class="meta-table">
      <div class="field-row">
        <span class="field-label">From:</span>
        <span class="field-value">${this.escapeHtml(m.from_name ? `${m.from_name} <${m.from_addr}>` : m.from_addr)}</span>
      </div>

      ${m.to_addr ? `
        <div class="field-row">
          <span class="field-label">To:</span>
          <span class="field-value">${this.escapeHtml(m.to_addr)}</span>
        </div>
      ` : ""}

      ${m.cc_addr ? `
        <div class="field-row">
          <span class="field-label">Cc:</span>
          <span class="field-value">${this.escapeHtml(m.cc_addr)}</span>
        </div>
      ` : ""}

      <div class="field-row">
        <span class="field-label">Date:</span>
        <span class="field-value">${this.escapeHtml(m.date)}</span>
      </div>

      <div class="field-row">
        <span class="field-label">Msg-ID:</span>
        <span class="field-value">
          <code>&lt;${this.escapeHtml(cleanMsgId)}&gt;</code>
          <a href="#" class="lore-link" onclick="openLore('${loreUrl}')" title="Open in lore.kernel.org">[lore ↗]</a>
          <button class="btn-icon" onclick="copyText('&lt;${this.escapeHtml(cleanMsgId)}&gt;')" title="Copy Message-ID">📋</button>
        </span>
      </div>
    </div>

    ${patchworkHtml}
    ${filesHtml}

    <div class="actions-container">
      <div class="actions-row">
        <button class="btn btn-primary" onclick="sendMessage('openDescription')">📝 Description</button>
        ${m.is_patch ? '<button class="btn btn-primary" onclick="sendMessage(\'applyPatch\')">📥 Apply (b4 am)</button>' : ""}
      </div>
      <button class="btn btn-secondary" onclick="sendMessage('generateReply')">💬 Compose LKML Reply</button>
    </div>
  </div>

  <script>
    const vscode = acquireVsCodeApi();
    function sendMessage(command) {
      vscode.postMessage({ command });
    }
    function openFileDiff(file) {
      vscode.postMessage({ command: 'openDiff', file });
    }
    function delegateToMe(patchId) {
      vscode.postMessage({ command: 'delegateToMe', patchId });
    }
    function openLore(url) {
      vscode.postMessage({ command: 'openLore', url });
    }
    function copyText(text) {
      vscode.postMessage({ command: 'copyText', text });
    }
  </script>
</body>
</html>`;
  }

  private renderPatchworkCheck(pw: any): string {
    const check = (pw.check || pw.checks || "").toString().toLowerCase();
    if (check.includes("pass") || check.includes("success")) {
      return '<span class="check-pass">✔ PASS</span>';
    } else if (check.includes("fail")) {
      return '<span class="check-fail">✖ FAIL</span>';
    } else if (check.includes("warn")) {
      return '<span class="check-warn">⚠ WARN</span>';
    } else if (check.includes("pending")) {
      return '<span class="check-pending">⏳ PENDING</span>';
    }
    return "";
  }

  private escapeHtml(str?: string): string {
    if (!str) return "";
    const map: Record<string, string> = {
      "&": "&amp;",
      "<": "&lt;",
      ">": "&gt;",
      '"': "&quot;",
      "'": "&#39;",
    };
    return str.replace(/[&<>"']/g, (m) => map[m] || m);
  }

  private getStyles(): string {
    return `
      body {
        font-family: var(--vscode-font-family);
        font-size: var(--vscode-font-size, 12px);
        color: var(--vscode-foreground);
        background-color: var(--vscode-sideBar-background, var(--vscode-editor-background));
        margin: 0;
        padding: 10px 12px;
        box-sizing: border-box;
        line-height: 1.4;
        overflow-x: hidden;
      }
      .empty-container {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        text-align: center;
        padding: 32px 16px;
        color: var(--vscode-descriptionForeground);
      }
      .empty-icon {
        font-size: 28px;
        margin-bottom: 8px;
        opacity: 0.8;
      }
      .empty-title {
        font-size: 1.05em;
        font-weight: 600;
        margin-bottom: 6px;
        color: var(--vscode-foreground);
      }
      .empty-desc {
        font-size: 0.9em;
        line-height: 1.4;
      }
      .header-card {
        display: flex;
        flex-direction: column;
        gap: 10px;
      }
      .subject-title {
        font-size: 1.1em;
        font-weight: 700;
        color: var(--vscode-editor-foreground, var(--vscode-foreground));
        word-break: break-word;
      }
      .badges-row {
        display: flex;
        flex-wrap: wrap;
        gap: 4px;
      }
      .badge {
        display: inline-block;
        padding: 2px 6px;
        border-radius: 3px;
        font-size: 0.75em;
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.5px;
      }
      .badge-patch {
        background-color: var(--vscode-badge-background, #007acc);
        color: var(--vscode-badge-foreground, #fff);
      }
      .badge-email {
        background-color: var(--vscode-badge-background, #555);
        color: var(--vscode-badge-foreground, #fff);
      }
      .badge-unread {
        background-color: #d97706;
        color: #fff;
      }
      .badge-replied {
        background-color: #059669;
        color: #fff;
      }
      .badge-tag {
        background-color: #7c3aed;
        color: #fff;
      }
      .meta-table {
        display: flex;
        flex-direction: column;
        gap: 4px;
        padding: 8px;
        background-color: var(--vscode-editorWidget-background, rgba(255, 255, 255, 0.04));
        border-radius: 4px;
        border: 1px solid var(--vscode-widget-border, rgba(128, 128, 128, 0.2));
      }
      .field-row {
        display: flex;
        align-items: baseline;
        gap: 6px;
        font-size: 0.88em;
      }
      .field-label {
        font-weight: 600;
        color: var(--vscode-descriptionForeground, #999);
        flex-shrink: 0;
        width: 58px;
      }
      .field-value {
        flex-grow: 1;
        word-break: break-all;
        overflow-wrap: anywhere;
      }
      .field-value code {
        font-family: var(--vscode-editor-font-family, monospace);
        font-size: 0.95em;
        background: var(--vscode-textCodeBlock-background, rgba(0, 0, 0, 0.2));
        padding: 1px 3px;
        border-radius: 2px;
      }
      .lore-link {
        color: var(--vscode-textLink-foreground);
        text-decoration: none;
        margin-left: 4px;
        font-size: 0.9em;
      }
      .lore-link:hover {
        text-decoration: underline;
      }
      .btn-icon {
        background: none;
        border: none;
        cursor: pointer;
        padding: 0 2px;
        font-size: 0.9em;
        vertical-align: middle;
      }
      .warning-banner {
        background-color: rgba(234, 179, 8, 0.15);
        border: 1px solid #eab308;
        border-radius: 4px;
        padding: 8px 10px;
        display: flex;
        flex-direction: column;
        gap: 6px;
        color: #fef08a;
        font-size: 0.85em;
      }
      .patchwork-card {
        padding: 8px 10px;
        border-radius: 4px;
        background-color: var(--vscode-editorWidget-background, rgba(255, 255, 255, 0.04));
        border: 1px solid var(--vscode-widget-border, rgba(128, 128, 128, 0.2));
        font-size: 0.85em;
        display: flex;
        flex-direction: column;
        gap: 4px;
      }
      .patchwork-header {
        font-weight: 600;
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 2px;
      }
      .check-pass { color: #22c55e; font-weight: 600; }
      .check-fail { color: #ef4444; font-weight: 600; }
      .check-warn { color: #f59e0b; font-weight: 600; }
      .check-pending { color: #6b7280; font-weight: 600; }
      .files-section {
        display: flex;
        flex-direction: column;
        gap: 6px;
      }
      .files-title {
        font-size: 0.85em;
        font-weight: 600;
        color: var(--vscode-descriptionForeground);
      }
      .files-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
        max-height: 140px;
        overflow-y: auto;
      }
      .btn-file {
        text-align: left;
        background-color: var(--vscode-badge-background, rgba(255, 255, 255, 0.08));
        color: var(--vscode-foreground);
        border: 1px solid var(--vscode-widget-border, rgba(128, 128, 128, 0.2));
        border-radius: 3px;
        padding: 4px 8px;
        cursor: pointer;
        font-size: 0.85em;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        display: block;
      }
      .btn-file:hover {
        border-color: var(--vscode-focusBorder, #007acc);
        background-color: var(--vscode-list-hoverBackground, rgba(255, 255, 255, 0.12));
      }
      .actions-container {
        display: flex;
        flex-direction: column;
        gap: 6px;
        margin-top: 4px;
      }
      .actions-row {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 6px;
      }
      .btn {
        background-color: var(--vscode-button-background);
        color: var(--vscode-button-foreground);
        border: none;
        padding: 6px 10px;
        border-radius: 4px;
        cursor: pointer;
        font-size: 0.85em;
        font-weight: 600;
        text-align: center;
      }
      .btn:hover {
        background-color: var(--vscode-button-hoverBackground);
      }
      .btn-primary {
        background-color: var(--vscode-button-background);
        color: var(--vscode-button-foreground);
      }
      .btn-secondary {
        background-color: var(--vscode-button-secondaryBackground, #3a3d41);
        color: var(--vscode-button-secondaryForeground, #fff);
      }
      .btn-secondary:hover {
        background-color: var(--vscode-button-secondaryHoverBackground, #45494e);
      }
      .btn-warning {
        background-color: #ca8a04;
        color: #fff;
      }
      .btn-warning:hover {
        background-color: #a16207;
      }
      .btn-sm {
        padding: 3px 6px;
        font-size: 0.8em;
      }
    `;
  }
}

export { MessageHeaderViewProvider as MessageHeaderView };
