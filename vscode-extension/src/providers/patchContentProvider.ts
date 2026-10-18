/**
 * PatchContentProvider: Virtual document provider for undine-patch:// URIs.
 * Supplies pre-image, post-image, and commit description text for native VS Code diffing.
 */

import * as vscode from "vscode";
import { UndineRestClient } from "../client/restClient";
import { ParsedEmailDiff, MessageDetail } from "../client/types";

export class PatchContentProvider implements vscode.TextDocumentContentProvider {
  public static readonly SCHEME = "undine-patch";

  private _onDidChange: vscode.EventEmitter<vscode.Uri> = new vscode.EventEmitter<vscode.Uri>();
  readonly onDidChange: vscode.Event<vscode.Uri> = this._onDidChange.event;

  // Cache for loaded message details
  private detailCache = new Map<string, MessageDetail>();

  constructor(private readonly client: UndineRestClient) {}

  public setCachedDetail(messageId: string, detail: MessageDetail): void {
    this.detailCache.set(messageId, detail);
  }

  public async provideTextDocumentContent(uri: vscode.Uri): Promise<string> {
    // URI structure:
    // undine-patch://<messageId>/DESCRIPTION
    // undine-patch://<messageId>/a/<filePath>  (old / pre-image)
    // undine-patch://<messageId>/b/<filePath>  (new / post-image)

    const messageId = uri.authority;
    const pathParts = uri.path.replace(/^\/+/, "").split("/");

    if (!messageId) {
      return "// Invalid URI: missing message ID";
    }

    let detail = this.detailCache.get(messageId);
    if (!detail) {
      try {
        detail = await this.client.getMessageDetail(messageId);
        this.detailCache.set(messageId, detail);
      } catch (err: any) {
        return `// Error loading message ${messageId}: ${err.message}`;
      }
    }

    if (pathParts[0] === "DESCRIPTION") {
      return this.renderDescription(detail);
    }

    const side = pathParts[0]; // "a" (pre-image) or "b" (post-image)
    const targetFile = pathParts.slice(1).join("/");

    if (!detail.parsed_diff || !detail.parsed_diff.files || detail.parsed_diff.files.length === 0) {
      return detail.message.body_plain || "";
    }

    return this.renderDiffSide(detail.parsed_diff, targetFile, side === "b");
  }

  private renderDescription(detail: MessageDetail): string {
    if (detail.parsed_diff && detail.parsed_diff.preamble) {
      return detail.parsed_diff.preamble;
    }
    return detail.message.body_plain || "(No message body)";
  }

  private renderDiffSide(parsed: ParsedEmailDiff, targetFile: string, isPostImage: boolean): string {
    const matched = parsed.files.find((f) => {
      const p = isPostImage ? (f.new_path || f.old_path) : (f.old_path || f.new_path);
      return p === targetFile || p.endsWith(targetFile) || targetFile.endsWith(p);
    });

    if (!matched) {
      return `// File ${targetFile} not found in patch diff.`;
    }

    const lines: string[] = [];

    for (const hunk of matched.hunks) {
      if (hunk.paired_rows && hunk.paired_rows.length > 0) {
        for (const row of hunk.paired_rows) {
          if (isPostImage) {
            // Post-image: include additions and context
            if (row.right && row.right.type !== "empty") {
              lines.push(row.right.content);
            }
          } else {
            // Pre-image: include deletions and context
            if (row.left && row.left.type !== "empty") {
              lines.push(row.left.content);
            }
          }
        }
      } else if (hunk.unified_lines && hunk.unified_lines.length > 0) {
        for (const u of hunk.unified_lines) {
          if (isPostImage) {
            if (u.type === "ctx" || u.type === "add") {
              lines.push(u.content);
            }
          } else {
            if (u.type === "ctx" || u.type === "del") {
              lines.push(u.content);
            }
          }
        }
      }
    }

    return lines.join("\n");
  }
}
