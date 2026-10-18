/**
 * PatchworkCommands: Handlers for Patchwork patch self-delegation and inspection.
 */

import * as vscode from "vscode";
import { UndineRestClient } from "../client/restClient";
import { PatchworkPatch } from "../client/types";

export function registerPatchworkCommands(
  context: vscode.ExtensionContext,
  client: UndineRestClient,
  onRefreshNeeded?: () => void
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand("undine.delegateToMe", async (patchIdOrItem?: any) => {
      try {
        let patchId: number | undefined;
        if (typeof patchIdOrItem === "number") {
          patchId = patchIdOrItem;
        } else if (patchIdOrItem?.patch?.id) {
          patchId = patchIdOrItem.patch.id;
        } else if (patchIdOrItem?.id) {
          patchId = patchIdOrItem.id;
        }

        if (!patchId) {
          const input = await vscode.window.showInputBox({
            prompt: "Enter Patchwork Patch ID to delegate to yourself:",
          });
          if (input) patchId = parseInt(input, 10);
        }

        if (!patchId || isNaN(patchId)) return;

        await client.delegatePatchworkPatchToMe(patchId);
        vscode.window.showInformationMessage(`Patchwork patch #${patchId} successfully delegated to you.`);
        if (onRefreshNeeded) onRefreshNeeded();
      } catch (err: any) {
        vscode.window.showErrorMessage(`Failed to delegate patch: ${err.message}`);
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("undine.openPatchworkPatch", async (patchOrItem?: any) => {
      let patch: PatchworkPatch | undefined;
      if (patchOrItem?.patch) {
        patch = patchOrItem.patch;
      } else if (patchOrItem?.msgid) {
        patch = patchOrItem;
      }

      if (!patch) return;

      // Open local patch diff or header view
      vscode.commands.executeCommand("undine.selectMessage", patch);
    })
  );
}
