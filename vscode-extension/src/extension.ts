/**
 * extension.ts: Main entry point for the Undine Review Workstation VS Code extension.
 */

import * as vscode from "vscode";
import { TokenManager } from "./client/tokenManager";
import { UndineRestClient } from "./client/restClient";
import { Folder, MessageDetail } from "./client/types";
import { FoldersTreeProvider } from "./views/foldersProvider";
import { ThreadsTreeProvider } from "./views/threadsProvider";
import { MessageHeaderViewProvider } from "./views/messageHeaderView";
import { PatchContentProvider } from "./providers/patchContentProvider";
import { UndineCommentController } from "./providers/commentController";
import { AddressCompletionProvider } from "./providers/addressCompletionProvider";
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

  // 2. Initialize Sidebar View Providers (Folders, Thread Hierarchy, Message Header)
  const foldersProvider = new FoldersTreeProvider(client);
  const threadsProvider = new ThreadsTreeProvider(client);
  const headerProvider = new MessageHeaderViewProvider(context.extensionUri, client);

  context.subscriptions.push(
    vscode.window.registerTreeDataProvider("undine.foldersView", foldersProvider),
    vscode.window.registerTreeDataProvider("undine.threadsView", threadsProvider),
    vscode.window.registerWebviewViewProvider(MessageHeaderViewProvider.viewType, headerProvider)
  );

  // 3. Initialize Virtual Document & Comments Providers
  const patchProvider = new PatchContentProvider(client);
  context.subscriptions.push(
    vscode.workspace.registerTextDocumentContentProvider(PatchContentProvider.SCHEME, patchProvider)
  );

  const commentController = new UndineCommentController(context, client);

  // Address Completion Provider for To: and Cc: headers in plaintext email drafts
  const addressProvider = new AddressCompletionProvider(client);
  context.subscriptions.push(
    vscode.languages.registerCompletionItemProvider(
      { scheme: "untitled", language: "plaintext" },
      addressProvider,
      " ",
      ",",
      ":",
      "<",
      "@"
    ),
    vscode.languages.registerCompletionItemProvider(
      { scheme: "file", language: "plaintext" },
      addressProvider,
      " ",
      ",",
      ":",
      "<",
      "@"
    )
  );

  // Active message tracking for Copilot and commands
  let activeDetail: MessageDetail | null = null;

  // 4. Register Internal Navigation Commands
  context.subscriptions.push(
    vscode.commands.registerCommand("undine.selectFolder", (folder: Folder) => {
      foldersProvider.setSelectedFolder(folder);
      threadsProvider.setFolder(folder);
    }),

    vscode.commands.registerCommand("undine.selectMessage", async (messageOrId: any) => {
      let mboxIndex: number | undefined;
      let cleanMsgId: string = "";

      try {
        // 1. Check if numeric Mbox index is directly provided
        if (typeof messageOrId === "number") {
          mboxIndex = messageOrId;
        } else if (typeof messageOrId?.id === "number") {
          mboxIndex = messageOrId.id;
          if (messageOrId.message_id) {
            cleanMsgId = messageOrId.message_id.replace(/^<|>$/g, "");
          }
        } else if (typeof messageOrId?.message?.id === "number") {
          mboxIndex = messageOrId.message.id;
          if (messageOrId.message.message_id) {
            cleanMsgId = messageOrId.message.message_id.replace(/^<|>$/g, "");
          }
        } else if (typeof messageOrId?.local_message_id === "number") {
          mboxIndex = messageOrId.local_message_id;
          if (messageOrId.msgid) {
            cleanMsgId = messageOrId.msgid.replace(/^<|>$/g, "");
          }
        } else if (typeof messageOrId?.patch?.local_message_id === "number") {
          mboxIndex = messageOrId.patch.local_message_id;
          if (messageOrId.patch.msgid) {
            cleanMsgId = messageOrId.patch.msgid.replace(/^<|>$/g, "");
          }
        }

        // 2. If numeric index is not known yet, extract Message-ID and find index
        if (!mboxIndex) {
          let rawMsgId: string | undefined;
          if (typeof messageOrId === "string") {
            if (/^\d+$/.test(messageOrId.trim())) {
              mboxIndex = parseInt(messageOrId.trim(), 10);
            } else {
              rawMsgId = messageOrId;
            }
          } else if (messageOrId?.patch?.msgid) {
            rawMsgId = messageOrId.patch.msgid;
          } else if (messageOrId?.msgid) {
            rawMsgId = messageOrId.msgid;
          } else if (messageOrId?.message_id) {
            rawMsgId = messageOrId.message_id;
          }

          if (rawMsgId) {
            cleanMsgId = rawMsgId.replace(/^<|>$/g, "");
            // Look up in threadsProvider cache first
            const cached = threadsProvider.findMessageByMsgId(cleanMsgId);
            if (cached && typeof cached.id === "number") {
              mboxIndex = cached.id;
            } else {
              // Look up via API (?msgid= or /search)
              const found = await client.getMessageByMsgId(cleanMsgId);
              if (found && typeof found.id === "number") {
                mboxIndex = found.id;
              }
            }
          }
        }

        if (!mboxIndex) {
          // Message is not in local mailbox
          if (cleanMsgId) {
            const choice = await vscode.window.showWarningMessage(
              `Patch <${cleanMsgId}> was not found in your local mailbox. Would you like to import it from lore via b4?`,
              "Import via b4",
              "Open in lore.kernel.org"
            );
            if (choice === "Import via b4") {
              await vscode.commands.executeCommand("undine.importB4", cleanMsgId);
              setTimeout(() => {
                vscode.commands.executeCommand("undine.selectMessage", cleanMsgId);
              }, 1000);
            } else if (choice === "Open in lore.kernel.org") {
              vscode.env.openExternal(vscode.Uri.parse(`https://lore.kernel.org/r/${encodeURIComponent(cleanMsgId)}`));
            }
            return;
          }
          vscode.window.showErrorMessage("Failed to open message: missing message index or ID.");
          return;
        }

        // 3. Fetch message detail by numeric Mbox index
        const detail = await client.getMessageDetail(mboxIndex);
        activeDetail = detail;
        cleanMsgId = (detail.message.message_id || "").replace(/^<|>$/g, "");

        patchProvider.setCachedDetail(cleanMsgId || String(detail.message.id), detail);
        threadsProvider.setSelectedMessageId(detail.message.id);

        // Mark as read in background
        if (!detail.message.is_read) {
          client.setMessageRead(detail.message.id).catch(() => {});
          detail.message.is_read = true;
        }

        // Update Message Header pane in the sidebar
        await headerProvider.setMessage(detail);

        // Auto-open diff only if explicitly configured (disabled by default)
        const autoOpenDiff = vscode.workspace.getConfiguration("undine").get<boolean>("autoOpenDiff", false);
        if (autoOpenDiff) {
          const targetId = cleanMsgId || detail.message.id;
          if (detail.message.is_patch && detail.parsed_diff && detail.parsed_diff.files && detail.parsed_diff.files.length > 0) {
            const firstFile = detail.parsed_diff.files[0];
            const targetFile = firstFile.new_path || firstFile.old_path;
            vscode.commands.executeCommand("undine.openPatchDiff", targetId, targetFile);
          } else if (detail.message.is_patch) {
            vscode.commands.executeCommand("undine.openCommitDescription", targetId);
          }
        }

        // Load existing review comments
        if (cleanMsgId) {
          await commentController.loadCommentsForMessage(cleanMsgId);
        }
      } catch (err: any) {
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
    if (activeDetail) {
      headerProvider.setMessage(activeDetail);
    }
  });

  // 6. Register Copilot Chat Participant (@undine)
  registerUndineChatParticipant(context, client, () => activeDetail);

  // 7. Polling Timer for background updates
  if (pollInterval > 0) {
    pollTimer = setInterval(() => {
      foldersProvider.refresh();
      threadsProvider.refresh();
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
