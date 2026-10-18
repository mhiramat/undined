/**
 * ViewCommands: Handlers for opening native patch diffs and commit descriptions.
 */

import * as vscode from "vscode";
import * as path from "path";
import { UndineRestClient } from "../client/restClient";
import { PatchContentProvider } from "../providers/patchContentProvider";

export function registerViewCommands(
  context: vscode.ExtensionContext,
  client: UndineRestClient,
  patchProvider: PatchContentProvider
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand(
      "undine.openPatchDiff",
      async (messageId: string, filePath?: string) => {
        try {
          const cleanId = messageId.replace(/^<|>$/g, "");
          const detail = await client.getMessageDetail(cleanId);
          patchProvider.setCachedDetail(cleanId, detail);

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
            `${PatchContentProvider.SCHEME}://${encodeURIComponent(cleanId)}/a/${targetFile}`
          );
          const rightUri = vscode.Uri.parse(
            `${PatchContentProvider.SCHEME}://${encodeURIComponent(cleanId)}/b/${targetFile}`
          );

          const title = `${path.basename(targetFile)} (Review: <${cleanId}>)`;
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
    vscode.commands.registerCommand("undine.openCommitDescription", async (messageId: string) => {
      try {
        const cleanId = messageId.replace(/^<|>$/g, "");
        const detail = await client.getMessageDetail(cleanId);
        patchProvider.setCachedDetail(cleanId, detail);

        const descUri = vscode.Uri.parse(
          `${PatchContentProvider.SCHEME}://${encodeURIComponent(cleanId)}/DESCRIPTION`
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
