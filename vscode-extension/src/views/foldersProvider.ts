/**
 * FoldersTreeProvider: Renders mail folders in the Undine Activity Bar sidebar.
 */

import * as vscode from "vscode";
import { UndineRestClient } from "../client/restClient";
import { Folder } from "../client/types";

export class FolderTreeItem extends vscode.TreeItem {
  constructor(public readonly folder: Folder) {
    super(
      folder.name,
      vscode.TreeItemCollapsibleState.None
    );

    this.id = `folder-${folder.id}`;
    this.contextValue = "folder";

    if (folder.unread_count && folder.unread_count > 0) {
      this.description = `${folder.unread_count}`;
    }

    // Set appropriate icon based on folder name
    const lower = folder.name.toLowerCase();
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

  constructor(private readonly client: UndineRestClient) {}

  public refresh(): void {
    this._onDidChangeTreeData.fire();
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
      return this.folders.map((f) => new FolderTreeItem(f));
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
