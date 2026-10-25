/**
 * FolderCommands: Handlers for syncing individual mail folders in UndineD.
 */

import * as vscode from "vscode";
import { UndineRestClient } from "../client/restClient";
import { Folder } from "../client/types";
import { FoldersTreeProvider, FolderTreeItem } from "../views/foldersProvider";
import { ThreadsTreeProvider } from "../views/threadsProvider";

export function registerFolderCommands(
  context: vscode.ExtensionContext,
  client: UndineRestClient,
  foldersProvider: FoldersTreeProvider,
  threadsProvider: ThreadsTreeProvider
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand("undine.syncFolder", async (folderOrItem?: any) => {
      try {
        let folder: Folder | undefined;

        if (folderOrItem instanceof FolderTreeItem) {
          folder = folderOrItem.folder;
        } else if (folderOrItem?.folder && typeof folderOrItem.folder.id === "number") {
          folder = folderOrItem.folder;
        } else if (folderOrItem && typeof folderOrItem.id === "number" && typeof folderOrItem.name === "string") {
          folder = folderOrItem as Folder;
        } else if (typeof folderOrItem === "number") {
          folder = foldersProvider.getFolderById(folderOrItem);
        }

        if (!folder) {
          folder = foldersProvider.getSelectedFolder() || undefined;
        }

        if (!folder) {
          const folders = await client.getFolders();
          if (!folders || folders.length === 0) {
            vscode.window.showInformationMessage("No folders available to sync.");
            return;
          }
          const picked = await vscode.window.showQuickPick(
            folders.map((f) => ({
              label: f.name,
              description: f.name.toLowerCase() === "todo" ? "Patchwork" : (f.is_imap ? "IMAP" : "Local"),
              folder: f,
            })),
            { placeHolder: "Select a folder to sync" }
          );
          if (!picked) return;
          folder = picked.folder;
        }

        if (foldersProvider.isFolderSyncing(folder.id)) {
          vscode.window.showWarningMessage(`Sync already in progress for folder "${folder.name}".`);
          return;
        }

        const lower = folder.name.toLowerCase();

        // 1. TODO Folder: Sync delegated patches from Patchwork
        if (lower === "todo") {
          foldersProvider.setFolderSyncing(folder.id, true);
          try {
            await vscode.window.withProgress(
              {
                location: vscode.ProgressLocation.Notification,
                title: `Syncing delegated patches from Patchwork for TODO...`,
                cancellable: false,
              },
              async () => {
                const res = await client.syncTodoFromPatchwork();
                const count = res.synced_count ?? 0;
                const removed = res.removed_count ?? 0;
                const total = res.total_delegated ?? count;

                if (count > 0 && removed > 0) {
                  vscode.window.showInformationMessage(
                    `TODO synced: ${count} new added, ${removed} removed patch(es) purged (${total} active).`
                  );
                } else if (count > 0) {
                  vscode.window.showInformationMessage(
                    `Synced ${count} new delegated patch(es) (${total} active) from Patchwork!`
                  );
                } else if (removed > 0) {
                  vscode.window.showInformationMessage(
                    `TODO updated: ${removed} removed patch(es) purged (${total} active).`
                  );
                } else {
                  vscode.window.showInformationMessage(
                    `All ${total} delegated TODO patches are up to date!`
                  );
                }
              }
            );
          } catch (err: any) {
            vscode.window.showErrorMessage(`TODO sync failed: ${err.message}`);
          } finally {
            foldersProvider.setFolderSyncing(folder.id, false);
            foldersProvider.refresh();
            threadsProvider.refresh();
          }
          return;
        }

        // 2. IMAP Folder: Sync full message summaries
        if (folder.is_imap || folder.sync_enabled || lower === "inbox") {
          foldersProvider.setFolderSyncing(folder.id, true);
          let pollTimer: NodeJS.Timeout | undefined;

          try {
            await vscode.window.withProgress(
              {
                location: vscode.ProgressLocation.Notification,
                title: `Syncing folder "${folder.name}"...`,
                cancellable: true,
              },
              async (progress, token) => {
                token.onCancellationRequested(async () => {
                  try {
                    await client.cancelFolderSync(folder!.id);
                  } catch {}
                });

                pollTimer = setInterval(async () => {
                  try {
                    const status = await client.getFolderSyncStatus(folder!.id);
                    if (status.total > 0) {
                      const pct = Math.round((status.synced / status.total) * 100);
                      progress.report({
                        message: `Downloading summaries: ${status.synced}/${status.total} (${pct}%)`,
                      });
                    } else if (status.synced > 0) {
                      progress.report({
                        message: `Downloaded ${status.synced} summaries...`,
                      });
                    }
                  } catch {}
                }, 1500);

                const res = await client.syncFolderFull(folder!.id);
                if (pollTimer) {
                  clearInterval(pollTimer);
                  pollTimer = undefined;
                }

                if (res.status === "cancelled") {
                  vscode.window.showInformationMessage(
                    `Summary sync cancelled: ${res.synced_count ?? 0} messages synced.`
                  );
                } else {
                  const synced = res.synced_count ?? 0;
                  const removed = res.removed_count ?? 0;
                  if (synced > 0 && removed > 0) {
                    vscode.window.showInformationMessage(
                      `Folder "${folder!.name}" synced: ${synced} new added, ${removed} removed.`
                    );
                  } else if (synced > 0) {
                    vscode.window.showInformationMessage(
                      `Folder "${folder!.name}" synced: ${synced} new messages downloaded.`
                    );
                  } else if (removed > 0) {
                    vscode.window.showInformationMessage(
                      `Folder "${folder!.name}" synced: ${removed} removed messages purged.`
                    );
                  } else {
                    vscode.window.showInformationMessage(
                      `Folder "${folder!.name}" is already up to date.`
                    );
                  }
                }
              }
            );
          } catch (err: any) {
            if (pollTimer) {
              clearInterval(pollTimer);
              pollTimer = undefined;
            }
            vscode.window.showErrorMessage(
              `Failed to sync folder "${folder.name}": ${err.message}`
            );
          } finally {
            if (pollTimer) {
              clearInterval(pollTimer);
              pollTimer = undefined;
            }
            foldersProvider.setFolderSyncing(folder.id, false);
            foldersProvider.refresh();
            threadsProvider.refresh();
          }
          return;
        }

        // 3. Local-only Folder: Refresh views and inform user
        vscode.window.showInformationMessage(
          `Folder "${folder.name}" is a local mailbox (refreshed).`
        );
        foldersProvider.refresh();
        threadsProvider.refresh();
      } catch (err: any) {
        vscode.window.showErrorMessage(`Sync command error: ${err.message}`);
      }
    })
  );
}
