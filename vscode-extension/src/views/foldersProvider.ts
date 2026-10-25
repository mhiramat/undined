/**
 * FoldersTreeProvider: Renders mail folders in the Undine Activity Bar sidebar.
 */

import * as vscode from "vscode";
import { UndineRestClient } from "../client/restClient";
import { Folder } from "../client/types";

export class FolderTreeItem extends vscode.TreeItem {
  constructor(public readonly folder: Folder, public readonly isSyncing: boolean = false) {
    super(
      folder.name,
      vscode.TreeItemCollapsibleState.None
    );

    this.id = `folder-${folder.id}`;

    const lower = folder.name.toLowerCase();
    if (lower === "todo") {
      this.contextValue = "folder-todo";
    } else if (folder.is_imap || folder.sync_enabled || lower === "inbox") {
      this.contextValue = "folder-syncable";
    } else {
      this.contextValue = "folder-local";
    }

    if (isSyncing) {
      this.iconPath = new vscode.ThemeIcon("sync~spin");
      this.description = "syncing...";
      this.tooltip = `Syncing ${folder.name}...`;
    } else {
      if (folder.unread_count && folder.unread_count > 0) {
        this.description = `${folder.unread_count}`;
      }
      this.tooltip = `${folder.name} (${folder.total_count ?? 0} messages, ${folder.unread_count ?? 0} unread)`;

      // Set appropriate icon based on folder name
      if (lower === "inbox") {
        this.iconPath = new vscode.ThemeIcon("inbox");
      } else if (lower.includes("draft")) {
        this.iconPath = new vscode.ThemeIcon("edit");
      } else if (lower.includes("sent")) {
        this.iconPath = new vscode.ThemeIcon("send");
      } else if (lower.includes("b4")) {
        this.iconPath = new vscode.ThemeIcon("package");
      } else if (lower.includes("todo")) {
        this.iconPath = new vscode.ThemeIcon("checklist");
      } else {
        this.iconPath = new vscode.ThemeIcon("folder");
      }
    }

    this.command = {
      command: "undine.selectFolder",
      title: "Select Folder",
      arguments: [folder],
    };
  }
}

export class FoldersTreeProvider implements vscode.TreeDataProvider<FolderTreeItem> {
  private _onDidChangeTreeData: vscode.EventEmitter<FolderTreeItem | undefined | null | void> =
    new vscode.EventEmitter<FolderTreeItem | undefined | null | void>();
  readonly onDidChangeTreeData: vscode.Event<FolderTreeItem | undefined | null | void> =
    this._onDidChangeTreeData.event;

  private folders: Folder[] = [];
  private selectedFolder: Folder | null = null;
  private syncingFolderIds: Set<number> = new Set<number>();

  constructor(private readonly client: UndineRestClient) {}

  public refresh(): void {
    this._onDidChangeTreeData.fire();
  }

  public isFolderSyncing(folderId: number): boolean {
    return this.syncingFolderIds.has(folderId);
  }

  public setFolderSyncing(folderId: number, isSyncing: boolean): void {
    if (isSyncing) {
      this.syncingFolderIds.add(folderId);
    } else {
      this.syncingFolderIds.delete(folderId);
    }
    this.refresh();
  }

  public getFolderById(id: number): Folder | undefined {
    return this.folders.find((f) => f.id === id);
  }

  public getFolders(): Folder[] {
    return this.folders;
  }

  public getTreeItem(element: FolderTreeItem): vscode.TreeItem {
    return element;
  }

  public async getChildren(element?: FolderTreeItem): Promise<FolderTreeItem[]> {
    if (element) {
      return [];
    }

    try {
      this.folders = await this.client.getFolders();
      if (!this.selectedFolder && this.folders.length > 0) {
        // Auto-select first folder (e.g. INBOX)
        this.selectedFolder = this.folders[0];
      }
      return this.folders.map((f) => new FolderTreeItem(f, this.syncingFolderIds.has(f.id)));
    } catch (err: any) {
      vscode.window.showErrorMessage(`Failed to load Undine folders: ${err.message}`);
      return [];
    }
  }

  public getSelectedFolder(): Folder | null {
    return this.selectedFolder;
  }

  public setSelectedFolder(folder: Folder): void {
    this.selectedFolder = folder;
  }
}
