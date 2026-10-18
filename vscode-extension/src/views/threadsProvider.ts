/**
 * ThreadsTreeProvider: Renders hierarchical email thread trees in the Undine sidebar.
 */

import * as vscode from "vscode";
import { UndineRestClient } from "../client/restClient";
import { Folder, MessageSummary } from "../client/types";

export class MessageTreeItem extends vscode.TreeItem {
  public children: MessageTreeItem[] = [];

  constructor(
    public readonly message: MessageSummary,
    hasChildren: boolean
  ) {
    super(
      message.subject || "(No Subject)",
      hasChildren
        ? vscode.TreeItemCollapsibleState.Expanded
        : vscode.TreeItemCollapsibleState.None
    );

    this.id = `msg-${message.id}`;
    this.contextValue = message.is_patch ? "patch" : "message";

    // Format sender and date in description
    const sender = message.from_name || message.from_addr || "Unknown";
    const shortDate = message.date ? message.date.split(" ")[0] : "";
    let desc = `${sender} · ${shortDate}`;
    if (message.has_notes) {
      desc = `● ${desc}`;
    }
    this.description = desc;

    this.tooltip = new vscode.MarkdownString(
      `**${message.subject}**\n\n` +
      `*From*: ${message.from_name ? `${message.from_name} <${message.from_addr}>` : message.from_addr}\n\n` +
      `*Date*: ${message.date}\n\n` +
      `*ID*: \`${message.message_id}\``
    );

    // Icons
    if (message.is_patch) {
      this.iconPath = new vscode.ThemeIcon("diff");
    } else if (!message.is_read) {
      this.iconPath = new vscode.ThemeIcon("mail-unread");
    } else if (message.is_replied) {
      this.iconPath = new vscode.ThemeIcon("reply");
    } else {
      this.iconPath = new vscode.ThemeIcon("mail");
    }

    this.command = {
      command: "undine.selectMessage",
      title: "Open Message",
      arguments: [message],
    };
  }
}

export class ThreadsTreeProvider implements vscode.TreeDataProvider<MessageTreeItem> {
  private _onDidChangeTreeData: vscode.EventEmitter<MessageTreeItem | undefined | null | void> =
    new vscode.EventEmitter<MessageTreeItem | undefined | null | void>();
  readonly onDidChangeTreeData: vscode.Event<MessageTreeItem | undefined | null | void> =
    this._onDidChangeTreeData.event;

  private currentFolder: Folder | null = null;
  private rootItems: MessageTreeItem[] = [];
  private flatList: MessageSummary[] = [];
  private selectedMessageId: number | null = null;
  private searchResults: MessageSummary[] | null = null;

  constructor(private readonly client: UndineRestClient) {}

  public refresh(): void {
    this._onDidChangeTreeData.fire();
  }

  public setFolder(folder: Folder): void {
    this.currentFolder = folder;
    this.searchResults = null;
    this.refresh();
  }

  public setSearchResults(results: MessageSummary[] | null): void {
    this.searchResults = results;
    this.refresh();
  }

  public findMessageByMsgId(msgId: string): MessageSummary | undefined {
    const clean = this.cleanMsgId(msgId);
    return this.flatList.find((m) => this.cleanMsgId(m.message_id) === clean);
  }

  public getTreeItem(element: MessageTreeItem): vscode.TreeItem {
    return element;
  }

  public async getChildren(element?: MessageTreeItem): Promise<MessageTreeItem[]> {
    if (element) {
      return element.children;
    }

    if (this.searchResults !== null) {
      return this.buildThreadHierarchy(this.searchResults);
    }

    if (!this.currentFolder) {
      return [];
    }

    try {
      const messages = await this.client.getMessages(this.currentFolder.id);
      this.flatList = messages;
      this.rootItems = this.buildThreadHierarchy(messages);
      return this.rootItems;
    } catch (err: any) {
      vscode.window.showErrorMessage(`Failed to load messages: ${err.message}`);
      return [];
    }
  }

  /**
   * Constructs a hierarchical tree from messages using In-Reply-To / References.
   */
  public buildThreadHierarchy(messages: MessageSummary[]): MessageTreeItem[] {
    const itemMap = new Map<string, MessageTreeItem>();
    const roots: MessageTreeItem[] = [];

    // Pre-create items
    for (const msg of messages) {
      const cleanId = this.cleanMsgId(msg.message_id);
      const item = new MessageTreeItem(msg, false);
      itemMap.set(cleanId, item);
    }

    // Link parents and children
    for (const msg of messages) {
      const cleanId = this.cleanMsgId(msg.message_id);
      const item = itemMap.get(cleanId);
      if (!item) continue;

      const parentId = this.findParentId(msg);
      if (parentId && itemMap.has(parentId)) {
        const parent = itemMap.get(parentId)!;
        parent.children.push(item);
        parent.collapsibleState = vscode.TreeItemCollapsibleState.Expanded;
      } else {
        roots.push(item);
      }
    }

    return roots;
  }

  public getFlatList(): MessageSummary[] {
    return this.flatList;
  }

  public getSelectedMessageId(): number | null {
    return this.selectedMessageId;
  }

  public setSelectedMessageId(id: number): void {
    this.selectedMessageId = id;
  }

  public getNextMessage(): MessageSummary | null {
    if (this.flatList.length === 0) return null;
    if (this.selectedMessageId === null) return this.flatList[0];

    const idx = this.flatList.findIndex((m) => m.id === this.selectedMessageId);
    if (idx >= 0 && idx + 1 < this.flatList.length) {
      return this.flatList[idx + 1];
    }
    return null;
  }

  public getPrevMessage(): MessageSummary | null {
    if (this.flatList.length === 0) return null;
    if (this.selectedMessageId === null) return this.flatList[0];

    const idx = this.flatList.findIndex((m) => m.id === this.selectedMessageId);
    if (idx > 0) {
      return this.flatList[idx - 1];
    }
    return null;
  }

  private cleanMsgId(id?: string): string {
    if (!id) return "";
    return id.trim().replace(/^<|>$/g, "");
  }

  private findParentId(msg: MessageSummary): string | null {
    if (msg.in_reply_to) {
      return this.cleanMsgId(msg.in_reply_to);
    }
    if (msg.references) {
      const refs = msg.references.trim().split(/\s+/);
      if (refs.length > 0) {
        return this.cleanMsgId(refs[refs.length - 1]);
      }
    }
    return null;
  }
}
