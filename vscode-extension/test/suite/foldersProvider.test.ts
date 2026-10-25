import * as assert from "assert";
import { FoldersTreeProvider, FolderTreeItem } from "../../src/views/foldersProvider";
import { registerFolderCommands } from "../../src/commands/folderCommands";
import { ThreadsTreeProvider } from "../../src/views/threadsProvider";
import { UndineRestClient } from "../../src/client/restClient";
import { TokenManager } from "../../src/client/tokenManager";
import { Folder } from "../../src/client/types";
import { mockVscode } from "../vscodeMock";

describe("FoldersTreeProvider & FolderCommands", () => {
  const sampleFolders: Folder[] = [
    {
      id: 1,
      name: "INBOX",
      is_subscribed: true,
      is_system: true,
      is_imap: true,
      unread_count: 5,
      total_count: 50,
    },
    {
      id: 2,
      name: "TODO",
      is_subscribed: true,
      is_system: true,
      is_imap: false,
      unread_count: 2,
      total_count: 10,
    },
    {
      id: 3,
      name: "Drafts",
      is_subscribed: true,
      is_system: true,
      is_imap: false,
      unread_count: 0,
      total_count: 1,
    },
    {
      id: 4,
      name: "linux-trace",
      is_subscribed: true,
      is_system: false,
      is_imap: true,
      sync_enabled: true,
      unread_count: 12,
      total_count: 120,
    },
  ];

  describe("FolderTreeItem", () => {
    it("should set appropriate contextValue based on folder attributes", () => {
      const inboxItem = new FolderTreeItem(sampleFolders[0]);
      assert.strictEqual(inboxItem.contextValue, "folder-syncable");
      assert.strictEqual(inboxItem.description, "5");

      const todoItem = new FolderTreeItem(sampleFolders[1]);
      assert.strictEqual(todoItem.contextValue, "folder-todo");
      assert.strictEqual(todoItem.description, "2");

      const draftsItem = new FolderTreeItem(sampleFolders[2]);
      assert.strictEqual(draftsItem.contextValue, "folder-local");
      assert.strictEqual(draftsItem.description, undefined);

      const customImapItem = new FolderTreeItem(sampleFolders[3]);
      assert.strictEqual(customImapItem.contextValue, "folder-syncable");
      assert.strictEqual(customImapItem.description, "12");
    });

    it("should display spinning sync icon and 'syncing...' description when syncing", () => {
      const syncingItem = new FolderTreeItem(sampleFolders[0], true);
      assert.strictEqual(syncingItem.description, "syncing...");
      assert.strictEqual(syncingItem.tooltip, "Syncing INBOX...");
      assert.strictEqual((syncingItem.iconPath as any)?.id, "sync~spin");
    });
  });

  describe("FoldersTreeProvider", () => {
    let mockClient: any;
    let provider: FoldersTreeProvider;

    beforeEach(() => {
      mockClient = {
        getFolders: async () => sampleFolders,
      };
      provider = new FoldersTreeProvider(mockClient);
    });

    it("should load folders and auto-select first folder", async () => {
      const items = await provider.getChildren();
      assert.strictEqual(items.length, 4);
      assert.strictEqual(items[0].label, "INBOX");
      assert.strictEqual(provider.getSelectedFolder()?.name, "INBOX");
    });

    it("should track syncing folder state and reflect in tree items", async () => {
      await provider.getChildren();
      assert.strictEqual(provider.isFolderSyncing(1), false);

      provider.setFolderSyncing(1, true);
      assert.strictEqual(provider.isFolderSyncing(1), true);

      const items = await provider.getChildren();
      const inboxItem = items.find((i) => i.folder.id === 1);
      assert.ok(inboxItem);
      assert.strictEqual(inboxItem.description, "syncing...");

      provider.setFolderSyncing(1, false);
      assert.strictEqual(provider.isFolderSyncing(1), false);
    });

    it("should find folder by id", async () => {
      await provider.getChildren();
      const folder = provider.getFolderById(2);
      assert.ok(folder);
      assert.strictEqual(folder.name, "TODO");
    });
  });

  describe("FolderCommands: undine.syncFolder", () => {
    let mockClient: any;
    let foldersProvider: FoldersTreeProvider;
    let threadsProvider: ThreadsTreeProvider;
    let context: any;
    let infoMessages: string[] = [];
    let errorMessages: string[] = [];
    let warningMessages: string[] = [];

    beforeEach(() => {
      infoMessages = [];
      errorMessages = [];
      warningMessages = [];

      mockVscode.window.showInformationMessage = async (msg: string) => {
        infoMessages.push(msg);
        return undefined;
      };
      mockVscode.window.showErrorMessage = async (msg: string) => {
        errorMessages.push(msg);
        return undefined;
      };
      mockVscode.window.showWarningMessage = async (msg: string) => {
        warningMessages.push(msg);
        return undefined;
      };

      mockClient = {
        getFolders: async () => sampleFolders,
        syncFolderFull: async (id: number) => ({
          status: "completed",
          folder_id: id,
          folder_name: "INBOX",
          synced_count: 5,
          removed_count: 1,
        }),
        getFolderSyncStatus: async (id: number) => ({
          folder_id: id,
          folder_name: "INBOX",
          is_syncing: false,
          synced: 5,
          total: 5,
          status: "completed",
        }),
        cancelFolderSync: async () => ({ success: true }),
        syncTodoFromPatchwork: async () => ({
          synced_count: 2,
          removed_count: 0,
          total_delegated: 8,
          patches: [],
        }),
      };

      foldersProvider = new FoldersTreeProvider(mockClient);
      const tm = new TokenManager();
      threadsProvider = new ThreadsTreeProvider(new UndineRestClient("http://127.0.0.1:8080", tm));
      context = { subscriptions: [] };

      registerFolderCommands(context, mockClient, foldersProvider, threadsProvider);
    });

    it("should sync IMAP folder via syncFolderFull and report progress & results", async () => {
      await foldersProvider.getChildren();
      const inboxFolder = sampleFolders[0];

      await mockVscode.commands.executeCommand("undine.syncFolder", inboxFolder);

      assert.strictEqual(infoMessages.length, 1);
      assert.strictEqual(infoMessages[0], 'Folder "INBOX" synced: 5 new added, 1 removed.');
      assert.strictEqual(foldersProvider.isFolderSyncing(inboxFolder.id), false);
    });

    it("should sync TODO folder via syncTodoFromPatchwork and report results", async () => {
      await foldersProvider.getChildren();
      const todoFolder = sampleFolders[1];

      await mockVscode.commands.executeCommand("undine.syncFolder", todoFolder);

      assert.strictEqual(infoMessages.length, 1);
      assert.strictEqual(
        infoMessages[0],
        "Synced 2 new delegated patch(es) (8 active) from Patchwork!"
      );
      assert.strictEqual(foldersProvider.isFolderSyncing(todoFolder.id), false);
    });

    it("should refresh views and inform user for local folders", async () => {
      await foldersProvider.getChildren();
      const draftsFolder = sampleFolders[2];

      await mockVscode.commands.executeCommand("undine.syncFolder", draftsFolder);

      assert.strictEqual(infoMessages.length, 1);
      assert.strictEqual(infoMessages[0], 'Folder "Drafts" is a local mailbox (refreshed).');
    });

    it("should reject concurrent sync request if folder is already syncing", async () => {
      await foldersProvider.getChildren();
      const inboxFolder = sampleFolders[0];

      foldersProvider.setFolderSyncing(inboxFolder.id, true);

      await mockVscode.commands.executeCommand("undine.syncFolder", inboxFolder);

      assert.strictEqual(warningMessages.length, 1);
      assert.strictEqual(
        warningMessages[0],
        'Sync already in progress for folder "INBOX".'
      );
    });

    it("should handle sync error and display error message", async () => {
      await foldersProvider.getChildren();
      const inboxFolder = sampleFolders[0];

      mockClient.syncFolderFull = async () => {
        throw new Error("Connection refused by IMAP server");
      };

      await mockVscode.commands.executeCommand("undine.syncFolder", inboxFolder);

      assert.strictEqual(errorMessages.length, 1);
      assert.ok(errorMessages[0].includes("Connection refused by IMAP server"));
      assert.strictEqual(foldersProvider.isFolderSyncing(inboxFolder.id), false);
    });
  });
});
