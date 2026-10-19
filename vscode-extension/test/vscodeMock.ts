/**
 * Minimal VS Code API mock for standalone Node.js unit testing.
 */

export class MockUri {
  public scheme: string;
  public authority: string;
  public path: string;
  public query: string;
  public fragment: string;

  constructor(uriStr: string) {
    const url = new URL(uriStr);
    this.scheme = url.protocol.replace(/:$/, "");
    const userinfo = url.username ? (url.password ? `${url.username}:${url.password}@` : `${url.username}@`) : "";
    this.authority = userinfo + url.host;
    this.path = url.pathname;
    this.query = url.search.replace(/^\?/, "");
    this.fragment = url.hash.replace(/^#/, "");
  }

  static parse(val: string): MockUri {
    return new MockUri(val);
  }
}

export enum TreeItemCollapsibleState {
  None = 0,
  Collapsed = 1,
  Expanded = 2,
}

export class TreeItem {
  public id?: string;
  public description?: string;
  public tooltip?: any;
  public iconPath?: any;
  public command?: any;
  public contextValue?: string;

  constructor(public label: string, public collapsibleState?: TreeItemCollapsibleState) {}
}

export class ThemeIcon {
  constructor(public id: string, public color?: any) {}
}

export class ThemeColor {
  constructor(public id: string) {}
}

export class MarkdownString {
  constructor(public value = "") {}
}

export class EventEmitter<T> {
  private listeners: ((e: T) => any)[] = [];

  get event(): (listener: (e: T) => any) => { dispose: () => void } {
    return (listener) => {
      this.listeners.push(listener);
      return {
        dispose: () => {
          this.listeners = this.listeners.filter((l) => l !== listener);
        },
      };
    };
  }

  fire(data: T): void {
    for (const l of this.listeners) {
      l(data);
    }
  }
}

export const mockVscode = {
  Uri: MockUri,
  TreeItem,
  TreeItemCollapsibleState,
  ThemeIcon,
  ThemeColor,
  MarkdownString,
  EventEmitter,
  window: {
    showInformationMessage: async () => undefined,
    showErrorMessage: async () => undefined,
    showWarningMessage: async () => undefined,
    showInputBox: async () => undefined,
    createWebviewPanel: () => ({
      webview: { html: "", onDidReceiveMessage: () => ({ dispose: () => {} }) },
      onDidDispose: () => ({ dispose: () => {} }),
      dispose: () => {},
      reveal: () => {},
    }),
    registerTreeDataProvider: () => ({ dispose: () => {} }),
    registerWebviewViewProvider: () => ({ dispose: () => {} }),
  },
  env: {
    openExternal: async () => true,
    clipboard: {
      writeText: async () => {},
      readText: async () => "",
    },
  },
  commands: {
    registerCommand: () => ({ dispose: () => {} }),
    executeCommand: async () => undefined,
  },
  workspace: {
    openTextDocument: async () => ({ getText: () => "" }),
    showTextDocument: async () => undefined,
    registerTextDocumentContentProvider: () => ({ dispose: () => {} }),
    getConfiguration: () => ({
      get: (_key: string, defaultValue?: any) => defaultValue,
    }),
    onDidChangeConfiguration: () => ({ dispose: () => {} }),
  },
  comments: {
    createCommentController: () => ({
      createCommentThread: () => ({
        comments: [],
        dispose: () => {},
      }),
      dispose: () => {},
    }),
  },
  CommentMode: {
    Preview: 1,
    Editing: 2,
  },
  CommentThreadCollapsibleState: {
    Collapsed: 0,
    Expanded: 1,
  },
  Position: class Position {
    constructor(public line: number, public character: number) {}
  },
  Range: class Range {
    public start: any;
    public end: any;
    constructor(startOrStartLine: any, endOrStartCol: any, endLine?: number, endCol?: number) {
      if (typeof startOrStartLine === "number") {
        this.start = { line: startOrStartLine, character: endOrStartCol };
        this.end = { line: endLine!, character: endCol! };
      } else {
        this.start = startOrStartLine;
        this.end = endOrStartCol;
      }
    }
  },
  CompletionItem: class CompletionItem {
    public insertText?: any;
    public range?: any;
    public detail?: string;
    public documentation?: any;
    public sortText?: string;
    constructor(public label: string, public kind?: any) {}
  },
  CompletionItemKind: {
    Text: 0,
    Method: 1,
    Function: 2,
    Constructor: 3,
    Field: 4,
    Variable: 5,
    Class: 6,
    Interface: 7,
    Module: 8,
    Property: 9,
    Unit: 10,
    Value: 11,
    Enum: 12,
    Keyword: 13,
    Snippet: 14,
    Color: 15,
    File: 16,
    Reference: 17,
    Folder: 18,
    EnumMember: 19,
    Constant: 20,
    Struct: 21,
    Event: 22,
    Operator: 23,
    TypeParameter: 24,
    User: 25,
  },
  languages: {
    registerCompletionItemProvider: () => ({ dispose: () => {} }),
  },
};
