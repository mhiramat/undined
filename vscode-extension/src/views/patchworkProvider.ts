/**
 * PatchworkTreeProvider: Displays patches assigned to the maintainer on Patchwork.
 */

import * as vscode from "vscode";
import { UndineRestClient } from "../client/restClient";
import { PatchworkPatch } from "../client/types";

export class PatchworkTreeItem extends vscode.TreeItem {
  constructor(public readonly patch: PatchworkPatch) {
    super(patch.name, vscode.TreeItemCollapsibleState.None);

    this.id = `pw-${patch.id}`;
    this.contextValue = "patchworkPatch";

    const delegateName = patch.delegate?.username || "unassigned";
    this.description = `#${patch.id} · ${patch.state} (${delegateName})`;

    this.tooltip = new vscode.MarkdownString(
      `**#${patch.id}: ${patch.name}**\n\n` +
      `*State*: \`${patch.state}\`\n\n` +
      `*Delegate*: ${delegateName}\n\n` +
      `*Checks*: ${patch.checks || "none"}\n\n` +
      `*MsgID*: \`${patch.msgid}\``
    );

    // Checks CI status icon
    const lowerChecks = (patch.checks || "").toLowerCase();
    if (lowerChecks.includes("pass") || lowerChecks.includes("success")) {
      this.iconPath = new vscode.ThemeIcon("check", new vscode.ThemeColor("testing.iconPassed"));
    } else if (lowerChecks.includes("fail")) {
      this.iconPath = new vscode.ThemeIcon("error", new vscode.ThemeColor("testing.iconFailed"));
    } else if (lowerChecks.includes("warn")) {
      this.iconPath = new vscode.ThemeIcon("warning", new vscode.ThemeColor("testing.iconQueued"));
    } else {
      this.iconPath = new vscode.ThemeIcon("git-pull-request");
    }

    this.command = {
      command: "undine.openPatchworkPatch",
      title: "Inspect Patch",
      arguments: [patch],
    };
  }
}

export class PatchworkTreeProvider implements vscode.TreeDataProvider<PatchworkTreeItem> {
  private _onDidChangeTreeData: vscode.EventEmitter<PatchworkTreeItem | undefined | null | void> =
    new vscode.EventEmitter<PatchworkTreeItem | undefined | null | void>();
  readonly onDidChangeTreeData: vscode.Event<PatchworkTreeItem | undefined | null | void> =
    this._onDidChangeTreeData.event;

  private patches: PatchworkPatch[] = [];

  constructor(private readonly client: UndineRestClient) {}

  public refresh(): void {
    this._onDidChangeTreeData.fire();
  }

  public getTreeItem(element: PatchworkTreeItem): vscode.TreeItem {
    return element;
  }

  public async getChildren(element?: PatchworkTreeItem): Promise<PatchworkTreeItem[]> {
    if (element) {
      return [];
    }

    try {
      this.patches = await this.client.getMyPatchworkPatches();
      return this.patches.map((p) => new PatchworkTreeItem(p));
    } catch (err: any) {
      // Patchwork might not be configured on server; don't pop up disruptive error, just show empty or message
      return [];
    }
  }
}
