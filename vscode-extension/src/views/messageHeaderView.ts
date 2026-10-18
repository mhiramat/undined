/**
 * MessageHeaderView: Lightweight Webview panel displaying email envelope,
 * lore hyperlinks, Patchwork status, and quick maintainer action buttons.
 */

import * as vscode from "vscode";
import { MessageDetail } from "../client/types";
import { UndineRestClient } from "../client/restClient";

export class MessageHeaderView {
  public static currentPanel: MessageHeaderView | undefined;
  private readonly panel: vscode.WebviewPanel;
  private disposables: vscode.Disposable[] = [];

  public static createOrShow(
    _extensionUri: vscode.Uri,
    detail: MessageDetail,
    client: UndineRestClient
  ): MessageHeaderView {
    const column = vscode.ViewColumn.Beside;

    if (MessageHeaderView.currentPanel) {
      MessageHeaderView.currentPanel.panel.reveal(column);
      MessageHeaderView.currentPanel.update(detail);
      return MessageHeaderView.currentPanel;
    }

    const panel = vscode.window.createWebviewPanel(
      "undineMessageHeader",
      detail.message.subject || "Message Header",
      column,
      {
        enableScripts: true,
        retainContextWhenHidden: true,
      }
    );

    MessageHeaderView.currentPanel = new MessageHeaderView(panel, detail, client);
    return MessageHeaderView.currentPanel;
  }

  private constructor(
    panel: vscode.WebviewPanel,
    private detail: MessageDetail,
    _client: UndineRestClient
  ) {
    this.panel = panel;
    this.update(detail);

    this.panel.onDidDispose(() => this.dispose(), null, this.disposables);

    this.panel.webview.onDidReceiveMessage(
      async (message) => {
        switch (message.command) {
          case "openDescription":
            vscode.commands.executeCommand("undine.openCommitDescription", this.detail.message.message_id);
            break;
          case "openDiff":
            vscode.commands.executeCommand("undine.openPatchDiff", this.detail.message.message_id, message.file);
            break;
          case "importB4":
            vscode.commands.executeCommand("undine.importB4", this.detail.message.message_id);
            break;
          case "applyPatch":
            vscode.commands.executeCommand("undine.applyPatch", this.detail.message.message_id);
            break;
          case "generateReply":
            vscode.commands.executeCommand("undine.generateReplyDraft", this.detail.message.message_id);
            break;
          case "delegateToMe":
            if (message.patchId) {
              vscode.commands.executeCommand("undine.delegateToMe", message.patchId);
            }
            break;
        }
      },
      null,
      this.disposables
    );
  }

  public update(detail: MessageDetail): void {
    this.detail = detail;
    this.panel.title = detail.message.subject || "Message Header";
    this.panel.webview.html = this.getHtmlForWebview();
  }

  public dispose(): void {
    MessageHeaderView.currentPanel = undefined;
    this.panel.dispose();
    while (this.disposables.length) {
      const x = this.disposables.pop();
      if (x) x.dispose();
    }
  }

  private getHtmlForWebview(): string {
    const m = this.detail.message;
    const cleanMsgId = m.message_id.replace(/^<|>$/g, "");
    const loreUrl = `https://lore.kernel.org/r/${encodeURIComponent(cleanMsgId)}`;

    // Missing parent banner
    let missingParentHtml = "";
    if (this.detail.parent_info && !this.detail.parent_info.found && m.in_reply_to) {
      missingParentHtml = `
        <div class="warning-banner">
          <span>⚠️ Parent email missing from local store.</span>
          <button class="btn btn-secondary" onclick="sendMessage('importB4')">Hydrate Thread via b4</button>
        </div>
      `;
    }

    // Files list
    let filesHtml = "";
    if (this.detail.parsed_diff && this.detail.parsed_diff.files && this.detail.parsed_diff.files.length > 0) {
      const fileButtons = this.detail.parsed_diff.files
        .map((f) => {
          const fn = f.new_path || f.old_path;
          return `<button class="btn btn-file" onclick="openFileDiff('${this.escapeHtml(fn)}')">📄 ${this.escapeHtml(fn)}</button>`;
        })
        .join(" ");

      filesHtml = `
        <div class="field-row">
          <span class="field-label">Changed Files:</span>
          <div class="files-container">${fileButtons}</div>
        </div>
      `;
    }

    return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <style>
    body {
      font-family: var(--vscode-font-family);
      font-size: var(--vscode-font-size);
      color: var(--vscode-foreground);
      background-color: var(--vscode-editor-background);
      margin: 0;
      padding: 16px;
      line-height: 1.5;
    }
    .header-card {
      border: 1px solid var(--vscode-widget-border, #333);
      background-color: var(--vscode-editorWidget-background, #1e1e1e);
      border-radius: 6px;
      padding: 16px;
      margin-bottom: 16px;
    }
    .subject-title {
      font-size: 1.25em;
      font-weight: 700;
      margin-bottom: 12px;
      color: var(--vscode-editor-foreground);
    }
    .field-row {
      display: flex;
      margin-bottom: 6px;
      font-size: 0.9em;
    }
    .field-label {
      width: 90px;
      font-weight: 600;
      color: var(--vscode-descriptionForeground, #999);
      flex-shrink: 0;
    }
    .field-value {
      word-break: break-all;
    }
    .field-value a {
      color: var(--vscode-textLink-foreground);
      text-decoration: none;
    }
    .field-value a:hover {
      text-decoration: underline;
    }
    .warning-banner {
      background-color: rgba(234, 179, 8, 0.15);
      border: 1px solid #eab308;
      border-radius: 4px;
      padding: 8px 12px;
      margin-bottom: 12px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      color: #fef08a;
      font-size: 0.9em;
    }
    .actions-bar {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      margin-top: 16px;
      padding-top: 12px;
      border-top: 1px solid var(--vscode-widget-border, #333);
    }
    .btn {
      background-color: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
      border: none;
      padding: 6px 12px;
      border-radius: 4px;
      cursor: pointer;
      font-size: 0.85em;
      font-weight: 600;
    }
    .btn:hover {
      background-color: var(--vscode-button-hoverBackground);
    }
    .btn-secondary {
      background-color: var(--vscode-button-secondaryBackground, #3a3d41);
      color: var(--vscode-button-secondaryForeground, #fff);
    }
    .btn-file {
      background-color: var(--vscode-badge-background, #2d2d2d);
      color: var(--vscode-badge-foreground, #ccc);
      border: 1px solid var(--vscode-widget-border, #444);
    }
    .btn-file:hover {
      border-color: var(--vscode-focusBorder, #007acc);
    }
    .files-container {
      display: flex;
      flex-wrap: wrap;
      gap: 6px;
    }
  </style>
</head>
<body>
  <div class="header-card">
    ${missingParentHtml}
    <div class="subject-title">${this.escapeHtml(m.subject)}</div>

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
      <span class="field-label">Message-ID:</span>
      <span class="field-value">
        <code>&lt;${this.escapeHtml(cleanMsgId)}&gt;</code>
        <a href="${loreUrl}" target="_blank" style="margin-left: 8px;">[lore.kernel.org ↗]</a>
      </span>
    </div>

    ${filesHtml}

    <div class="actions-bar">
      <button class="btn" onclick="sendMessage('openDescription')">📝 Commit Description</button>
      ${m.is_patch ? '<button class="btn" onclick="sendMessage(\'applyPatch\')">📥 Apply to Git (b4 am)</button>' : ""}
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
  </script>
</body>
</html>`;
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
}
