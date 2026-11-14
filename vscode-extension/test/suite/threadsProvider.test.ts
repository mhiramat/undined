import * as assert from "assert";
import * as fs from "fs";
import * as path from "path";
import * as vscode from "vscode";
import { THEME_COLOR_PALETTE, ThreadsTreeProvider } from "../../src/views/threadsProvider";
import { UndineRestClient } from "../../src/client/restClient";
import { TokenManager } from "../../src/client/tokenManager";
import { MessageSummary } from "../../src/client/types";

describe("ThreadsTreeProvider", () => {
  const sampleMessages: MessageSummary[] = [
    {
      id: 1,
      message_id: "root-1@kernel.org",
      subject: "[PATCH 0/2] trace: improve probe handling",
      from_addr: "dev@example.com",
      from_name: "Kernel Developer",
      date: "2026-10-18 10:00:00",
      is_patch: false,
      is_read: true,
      is_replied: true,
    },
    {
      id: 2,
      message_id: "patch-1@kernel.org",
      in_reply_to: "root-1@kernel.org",
      subject: "[PATCH 1/2] trace: add lockdep assertion",
      from_addr: "dev@example.com",
      from_name: "Kernel Developer",
      date: "2026-10-18 10:01:00",
      is_patch: true,
      is_read: false,
      is_replied: false,
    },
    {
      id: 3,
      message_id: "patch-2@kernel.org",
      in_reply_to: "root-1@kernel.org",
      subject: "[PATCH 2/2] trace: update selftests",
      from_addr: "dev@example.com",
      from_name: "Kernel Developer",
      date: "2026-10-18 10:02:00",
      is_patch: true,
      is_read: false,
      is_replied: false,
    },
    {
      id: 4,
      message_id: "reply-1@kernel.org",
      references: "<root-1@kernel.org> <patch-1@kernel.org>",
      subject: "Re: [PATCH 1/2] trace: add lockdep assertion",
      from_addr: "reviewer@kernel.org",
      from_name: "Reviewer",
      date: "2026-10-18 10:05:00",
      is_patch: false,
      is_read: true,
      is_replied: false,
    },
  ];

  it("should organize messages into hierarchical thread tree", () => {
    const tm = new TokenManager();
    const client = new UndineRestClient("http://127.0.0.1:8080", tm);
    const provider = new ThreadsTreeProvider(client);

    const roots = provider.buildThreadHierarchy(sampleMessages);

    // Root should be root-1@kernel.org
    assert.strictEqual(roots.length, 1);
    assert.strictEqual(roots[0].message.message_id, "root-1@kernel.org");

    // Children of root should be patch-1 and patch-2
    assert.strictEqual(roots[0].children.length, 2);
    const patch1Item = roots[0].children.find((c) => c.message.message_id === "patch-1@kernel.org");
    assert.ok(patch1Item, "patch-1 should be a child of root");

    // Child of patch-1 should be reply-1
    assert.strictEqual(patch1Item!.children.length, 1);
    assert.strictEqual(patch1Item!.children[0].message.message_id, "reply-1@kernel.org");
  });

  it("should support j / k keyboard navigation helpers", () => {
    const tm = new TokenManager();
    const client = new UndineRestClient("http://127.0.0.1:8080", tm);
    const provider = new ThreadsTreeProvider(client);

    // Mock setting folder and flat list
    (provider as any).flatList = sampleMessages;

    provider.setSelectedMessageId(1);
    const next1 = provider.getNextMessage();
    assert.strictEqual(next1?.id, 2);

    provider.setSelectedMessageId(2);
    const next2 = provider.getNextMessage();
    assert.strictEqual(next2?.id, 3);

    const prev2 = provider.getPrevMessage();
    assert.strictEqual(prev2?.id, 1);
  });

  it("should handle messages with empty message_id (e.g. AI-reviews) without collisions or duplicate tree items", () => {
    const tm = new TokenManager();
    const client = new UndineRestClient("http://127.0.0.1:8080", tm);
    const provider = new ThreadsTreeProvider(client);

    const aiReviews: MessageSummary[] = [
      {
        id: 84,
        message_id: "",
        subject: "[Review] trace: optimize probe event lookup",
        from_addr: "ai@undine.local",
        from_name: "AI review",
        date: "2026-10-18 11:00:00",
        is_patch: false,
        is_read: false,
        is_replied: false,
        is_ai_review: true,
      },
      {
        id: 85,
        message_id: "",
        subject: "[Review] mm/damon: fix race condition",
        from_addr: "ai@undine.local",
        from_name: "AI review",
        date: "2026-10-18 11:30:00",
        is_patch: false,
        is_read: true,
        is_replied: false,
        is_ai_review: true,
      },
    ];

    const roots = provider.buildThreadHierarchy(aiReviews);

    // Both messages should be distinct root items without collapsing or duplicating
    assert.strictEqual(roots.length, 2, "Should have 2 distinct root items");
    assert.strictEqual(roots[0].id, "msg-84");
    assert.strictEqual(roots[1].id, "msg-85");
    assert.strictEqual(roots[0].message.id, 84);
    assert.strictEqual(roots[1].message.id, 85);
    assert.strictEqual(roots[0].contextValue, "ai-review");
    assert.strictEqual(roots[0].command?.arguments?.[0]?.id, 84);
    assert.strictEqual(roots[1].command?.arguments?.[0]?.id, 85);

    // Flat list lookup by numeric ID
    (provider as any).flatList = aiReviews;
    const found84 = provider.findMessageByMsgId("84");
    assert.strictEqual(found84?.id, 84);

    const foundMsg85 = provider.findMessageByMsgId("msg-85");
    assert.strictEqual(foundMsg85?.id, 85);
  });

  it("should color message tree items and provide file decorations according to custom color tags", () => {
    const tm = new TokenManager();
    const client = new UndineRestClient("http://127.0.0.1:8080", tm);
    const provider = new ThreadsTreeProvider(client);

    // Register a custom user tag in addition to DEFAULT_TAGS
    provider.setTagsMap({
      "$custom_teal": { tag: "Needs Review", color: "#14B8A6" },
    });

    const taggedMessages: MessageSummary[] = [
      {
        id: 10,
        message_id: "tagged-1@kernel.org",
        subject: "[PATCH] tracing: fix ring buffer race",
        from_addr: "dev@example.com",
        from_name: "Kernel Developer",
        date: "2026-10-18 12:00:00",
        is_patch: true,
        is_read: false,
        is_replied: false,
        tag_key: "$cl_1",
        tag_name: "Red",
        tag_color: "#CC0000",
      },
      {
        id: 11,
        message_id: "tagged-2@kernel.org",
        subject: "Re: [PATCH] tracing: fix ring buffer race",
        from_addr: "reviewer@kernel.org",
        from_name: "Reviewer",
        date: "2026-10-18 12:05:00",
        is_patch: false,
        is_read: true,
        is_replied: false,
        flags: "\\Seen $custom_teal",
      },
      {
        id: 12,
        message_id: "untagged-3@kernel.org",
        subject: "[PATCH] kprobes: minor cleanup",
        from_addr: "dev@example.com",
        from_name: "Kernel Developer",
        date: "2026-10-18 12:10:00",
        is_patch: true,
        is_read: true,
        is_replied: false,
      },
    ];

    (provider as any).flatList = taggedMessages;
    const roots = provider.buildThreadHierarchy(taggedMessages);
    assert.strictEqual(roots.length, 3);

    // 1. Message with explicit tag_key / tag_name / tag_color ($cl_1 Red)
    const item1 = roots[0];
    assert.ok(item1.resolvedTag, "item1 should have a resolvedTag");
    assert.strictEqual(item1.resolvedTag?.name, "Red");
    assert.strictEqual(item1.resolvedTag?.color, "#CC0000");
    assert.strictEqual(item1.resolvedTag?.themeColorId, "undine.tag.cl1");
    assert.ok(String(item1.description).includes("[Red]"), "description should include [Red] badge");
    assert.strictEqual((item1.iconPath as any)?.color?.id, "undine.tag.cl1", "icon should use undine.tag.cl1 ThemeColor");
    assert.ok(item1.resourceUri, "tagged item should set resourceUri for FileDecorationProvider");
    assert.strictEqual(item1.resourceUri?.toString(), "undine-msg:/10/undine.tag.cl1");

    // Verify URI round-trip across Extension Host <-> Workbench IPC
    const roundTrippedUri = vscode.Uri.parse(item1.resourceUri!.toString());
    const dec1 = provider.decorationProvider.provideFileDecoration(roundTrippedUri) as any;
    assert.ok(dec1, "FileDecorationProvider should return a decoration for tagged item");
    assert.strictEqual(dec1.badge, "●");
    assert.strictEqual(dec1.color?.id, "undine.tag.cl1");
    assert.ok(dec1.tooltip?.includes("Red (#CC0000)"), "decoration tooltip should include tag name and hex color");

    // 2. Message with custom tag resolved from flags ("$custom_teal" -> "#14B8A6" -> "undine.tag.teal")
    const item2 = roots[1];
    assert.ok(item2.resolvedTag, "item2 should resolve custom tag from flags");
    assert.strictEqual(item2.resolvedTag?.name, "Needs Review");
    assert.strictEqual(item2.resolvedTag?.themeColorId, "undine.tag.teal");
    assert.strictEqual((item2.iconPath as any)?.color?.id, "undine.tag.teal");
    const dec2 = provider.decorationProvider.provideFileDecoration(item2.resourceUri!) as any;
    assert.strictEqual(dec2?.color?.id, "undine.tag.teal");

    // 3. Untagged message should have no resourceUri or icon color override
    const item3 = roots[2];
    assert.strictEqual(item3.resolvedTag, null);
    assert.strictEqual(item3.resourceUri, undefined);
    assert.strictEqual((item3.iconPath as any)?.color, undefined);

    // 4. In-place tag update should update flatList and fire decoration change event
    let decorationFired = false;
    provider.decorationProvider.onDidChangeFileDecorations(() => {
      decorationFired = true;
    });
    provider.updateMessageTagInPlace(12, "$label1");
    assert.ok(decorationFired, "updateMessageTagInPlace should fire onDidChangeFileDecorations");
    const rebuilt = provider.buildThreadHierarchy(provider.getFlatList());
    assert.strictEqual(rebuilt[2].resolvedTag?.name, "Important");
    assert.strictEqual(rebuilt[2].resolvedTag?.themeColorId, "undine.tag.label1");
  });

  it("should register valid word[.word]* ThemeColor IDs in package.json without underscores", () => {
    const pkgPath = path.resolve(__dirname, "../../../package.json");
    const pkg = JSON.parse(fs.readFileSync(pkgPath, "utf8"));
    const contributedColors: Array<{ id: string }> = pkg?.contributes?.colors || [];
    const contributedIds = new Set(contributedColors.map((c) => c.id));

    // VS Code colorExtensionPoint rejects IDs containing underscores
    const colorIdPattern = /^[a-zA-Z0-9-]+(\.[a-zA-Z0-9-]+)+$/;
    for (const c of contributedColors) {
      assert.ok(
        colorIdPattern.test(c.id),
        `contributes.colors ID "${c.id}" violates VS Code word[.word]* pattern`
      );
    }

    for (const entry of THEME_COLOR_PALETTE) {
      assert.ok(
        colorIdPattern.test(entry.themeColorId),
        `THEME_COLOR_PALETTE ID "${entry.themeColorId}" violates VS Code word[.word]* pattern`
      );
      assert.ok(
        contributedIds.has(entry.themeColorId),
        `THEME_COLOR_PALETTE ID "${entry.themeColorId}" is missing from package.json contributes.colors`
      );
    }
  });
});
