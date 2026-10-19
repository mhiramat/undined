import * as assert from "assert";
import * as vscode from "vscode";
import { AddressCompletionProvider } from "../../src/providers/addressCompletionProvider";
import { UndineRestClient } from "../../src/client/restClient";
import { TokenManager } from "../../src/client/tokenManager";
import { KnownAddress } from "../../src/client/types";

describe("AddressCompletionProvider", () => {
  let client: UndineRestClient;
  let provider: AddressCompletionProvider;

  const mockAddresses: KnownAddress[] = [
    {
      id: 1,
      name: "Kernel Developer",
      address: "dev@example.com",
      frequency: 10,
      last_seen: "2026-10-18 10:00:00",
    },
    {
      id: 2,
      name: "Reviewer One",
      address: "reviewer1@kernel.org",
      frequency: 5,
      last_seen: "2026-10-18 10:05:00",
    },
    {
      id: 3,
      name: "",
      address: "linux-trace-kernel@vger.kernel.org",
      frequency: 2,
      last_seen: "2026-10-18 09:00:00",
    },
  ];

  beforeEach(() => {
    const tm = new TokenManager();
    client = new UndineRestClient("http://127.0.0.1:8080", tm);
    client.getKnownAddresses = async (query = "") => {
      const q = query.toLowerCase();
      if (!q) return mockAddresses;
      return mockAddresses.filter(
        (a) => a.address.toLowerCase().includes(q) || a.name.toLowerCase().includes(q)
      );
    };
    provider = new AddressCompletionProvider(client);
  });

  function createMockDocument(lineText: string): vscode.TextDocument {
    return {
      lineAt: (_line: number) => ({ text: lineText }),
    } as any;
  }

  it("should provide completions when typing in To: header line", async () => {
    const doc = createMockDocument("To: dev");
    const pos = new vscode.Position(0, 7);

    const items = await provider.provideCompletionItems(
      doc,
      pos,
      {} as vscode.CancellationToken,
      {} as vscode.CompletionContext
    );

    assert.ok(items && items.length > 0, "Should return completion items");
    assert.strictEqual(items[0].insertText, '"Kernel Developer" <dev@example.com>');
    assert.ok(String(items[0].label).includes("Kernel Developer"));
  });

  it("should provide completions for subsequent recipient after comma in Cc: header line", async () => {
    const line = 'Cc: "Kernel Developer" <dev@example.com>, review';
    const doc = createMockDocument(line);
    const pos = new vscode.Position(0, line.length);

    const items = await provider.provideCompletionItems(
      doc,
      pos,
      {} as vscode.CancellationToken,
      {} as vscode.CompletionContext
    );

    assert.ok(items && items.length > 0, "Should return completion items");
    const reviewerItem = items.find((i) => String(i.insertText || "").includes("reviewer1@kernel.org"));
    assert.ok(reviewerItem, "Should suggest reviewer1@kernel.org");
    assert.strictEqual(reviewerItem!.insertText, '"Reviewer One" <reviewer1@kernel.org>');
  });

  it("should return undefined on non-address header lines or body lines", async () => {
    const subjectDoc = createMockDocument("Subject: Re: dev");
    const subjectItems = await provider.provideCompletionItems(
      subjectDoc,
      new vscode.Position(0, 16),
      {} as vscode.CancellationToken,
      {} as vscode.CompletionContext
    );
    assert.strictEqual(subjectItems, undefined, "Should not provide completions on Subject: line");

    const bodyDoc = createMockDocument("Signed-off-by: dev");
    const bodyItems = await provider.provideCompletionItems(
      bodyDoc,
      new vscode.Position(0, 18),
      {} as vscode.CancellationToken,
      {} as vscode.CompletionContext
    );
    assert.strictEqual(bodyItems, undefined, "Should not provide completions on body lines");
  });
});
