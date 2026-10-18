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
  Range: class {
    constructor(
      public startLine: number,
      public startCol: number,
      public endLine: number,
      public endCol: number
    ) {}
    get start() {
      return { line: this.startLine, character: this.startCol };
    }
  },
};
