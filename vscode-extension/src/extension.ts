/**
 * extension.ts: Main entry point for the Undine Review Workstation VS Code extension.
 */

import * as vscode from "vscode";
import { TokenManager } from "./client/tokenManager";
import { UndineRestClient } from "./client/restClient";
import { Folder, MessageDetail } from "./client/types";
import { FoldersTreeProvider } from "./views/foldersProvider";
import { ThreadsTreeProvider } from "./views/threadsProvider";
import { PatchworkTreeProvider } from "./views/patchworkProvider";
import { PatchContentProvider } from "./providers/patchContentProvider";
import { UndineCommentController } from "./providers/commentController";
import { MessageHeaderView } from "./views/messageHeaderView";
import { registerViewCommands } from "./commands/viewCommands";
import { registerComposeCommands } from "./commands/composeCommands";
import { registerB4Commands } from "./commands/b4Commands";
import { registerPatchworkCommands } from "./commands/patchworkCommands";
import { registerUndineChatParticipant } from "./copilot/undineChatParticipant";

let pollTimer: NodeJS.Timeout | undefined;

export function activate(context: vscode.ExtensionContext) {
  const config = vscode.workspace.getConfiguration("undine");
  const serverUrl = config.get<string>("serverUrl", "http://127.0.0.1:8080");
  const authTokenPath = config.get<string>("authTokenPath", "~/.local/share/undined/auth_token");
  const pollInterval = config.get<number>("pollIntervalSeconds", 30);

  // 1. Initialize Client Services
  const tokenManager = new TokenManager({ customTokenPath: authTokenPath });
  const client = new UndineRestClient(serverUrl, tokenManager);

  // 2. Initialize Tree View Providers (Activity Bar Sidebar)
  const foldersProvider = new FoldersTreeProvider(client);
  const threadsProvider = new ThreadsTreeProvider(client);
  const patchworkProvider = new PatchworkTreeProvider(client);

  context.subscriptions.push(
    vscode.window.registerTreeDataProvider("undine.foldersView", foldersProvider),
    vscode.window.registerTreeDataProvider("undine.threadsView", threadsProvider),
    vscode.window.registerTreeDataProvider("undine.patchworkView", patchworkProvider)
  );

  // 3. Initialize Virtual Document & Comments Providers
  const patchProvider = new PatchContentProvider(client);
  context.subscriptions.push(
    vscode.workspace.registerTextDocumentContentProvider(PatchContentProvider.SCHEME, patchProvider)
  );

  const commentController = new UndineCommentController(context, client);

  // Active message tracking for Copilot and commands
  let activeDetail: MessageDetail | null = null;

  // 4. Register Internal Navigation Commands
  context.subscriptions.push(
    vscode.commands.registerCommand("undine.selectFolder", (folder: Folder) => {
      foldersProvider.setSelectedFolder(folder);
      threadsProvider.setFolder(folder);
    }),

    vscode.commands.registerCommand("undine.selectMessage", async (messageOrId: any) => {
      let cleanId = "";
      try {
        let msgId: string;

        if (typeof messageOrId === "string") {
          msgId = messageOrId;
        } else if (messageOrId?.patch?.msgid) {
          msgId = messageOrId.patch.msgid;
        } else if (messageOrId && messageOrId.message_id) {
          msgId = messageOrId.message_id;
        } else if (messageOrId && messageOrId.message && messageOrId.message.message_id) {
          msgId = messageOrId.message.message_id;
        } else if (messageOrId?.msgid) {
          msgId = messageOrId.msgid;
        } else if (typeof messageOrId === "number") {
          msgId = String(messageOrId);
        } else {
          return;
        }

        cleanId = msgId.replace(/^<|>$/g, "");
        const detail = await client.getMessageDetail(cleanId);
        activeDetail = detail;

        patchProvider.setCachedDetail(cleanId, detail);
        threadsProvider.setSelectedMessageId(detail.message.id);

        // Mark as read in background
        if (!detail.message.is_read) {
          client.setMessageRead(detail.message.id).catch(() => {});
          detail.message.is_read = true;
        }

        // Open diff editor for patch if files are present, or description
        if (detail.message.is_patch && detail.parsed_diff && detail.parsed_diff.files && detail.parsed_diff.files.length > 0) {
          const firstFile = detail.parsed_diff.files[0];
          const targetFile = firstFile.new_path || firstFile.old_path;
          vscode.commands.executeCommand("undine.openPatchDiff", cleanId, targetFile);
        } else if (detail.message.is_patch) {
          vscode.commands.executeCommand("undine.openCommitDescription", cleanId);
        }

        // Open Header Drawer panel docked beside diff editor
        MessageHeaderView.createOrShow(context.extensionUri, detail, client);

        // Load existing review comments
        await commentController.loadCommentsForMessage(cleanId);
      } catch (err: any) {
        if (cleanId && (err.message?.includes("404") || err.message?.includes("not found"))) {
          const choice = await vscode.window.showWarningMessage(
            `Patch <${cleanId}> was not found in your local mailbox. Would you like to import it from lore via b4?`,
            "Import via b4",
            "Open in lore.kernel.org"
          );
          if (choice === "Import via b4") {
            await vscode.commands.executeCommand("undine.importB4", cleanId);
            setTimeout(() => {
              vscode.commands.executeCommand("undine.selectMessage", cleanId);
            }, 1000);
          } else if (choice === "Open in lore.kernel.org") {
            vscode.env.openExternal(vscode.Uri.parse(`https://lore.kernel.org/r/${encodeURIComponent(cleanId)}`));
          }
          return;
        }
        vscode.window.showErrorMessage(`Failed to open message: ${err.message}`);
      }
    }),

    vscode.commands.registerCommand("undine.refreshFolders", () => {
      foldersProvider.refresh();
    }),

    vscode.commands.registerCommand("undine.refreshThreads", () => {
      threadsProvider.refresh();
    }),

    vscode.commands.registerCommand("undine.nextMessage", () => {
      const next = threadsProvider.getNextMessage();
      if (next) {
        vscode.commands.executeCommand("undine.selectMessage", next);
      }
    }),

    vscode.commands.registerCommand("undine.prevMessage", () => {
      const prev = threadsProvider.getPrevMessage();
      if (prev) {
        vscode.commands.executeCommand("undine.selectMessage", prev);
      }
    }),

    vscode.commands.registerCommand("undine.searchMessages", async () => {
      const query = await vscode.window.showInputBox({
        prompt: "Search Linux kernel emails (SQLite FTS5 query):",
        placeHolder: "e.g. ftrace AND kprobe",
      });

      if (!query || !query.trim()) {
        threadsProvider.setSearchResults(null);
        return;
      }

      try {
        const results = await client.search(query.trim());
        threadsProvider.setSearchResults(results);
        vscode.window.showInformationMessage(`Found ${results.length} message(s) matching "${query}".`);
      } catch (err: any) {
        vscode.window.showErrorMessage(`Search failed: ${err.message}`);
      }
    })
  );

  // 5. Register Feature Commands
  registerViewCommands(context, client, patchProvider);
  registerComposeCommands(context, client);
  registerB4Commands(context, client, () => {
    foldersProvider.refresh();
    threadsProvider.refresh();
  });
  registerPatchworkCommands(context, client, () => {
    patchworkProvider.refresh();
  });

  // 6. Register Copilot Chat Participant (@undine)
  registerUndineChatParticipant(context, client, () => activeDetail);

  // 7. Polling Timer for background updates
  if (pollInterval > 0) {
    pollTimer = setInterval(() => {
      foldersProvider.refresh();
      threadsProvider.refresh();
      patchworkProvider.refresh();
    }, pollInterval * 1000);
  }

  // 8. Watch configuration changes
  context.subscriptions.push(
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("undine.serverUrl")) {
        const newUrl = vscode.workspace.getConfiguration("undine").get<string>("serverUrl", "http://127.0.0.1:8080");
        client.setBaseUrl(newUrl);
        foldersProvider.refresh();
      }
      if (e.affectsConfiguration("undine.authTokenPath")) {
        const newPath = vscode.workspace.getConfiguration("undine").get<string>("authTokenPath", "~/.local/share/undined/auth_token");
        tokenManager.setCustomPath(newPath);
      }
    })
  );
}

export function deactivate() {
  if (pollTimer) {
    clearInterval(pollTimer);
    pollTimer = undefined;
  }
}
