/**
 * ThreadsTreeProvider: Renders hierarchical email thread trees in the Undine sidebar,
 * with custom color tag decorations (FileDecorationProvider + ThemeIcon coloring).
 */

import * as vscode from "vscode";
import { UndineRestClient } from "../client/restClient";
import { Folder, MessageSummary, TagDefinition } from "../client/types";

export const DEFAULT_TAGS: Record<string, TagDefinition> = {
  "$cl_1": { tag: "Red", color: "#CC0000" },
  "$cl_2": { tag: "Blue", color: "#3333FF" },
  "$cl_3": { tag: "Green", color: "#006600" },
  "$cl_4": { tag: "Gray", color: "#999999" },
  "$cl_5": { tag: "Purple", color: "#6600CC" },
  "$cl_6": { tag: "Light green", color: "#33CC00" },
  "$cl_7": { tag: "Orange", color: "#FF9900" },
  "$cl_8": { tag: "Pink", color: "#CC33CC" },
  "$cl_9": { tag: "Light blue", color: "#33CCFF" },
  "$cl_10": { tag: "Yellow", color: "#FFFF00" },
  "$label1": { tag: "Important", color: "#FF2600" },
  "$label2": { tag: "Work", color: "#FF9900" },
  "$label3": { tag: "Personal", color: "#009900" },
  "$label4": { tag: "To Do", color: "#3333FF" },
  "$label5": { tag: "Later", color: "#993399" },
};

interface PaletteEntry {
  themeColorId: string;
  hex: string;
  key?: string;
}

const THEME_COLOR_PALETTE: PaletteEntry[] = [
  { key: "$cl_1", themeColorId: "undine.tag.cl_1", hex: "#CC0000" },
  { key: "$cl_2", themeColorId: "undine.tag.cl_2", hex: "#3333FF" },
  { key: "$cl_3", themeColorId: "undine.tag.cl_3", hex: "#006600" },
  { key: "$cl_4", themeColorId: "undine.tag.cl_4", hex: "#999999" },
  { key: "$cl_5", themeColorId: "undine.tag.cl_5", hex: "#6600CC" },
  { key: "$cl_6", themeColorId: "undine.tag.cl_6", hex: "#33CC00" },
  { key: "$cl_7", themeColorId: "undine.tag.cl_7", hex: "#FF9900" },
  { key: "$cl_8", themeColorId: "undine.tag.cl_8", hex: "#CC33CC" },
  { key: "$cl_9", themeColorId: "undine.tag.cl_9", hex: "#33CCFF" },
  { key: "$cl_10", themeColorId: "undine.tag.cl_10", hex: "#FFFF00" },
  { key: "$label1", themeColorId: "undine.tag.label1", hex: "#FF2600" },
  { key: "$label2", themeColorId: "undine.tag.label2", hex: "#FF9900" },
  { key: "$label3", themeColorId: "undine.tag.label3", hex: "#009900" },
  { key: "$label4", themeColorId: "undine.tag.label4", hex: "#3333FF" },
  { key: "$label5", themeColorId: "undine.tag.label5", hex: "#993399" },
  { themeColorId: "undine.tag.teal", hex: "#14B8A6" },
  { themeColorId: "undine.tag.cyan", hex: "#06B6D4" },
  { themeColorId: "undine.tag.magenta", hex: "#EC4899" },
  { themeColorId: "undine.tag.gold", hex: "#F59E0B" },
  { themeColorId: "undine.tag.indigo", hex: "#6366F1" },
  { themeColorId: "undine.tag.coral", hex: "#F87171" },
];

export interface ResolvedMessageTag {
  key: string;
  name: string;
  color: string;
  themeColorId: string;
}

function parseHexRgb(hex: string): [number, number, number] | null {
  const clean = (hex || "").trim().replace(/^#/, "");
  if (/^[0-9a-fA-F]{3}$/.test(clean)) {
    const r = parseInt(clean[0] + clean[0], 16);
    const g = parseInt(clean[1] + clean[1], 16);
    const b = parseInt(clean[2] + clean[2], 16);
    return [r, g, b];
  }
  if (/^[0-9a-fA-F]{6}$/.test(clean)) {
    const r = parseInt(clean.slice(0, 2), 16);
    const g = parseInt(clean.slice(2, 4), 16);
    const b = parseInt(clean.slice(4, 6), 16);
    return [r, g, b];
  }
  return null;
}

/**
 * Maps a tag key and/or hex color to a contributed VS Code ThemeColor ID.
 */
export function resolveTagThemeColorId(tagKey?: string | null, tagColor?: string | null): string {
  const normKey = (tagKey || "").trim().toLowerCase();
  const normColor = (tagColor || "").trim().toUpperCase();

  if (normKey) {
    const keyEntry = THEME_COLOR_PALETTE.find(
      (e) => e.key && (e.key.toLowerCase() === normKey || e.key.replace(/^\$/, "").toLowerCase() === normKey.replace(/^\$/, ""))
    );
    if (keyEntry && (!normColor || keyEntry.hex.toUpperCase() === normColor)) {
      return keyEntry.themeColorId;
    }
  }

  const targetRgb = parseHexRgb(normColor);
  if (targetRgb) {
    let bestId = THEME_COLOR_PALETTE[0].themeColorId;
    let bestDist = Number.POSITIVE_INFINITY;
    for (const entry of THEME_COLOR_PALETTE) {
      const rgb = parseHexRgb(entry.hex);
      if (!rgb) continue;
      const dr = targetRgb[0] - rgb[0];
      const dg = targetRgb[1] - rgb[1];
      const db = targetRgb[2] - rgb[2];
      const dist = dr * dr + dg * dg + db * db;
      if (dist < bestDist) {
        bestDist = dist;
        bestId = entry.themeColorId;
        if (dist === 0) break;
      }
    }
    return bestId;
  }

  if (normKey) {
    const keyEntry = THEME_COLOR_PALETTE.find(
      (e) => e.key && (e.key.toLowerCase() === normKey || e.key.replace(/^\$/, "").toLowerCase() === normKey.replace(/^\$/, ""))
    );
    if (keyEntry) {
      return keyEntry.themeColorId;
    }
  }

  return "undine.tag.cl_2";
}

/**
 * Resolves custom color tag metadata for a MessageSummary.
 */
export function resolveMessageTag(
  message: MessageSummary,
  customTags?: Record<string, TagDefinition>
): ResolvedMessageTag | null {
  if (!message) return null;

  const mergedTags: Record<string, TagDefinition> = {
    ...DEFAULT_TAGS,
    ...(customTags || {}),
  };

  const lowerMap = new Map<string, { origKey: string; def: TagDefinition }>();
  for (const [k, v] of Object.entries(mergedTags)) {
    if (v && typeof v === "object") {
      lowerMap.set(k.toLowerCase(), { origKey: k, def: v });
      lowerMap.set(k.replace(/^\$/, "").toLowerCase(), { origKey: k, def: v });
      if (v.tag) {
        lowerMap.set(v.tag.toLowerCase(), { origKey: k, def: v });
      }
    }
  }

  // 1. Check explicit tag_key / tag_name / tag_color / tag fields from API
  const explicitKey = (message.tag_key || message.tag || "").trim();
  const explicitName = (message.tag_name || "").trim();
  const explicitColor = (message.tag_color || "").trim();

  if (explicitKey || explicitName || explicitColor) {
    const lookup =
      (explicitKey ? lowerMap.get(explicitKey.toLowerCase()) : undefined) ||
      (explicitName ? lowerMap.get(explicitName.toLowerCase()) : undefined);
    const key = explicitKey || lookup?.origKey || explicitName || "custom";
    const name = explicitName || lookup?.def.tag || explicitKey || "Tagged";
    const color = explicitColor || lookup?.def.color || "#3333FF";
    const themeColorId = resolveTagThemeColorId(key, color);
    return { key, name, color, themeColorId };
  }

  // 2. Fallback: inspect message.flags tokens against tag definitions
  if (message.flags && message.flags.trim()) {
    const tokens = message.flags.replace(/,/g, " ").trim().split(/\s+/);
    for (const tok of tokens) {
      const entry = lowerMap.get(tok.toLowerCase());
      if (entry) {
        const themeColorId = resolveTagThemeColorId(entry.origKey, entry.def.color);
        return {
          key: entry.origKey,
          name: entry.def.tag,
          color: entry.def.color,
          themeColorId,
        };
      }
    }
  }

  return null;
}

export class MessageTreeItem extends vscode.TreeItem {
  public children: MessageTreeItem[] = [];
  public readonly resolvedTag: ResolvedMessageTag | null;

  constructor(
    public readonly message: MessageSummary,
    hasChildren: boolean,
    customTags?: Record<string, TagDefinition>
  ) {
    super(
      message.subject || "(No Subject)",
      hasChildren
        ? vscode.TreeItemCollapsibleState.Expanded
        : vscode.TreeItemCollapsibleState.None
    );

    this.id = `msg-${message.id}`;
    const isAi = message.is_ai_review || message.from_name === "AI review" || message.from_addr === "ai@undine.local";
    this.contextValue = isAi ? "ai-review" : (message.is_patch ? "patch" : "message");

    this.resolvedTag = resolveMessageTag(message, customTags);

    // Format sender, date, and optional tag badge in description
    const sender = message.from_name || message.from_addr || "Unknown";
    const shortDate = message.date ? message.date.split(" ")[0] : "";
    let desc = `${sender} · ${shortDate}`;
    if (this.resolvedTag) {
      desc = `[${this.resolvedTag.name}] · ${desc}`;
    }
    if (message.has_notes) {
      desc = `● ${desc}`;
    }
    this.description = desc;

    let tooltipText =
      `**${message.subject}**\n\n` +
      `*From*: ${message.from_name ? `${message.from_name} <${message.from_addr}>` : message.from_addr}\n\n` +
      `*Date*: ${message.date}`;
    if (this.resolvedTag) {
      tooltipText += `\n\n*Tag*: **${this.resolvedTag.name}** (\`${this.resolvedTag.color}\`)`;
    }
    if (message.message_id) {
      tooltipText += `\n\n*ID*: \`${message.message_id}\``;
    }
    this.tooltip = new vscode.MarkdownString(tooltipText);

    const tagThemeColor = this.resolvedTag
      ? new vscode.ThemeColor(this.resolvedTag.themeColorId)
      : undefined;

    if (this.resolvedTag) {
      const qs =
        `themeColor=${encodeURIComponent(this.resolvedTag.themeColorId)}` +
        `&tag=${encodeURIComponent(this.resolvedTag.name)}` +
        `&color=${encodeURIComponent(this.resolvedTag.color)}` +
        `&key=${encodeURIComponent(this.resolvedTag.key)}`;
      this.resourceUri = vscode.Uri.parse(`undine-msg://message/${message.id}?${qs}`);
    }

    // Icons (colored with the message's tag ThemeColor when present)
    if (isAi) {
      this.iconPath = new vscode.ThemeIcon("hubot", tagThemeColor);
    } else if (message.is_patch) {
      this.iconPath = new vscode.ThemeIcon("diff", tagThemeColor);
    } else if (!message.is_read) {
      this.iconPath = new vscode.ThemeIcon("mail-unread", tagThemeColor);
    } else if (message.is_replied) {
      this.iconPath = new vscode.ThemeIcon("reply", tagThemeColor);
    } else {
      this.iconPath = new vscode.ThemeIcon("mail", tagThemeColor);
    }

    this.command = {
      command: "undine.selectMessage",
      title: "Open Message",
      arguments: [message],
    };
  }
}

/**
 * MessageDecorationProvider: Colors message rows in the Thread Hierarchy tree view
 * according to each message's custom color tag via VS Code's FileDecoration API.
 */
export class MessageDecorationProvider implements vscode.FileDecorationProvider {
  public static readonly SCHEME = "undine-msg";

  private readonly _onDidChangeFileDecorations = new vscode.EventEmitter<vscode.Uri | vscode.Uri[] | undefined>();
  public readonly onDidChangeFileDecorations: vscode.Event<vscode.Uri | vscode.Uri[] | undefined> =
    this._onDidChangeFileDecorations.event;

  public fireDidChange(uris?: vscode.Uri | vscode.Uri[]): void {
    this._onDidChangeFileDecorations.fire(uris);
  }

  public provideFileDecoration(uri: vscode.Uri): vscode.ProviderResult<vscode.FileDecoration> {
    if (!uri || uri.scheme !== MessageDecorationProvider.SCHEME) {
      return undefined;
    }

    const params = new URLSearchParams(uri.query || "");
    const themeColorId = params.get("themeColor");
    if (!themeColorId) {
      return undefined;
    }
    const tagName = params.get("tag") || "Tagged";
    const tagColor = params.get("color") || "";
    const tooltip = tagColor ? `Tag: ${tagName} (${tagColor})` : `Tag: ${tagName}`;

    return new vscode.FileDecoration("●", tooltip, new vscode.ThemeColor(themeColorId));
  }
}

export class ThreadsTreeProvider implements vscode.TreeDataProvider<MessageTreeItem> {
  private _onDidChangeTreeData: vscode.EventEmitter<MessageTreeItem | undefined | null | void> =
    new vscode.EventEmitter<MessageTreeItem | undefined | null | void>();
  readonly onDidChangeTreeData: vscode.Event<MessageTreeItem | undefined | null | void> =
    this._onDidChangeTreeData.event;

  public readonly decorationProvider = new MessageDecorationProvider();

  private currentFolder: Folder | null = null;
  private rootItems: MessageTreeItem[] = [];
  private flatList: MessageSummary[] = [];
  private selectedMessageId: number | null = null;
  private searchResults: MessageSummary[] | null = null;
  private cachedTags: Record<string, TagDefinition> | null = null;

  constructor(private readonly client: UndineRestClient) {}

  public refresh(): void {
    this._onDidChangeTreeData.fire();
    this.decorationProvider.fireDidChange();
  }

  public async ensureTagsLoaded(force = false): Promise<Record<string, TagDefinition>> {
    if (this.cachedTags && !force) {
      return this.cachedTags;
    }
    try {
      const apiTags = await this.client.getTags();
      if (apiTags && typeof apiTags === "object") {
        const merged: Record<string, TagDefinition> = { ...DEFAULT_TAGS, ...apiTags };
        this.cachedTags = merged;
        return merged;
      }
    } catch {
      // Fallback to built-in default tags when offline or unconfigured
    }
    if (!this.cachedTags) {
      this.cachedTags = { ...DEFAULT_TAGS };
    }
    return this.cachedTags;
  }

  public getTagsMap(): Record<string, TagDefinition> {
    return this.cachedTags ? { ...this.cachedTags } : { ...DEFAULT_TAGS };
  }

  public setTagsMap(tags: Record<string, TagDefinition>): void {
    this.cachedTags = { ...DEFAULT_TAGS, ...tags };
  }

  public updateMessageTagInPlace(id: number, tagKey: string | null): void {
    const tags = this.getTagsMap();
    const def = tagKey ? tags[tagKey] : undefined;
    const applyUpdate = (m: MessageSummary) => {
      if (m.id === id) {
        m.tag_key = tagKey || null;
        m.tag_name = def ? def.tag : (tagKey || null);
        m.tag_color = def ? def.color : null;
        m.tag = def ? def.tag : (tagKey || undefined);
      }
    };
    this.flatList.forEach(applyUpdate);
    if (this.searchResults) {
      this.searchResults.forEach(applyUpdate);
    }
    this.refresh();
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
    if (clean) {
      const found = this.flatList.find((m) => this.cleanMsgId(m.message_id) === clean);
      if (found) return found;
    }
    const num = parseInt(msgId.replace(/^msg-/, ""), 10);
    if (!isNaN(num)) {
      return this.flatList.find((m) => m.id === num);
    }
    return undefined;
  }

  public getTreeItem(element: MessageTreeItem): vscode.TreeItem {
    return element;
  }

  public async getChildren(element?: MessageTreeItem): Promise<MessageTreeItem[]> {
    if (element) {
      return element.children;
    }

    await this.ensureTagsLoaded();

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
    const items: MessageTreeItem[] = [];
    const tags = this.getTagsMap();

    // Pre-create distinct items
    for (const msg of messages) {
      const item = new MessageTreeItem(msg, false, tags);
      items.push(item);
      const cleanId = this.cleanMsgId(msg.message_id);
      if (cleanId) {
        itemMap.set(cleanId, item);
      }
    }

    // Link parents and children
    for (const item of items) {
      const parentId = this.findParentId(item.message);
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
    const refsRaw = msg.references || msg.references_header;
    if (refsRaw) {
      const refs = refsRaw.trim().split(/\s+/);
      if (refs.length > 0) {
        return this.cleanMsgId(refs[refs.length - 1]);
      }
    }
    return null;
  }
}
