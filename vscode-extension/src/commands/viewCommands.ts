/**
 * ViewCommands: Handlers for opening native patch diffs and commit descriptions.
 */

import * as vscode from "vscode";
import * as path from "path";
import { UndineRestClient } from "../client/restClient";
import { PatchContentProvider } from "../providers/patchContentProvider";

function extractMessageTarget(messageIdOrItem: any): { cleanId?: string; numericId?: number; targetKey: string } {
  let rawMsgId: string | undefined;
  let numericId: number | undefined;

  if (typeof messageIdOrItem === "number") {
    numericId = messageIdOrItem;
  } else if (typeof messageIdOrItem === "string") {
    if (/^\d+$/.test(messageIdOrItem.trim())) {
      numericId = parseInt(messageIdOrItem.trim(), 10);
    } else {
      rawMsgId = messageIdOrItem;
    }
  } else if (messageIdOrItem?.message) {
    if (messageIdOrItem.message.message_id) {
      rawMsgId = messageIdOrItem.message.message_id;
    }
    if (typeof messageIdOrItem.message.id === "number") {
      numericId = messageIdOrItem.message.id;
    }
  } else if (messageIdOrItem?.patch?.msgid) {
    rawMsgId = messageIdOrItem.patch.msgid;
    if (typeof messageIdOrItem.patch.local_message_id === "number") {
      numericId = messageIdOrItem.patch.local_message_id;
    }
  } else if (messageIdOrItem?.msgid) {
    rawMsgId = messageIdOrItem.msgid;
  } else if (typeof messageIdOrItem?.id === "number") {
    numericId = messageIdOrItem.id;
  }

  const cleanId = rawMsgId ? rawMsgId.replace(/^<|>$/g, "").trim() : undefined;
  const targetKey = cleanId || (numericId ? String(numericId) : "");
  return { cleanId, numericId, targetKey };
}

export function registerViewCommands(
  context: vscode.ExtensionContext,
  client: UndineRestClient,
  patchProvider: PatchContentProvider
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand(
      "undine.openPatchDiff",
      async (messageIdOrItem: any, filePath?: string) => {
        try {
          const { cleanId, targetKey } = extractMessageTarget(messageIdOrItem);
          if (!targetKey) return;

          const detail = await client.getMessageDetail(targetKey);
          patchProvider.setCachedDetail(targetKey, detail);

          if (!detail.parsed_diff || !detail.parsed_diff.files || detail.parsed_diff.files.length === 0) {
            vscode.window.showInformationMessage("This message does not contain any diff files.");
            return;
          }

          let targetFile = filePath;
          if (!targetFile) {
            const firstFile = detail.parsed_diff.files[0];
            targetFile = firstFile.new_path || firstFile.old_path;
          }

          const leftUri = vscode.Uri.parse(
            `${PatchContentProvider.SCHEME}://${encodeURIComponent(targetKey)}/a/${targetFile}?msgid=${encodeURIComponent(targetKey)}`
          );
          const rightUri = vscode.Uri.parse(
            `${PatchContentProvider.SCHEME}://${encodeURIComponent(targetKey)}/b/${targetFile}?msgid=${encodeURIComponent(targetKey)}`
          );

          const title = `${path.basename(targetFile)} (Review: ${cleanId ? `<${cleanId}>` : `#${targetKey}`})`;
          await vscode.commands.executeCommand("vscode.diff", leftUri, rightUri, title, {
            preview: true,
          });

          // Enforce 8-space tab display for LKML
          const activeEditor = vscode.window.activeTextEditor;
          if (activeEditor) {
            activeEditor.options.tabSize = 8;
            activeEditor.options.insertSpaces = false;
          }
        } catch (err: any) {
          vscode.window.showErrorMessage(`Failed to open patch diff: ${err.message}`);
        }
      }
    )
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("undine.openCommitDescription", async (messageIdOrItem: any) => {
      try {
        const { targetKey } = extractMessageTarget(messageIdOrItem);
        if (!targetKey) return;

        const detail = await client.getMessageDetail(targetKey);
        patchProvider.setCachedDetail(targetKey, detail);

        const descUri = vscode.Uri.parse(
          `${PatchContentProvider.SCHEME}://${encodeURIComponent(targetKey)}/DESCRIPTION?msgid=${encodeURIComponent(targetKey)}`
        );

        const doc = await vscode.workspace.openTextDocument(descUri);
        await vscode.window.showTextDocument(doc, { preview: true });

        const activeEditor = vscode.window.activeTextEditor;
        if (activeEditor) {
          activeEditor.options.tabSize = 8;
          activeEditor.options.insertSpaces = false;
        }
      } catch (err: any) {
        vscode.window.showErrorMessage(`Failed to open commit description: ${err.message}`);
      }
    })
  );
}
