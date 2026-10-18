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
});
