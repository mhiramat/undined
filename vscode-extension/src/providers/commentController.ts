/**
 * CommentController: Integrates Undine patch comments with vscode.comments API.
 * Supports line comments on diffs and commit descriptions (DESCRIPTION:LINE).
 */

import * as vscode from "vscode";
import { UndineRestClient } from "../client/restClient";
import { PatchComment } from "../client/types";
import { PatchContentProvider } from "./patchContentProvider";

export class UndineComment implements vscode.Comment {
  public id?: number;
  public label?: string;
  public mode: vscode.CommentMode = vscode.CommentMode.Preview;
  public author: vscode.CommentAuthorInformation;
  public body: vscode.MarkdownString;

  constructor(
    public text: string,
    authorName = "You",
    public parentThread: vscode.CommentThread,
    id?: number
  ) {
    this.id = id;
    this.body = new vscode.MarkdownString(text);
    this.author = {
      name: authorName,
    };
  }
}

export class UndineCommentController {
  private controller: vscode.CommentController;
  private threadMap = new Map<string, vscode.CommentThread>(); // key: `${msgId}:${file}:${line}`

  constructor(
    private readonly context: vscode.ExtensionContext,
    private readonly client: UndineRestClient
  ) {
    this.controller = vscode.comments.createCommentController("undine-comments", "Undine Patch Review");
    this.context.subscriptions.push(this.controller);

    // Allow commenting on any line of virtual patch documents
    this.controller.commentingRangeProvider = {
      provideCommentingRanges(document: vscode.TextDocument) {
        if (document.uri.scheme !== PatchContentProvider.SCHEME) {
          return [];
        }
        const lineCount = document.lineCount;
        return [new vscode.Range(0, 0, Math.max(0, lineCount - 1), 0)];
      },
    };

    this.registerCommands();
  }

  private registerCommands(): void {
    this.context.subscriptions.push(
      vscode.commands.registerCommand("undine.createComment", async (reply: vscode.CommentReply) => {
        await this.handleCreateComment(reply);
      }),
      vscode.commands.registerCommand("undine.deleteComment", async (comment: UndineComment) => {
        await this.handleDeleteComment(comment);
      })
    );
  }

  public async loadCommentsForMessage(messageId: string): Promise<void> {
    const cleanId = messageId.replace(/^<|>$/g, "");
    try {
      const res = await this.client.getComments(cleanId);
      for (const c of res.comments) {
        this.renderComment(cleanId, c);
      }
    } catch (err: any) {
      console.error(`Failed to load comments for ${cleanId}:`, err);
    }
  }

  private renderComment(cleanId: string, comment: PatchComment): void {
    const filePath = comment.file_path || comment.file || "DESCRIPTION";
    const lineNo = comment.line_no || 1;
    const targetLine = Math.max(0, lineNo - 1);

    let docUri: vscode.Uri;
    if (filePath.toUpperCase() === "DESCRIPTION") {
      docUri = vscode.Uri.parse(
        `${PatchContentProvider.SCHEME}://${encodeURIComponent(cleanId)}/DESCRIPTION`
      );
    } else {
      docUri = vscode.Uri.parse(
        `${PatchContentProvider.SCHEME}://${encodeURIComponent(cleanId)}/b/${filePath}`
      );
    }

    const key = `${cleanId}:${filePath}:${lineNo}`;
    let thread = this.threadMap.get(key);
    const range = new vscode.Range(targetLine, 0, targetLine, 0);

    if (!thread) {
      thread = this.controller.createCommentThread(docUri, range, []);
      thread.canReply = true;
      thread.collapsibleState = vscode.CommentThreadCollapsibleState.Expanded;
      this.threadMap.set(key, thread);
    }

    const author = comment.author === "agent" ? "Undine Reviewer" : "You";
    const undineComment = new UndineComment(comment.text, author, thread, comment.id);
    thread.comments = [...thread.comments, undineComment];
  }

  private async handleCreateComment(reply: vscode.CommentReply): Promise<void> {
    const thread = reply.thread;
    const uri = thread.uri;
    const cleanId = uri.authority;
    const pathParts = uri.path.replace(/^\/+/, "").split("/");

    let filePath = "DESCRIPTION";
    if (pathParts[0] === "b") {
      filePath = pathParts.slice(1).join("/");
    }

    const lineNo = thread.range ? thread.range.start.line + 1 : 1;
    const text = reply.text.trim();
    if (!text) return;

    try {
      const res = await this.client.saveComment(cleanId, {
        file_path: filePath,
        line_no: lineNo,
        side: "new",
        text: text,
      });

      const newComment = new UndineComment(text, "You", thread, res.comment.id);
      thread.comments = [...thread.comments, newComment];
      vscode.window.showInformationMessage(`Comment saved for ${filePath}:${lineNo}`);
    } catch (err: any) {
      vscode.window.showErrorMessage(`Failed to save review comment: ${err.message}`);
    }
  }

  private async handleDeleteComment(comment: UndineComment): Promise<void> {
    const thread = comment.parentThread;
    const cleanId = thread.uri.authority;

    if (comment.id != null) {
      try {
        await this.client.deleteComment(cleanId, comment.id);
      } catch (err: any) {
        vscode.window.showErrorMessage(`Failed to delete comment: ${err.message}`);
        return;
      }
    }

    thread.comments = thread.comments.filter((c) => c !== comment);
    if (thread.comments.length === 0) {
      thread.dispose();
      for (const [k, v] of this.threadMap.entries()) {
        if (v === thread) {
          this.threadMap.delete(k);
          break;
        }
      }
    }
  }
}
