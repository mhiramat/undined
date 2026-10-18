/**
 * B4Commands: Handlers for b4 thread hydration and patch application.
 */

import * as vscode from "vscode";
import { UndineRestClient } from "../client/restClient";

export function registerB4Commands(
  context: vscode.ExtensionContext,
  client: UndineRestClient,
  onRefreshNeeded?: () => void
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand("undine.importB4", async (messageIdOrItem?: any) => {
      try {
        let msgId: string | undefined;
        if (typeof messageIdOrItem === "string") {
          msgId = messageIdOrItem;
        } else if (messageIdOrItem?.message?.message_id) {
          msgId = messageIdOrItem.message.message_id;
        }

        if (!msgId) {
          msgId = await vscode.window.showInputBox({
            prompt: "Enter Message-ID or lore.kernel.org URL to import via b4:",
            placeHolder: "e.g. 20261003...-1-author@kernel.org",
          });
        }

        if (!msgId) return;

        const cleanId = msgId.replace(/^<|>$/g, "");

        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: `Importing thread <${cleanId}> via b4...`,
            cancellable: false,
          },
          async () => {
            const res = await client.importB4(cleanId);
            vscode.window.showInformationMessage(
              `b4 import completed: ${res.imported_count} message(s) imported to b4-imports folder.`
            );
            if (onRefreshNeeded) onRefreshNeeded();
          }
        );
      } catch (err: any) {
        vscode.window.showErrorMessage(`b4 import failed: ${err.message}`);
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("undine.applyPatch", async (messageIdOrItem?: any) => {
      try {
        let msgId: string | undefined;
        if (typeof messageIdOrItem === "string") {
          msgId = messageIdOrItem;
        } else if (messageIdOrItem?.message?.message_id) {
          msgId = messageIdOrItem.message.message_id;
        }

        if (!msgId) {
          msgId = await vscode.window.showInputBox({
            prompt: "Enter Message-ID of patch series to apply:",
            placeHolder: "e.g. 20261003...-1-author@kernel.org",
          });
        }

        if (!msgId) return;

        const cleanId = msgId.replace(/^<|>$/g, "");
        const cfg = vscode.workspace.getConfiguration("undine");
        let targetRepo = cfg.get<string>("targetGitRepo");

        if (!targetRepo || targetRepo === "${workspaceFolder}") {
          const folders = vscode.workspace.workspaceFolders;
          if (folders && folders.length > 0) {
            targetRepo = folders[0].uri.fsPath;
          } else {
            targetRepo = await vscode.window.showInputBox({
              prompt: "Target Git repository path to apply patch to:",
              placeHolder: "/path/to/linux",
            });
          }
        }

        if (!targetRepo) return;

        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: `Applying patch series <${cleanId}> to ${targetRepo}...`,
            cancellable: false,
          },
          async () => {
            const res = await client.applyPatch(cleanId, targetRepo);
            if (res.success) {
              vscode.window.showInformationMessage(
                `Patch successfully applied to branch "${res.branch}" (commit ${res.head.slice(0, 10)})`
              );
            } else {
              vscode.window.showErrorMessage(`Failed to apply patch: ${res.error || res.detail || "git am error"}`);
            }
          }
        );
      } catch (err: any) {
        vscode.window.showErrorMessage(`Patch application failed: ${err.message}`);
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("undine.abortApplyPatch", async () => {
      try {
        const res = await client.abortPatchApply();
        if (res.success) {
          vscode.window.showInformationMessage("Patch application aborted successfully.");
        } else {
          vscode.window.showErrorMessage(`Abort failed: ${res.error || res.detail || "unknown error"}`);
        }
      } catch (err: any) {
        vscode.window.showErrorMessage(`Abort failed: ${err.message}`);
      }
    })
  );
}
