/**
 * ComposeCommands: Handlers for generating LKML quoted replies and sending emails.
 */

import * as vscode from "vscode";
import { UndineRestClient } from "../client/restClient";

export function registerComposeCommands(
  context: vscode.ExtensionContext,
  client: UndineRestClient
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand("undine.generateReplyDraft", async (messageIdOrItem?: any) => {
      try {
        let msgId: string | undefined;
        if (typeof messageIdOrItem === "string") {
          msgId = messageIdOrItem;
        } else if (messageIdOrItem?.patch?.msgid) {
          msgId = messageIdOrItem.patch.msgid;
        } else if (messageIdOrItem?.message?.message_id) {
          msgId = messageIdOrItem.message.message_id;
        } else if (messageIdOrItem?.msgid) {
          msgId = messageIdOrItem.msgid;
        }

        if (!msgId) {
          msgId = await vscode.window.showInputBox({
            prompt: "Enter Message-ID of patch or email to reply to:",
            placeHolder: "e.g. 20261003...-1-author@kernel.org",
          });
        }

        if (!msgId) return;

        const cleanId = msgId.replace(/^<|>$/g, "");
        const detail = await client.getMessageDetail(cleanId);
        const commentsRes = await client.getComments(cleanId).catch(() => ({ comments: [] }));

        const quoteRes = await client.previewQuote(cleanId, commentsRes.comments);
        const orig = detail.message;

        const cleanSubj = (orig.subject || "").trim();
        const replySubj = cleanSubj.toLowerCase().startsWith("re:") ? cleanSubj : `Re: ${cleanSubj}`;

        // Format RFC 822 draft document
        const headers: string[] = [];
        headers.push(`To: ${orig.from_addr || ""}`);
        if (orig.cc_addr) {
          headers.push(`Cc: ${orig.cc_addr}`);
        }
        headers.push(`Subject: ${replySubj}`);
        headers.push(`In-Reply-To: <${cleanId}>`);
        if (orig.references) {
          headers.push(`References: ${orig.references} <${cleanId}>`);
        } else {
          headers.push(`References: <${cleanId}>`);
        }

        const draftText = `${headers.join("\n")}\n\n${quoteRes.quote_text || quoteRes.body || ""}\n`;

        const doc = await vscode.workspace.openTextDocument({
          language: "plaintext",
          content: draftText,
        });

        await vscode.window.showTextDocument(doc, { preview: false });

        const activeEditor = vscode.window.activeTextEditor;
        if (activeEditor) {
          activeEditor.options.tabSize = 8;
          activeEditor.options.insertSpaces = false;
        }

        vscode.window.showInformationMessage("LKML reply draft generated. Review and run 'Undine: Send Email Draft' to dispatch.");
      } catch (err: any) {
        vscode.window.showErrorMessage(`Failed to generate review draft: ${err.message}`);
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("undine.sendEmail", async () => {
      const editor = vscode.window.activeTextEditor;
      if (!editor) {
        vscode.window.showErrorMessage("No active email draft editor found.");
        return;
      }

      const text = editor.document.getText();
      const splitIdx = text.indexOf("\n\n");
      if (splitIdx === -1) {
        vscode.window.showErrorMessage("Invalid email format: could not separate headers from body (missing blank line).");
        return;
      }

      const headerBlock = text.slice(0, splitIdx);
      const bodyText = text.slice(splitIdx + 2);

      const headers: Record<string, string> = {};
      for (const line of headerBlock.split("\n")) {
        const colonIdx = line.indexOf(":");
        if (colonIdx > 0) {
          const key = line.slice(0, colonIdx).trim().toLowerCase();
          const val = line.slice(colonIdx + 1).trim();
          headers[key] = val;
        }
      }

      const toAddr = headers["to"];
      const subject = headers["subject"];
      const ccAddr = headers["cc"] || null;
      const inReplyTo = headers["in-reply-to"] || null;
      const references = headers["references"] || null;

      if (!toAddr || !subject) {
        vscode.window.showErrorMessage("Missing required email headers (To, Subject).");
        return;
      }

      const confirm = await vscode.window.showWarningMessage(
        `Send email to "${toAddr}" with subject "${subject}"?`,
        { modal: true },
        "Send",
        "Cancel"
      );

      if (confirm !== "Send") return;

      try {
        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: `Sending email to ${toAddr}...`,
          },
          async () => {
            await client.sendEmail({
              to_addr: toAddr,
              cc_addr: ccAddr,
              subject: subject,
              body_text: bodyText,
              in_reply_to: inReplyTo,
              references: references,
            });
          }
        );

        vscode.window.showInformationMessage(`Email successfully dispatched via SMTP to ${toAddr}`);
      } catch (err: any) {
        vscode.window.showErrorMessage(`Failed to send email: ${err.message}`);
      }
    })
  );
}
