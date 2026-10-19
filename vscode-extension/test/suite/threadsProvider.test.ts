import * as assert from "assert";
import { ThreadsTreeProvider } from "../../src/views/threadsProvider";
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
});
