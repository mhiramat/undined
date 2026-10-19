import * as assert from "assert";
import { MessageHeaderViewProvider } from "../../src/views/messageHeaderView";
import { UndineRestClient } from "../../src/client/restClient";
import { TokenManager } from "../../src/client/tokenManager";
import { MessageDetail } from "../../src/client/types";

describe("MessageHeaderViewProvider", () => {
  let client: UndineRestClient;
  let mockExtensionUri: any;

  const samplePatchDetail: MessageDetail = {
    message: {
      id: 42,
      message_id: "<20261018090000.1234-1-dev@kernel.org>",
      subject: "[PATCH v2] trace: optimize probe event lookup",
      from_addr: "dev@example.com",
      from_name: "Kernel Developer",
      date: "2026-10-18 09:00:00",
      to_addr: "linux-trace-kernel@vger.kernel.org",
      cc_addr: "reviewer@kernel.org",
      in_reply_to: "<parent-1@kernel.org>",
      is_patch: true,
      is_read: false,
      is_replied: false,
      file_path: "LKML/cur/12345:2,S",
      body_plain: "Optimize probe lookup.\n\nSigned-off-by: Kernel Developer <dev@example.com>\n---\n kernel/trace/trace_probe.c | 10 ++++++++++\n 1 file changed, 10 insertions(+)\n",
    },
    parent_info: {
      found: false,
      folder_name: "LKML",
    },
    parsed_diff: {
      is_patch: true,
      preamble: "Optimize probe lookup.",
      postscript: "-- \n2.45.0\n",
      files: [
        {
          old_path: "kernel/trace/trace_probe.c",
          new_path: "kernel/trace/trace_probe.c",
          hunks: [],
        },
      ],
    },
  };

  beforeEach(() => {
    const tm = new TokenManager();
    client = new UndineRestClient("http://127.0.0.1:8080", tm);
    mockExtensionUri = {
      scheme: "file",
      authority: "",
      path: "/mock/extension",
      toString: () => "file:///mock/extension",
    };
  });

  it("should render empty state when no message is selected", () => {
    const provider = new MessageHeaderViewProvider(mockExtensionUri, client);
    const html = provider.getHtmlForWebview();

    assert.ok(html.includes("No Message Selected"), "Should display empty placeholder title");
    assert.ok(html.includes("Select an email or patch"), "Should display instruction text");
  });

  it("should render message header details when a patch is selected", async () => {
    const provider = new MessageHeaderViewProvider(mockExtensionUri, client);
    await provider.setMessage(samplePatchDetail);

    const html = provider.getHtmlForWebview();

    // Subject & Badges
    assert.ok(html.includes("[PATCH v2] trace: optimize probe event lookup"), "Subject should be displayed");
    assert.ok(html.includes("PATCH"), "PATCH badge should be present");
    assert.ok(html.includes("UNREAD"), "UNREAD badge should be present");

    // Envelope metadata
    assert.ok(html.includes("Kernel Developer &lt;dev@example.com&gt;"), "From header should be displayed");
    assert.ok(html.includes("linux-trace-kernel@vger.kernel.org"), "To header should be displayed");
    assert.ok(html.includes("reviewer@kernel.org"), "Cc header should be displayed");
    assert.ok(html.includes("2026-10-18 09:00:00"), "Date should be displayed");

    // lore link
    assert.ok(html.includes("https://lore.kernel.org/r/20261018090000.1234-1-dev%40kernel.org"), "Lore URL should be correctly formed");

    // Missing parent banner
    assert.ok(html.includes("Parent email missing from local store"), "Missing parent warning banner should be rendered");
    assert.ok(html.includes("Hydrate via b4"), "b4 hydration button should be rendered");

    // Changed files
    assert.ok(html.includes("kernel/trace/trace_probe.c"), "Changed file should be listed");
    assert.ok(html.includes("Changed Files (1)"), "Changed file counter should be correct");

    // Action buttons
    assert.ok(html.includes("View Diff"), "View Diff button should be rendered for patches with files");
    assert.ok(html.includes("Commit Description") || html.includes("Description"), "Description button should be rendered");
    assert.ok(html.includes("Apply (b4 am)"), "Apply button should be rendered");
    assert.ok(html.includes("Compose LKML Reply"), "Compose LKML Reply button should be rendered");
  });

  it("should integrate Patchwork checks and delegation info", async () => {
    // Mock getPatchworkInfo
    client.getPatchworkInfo = async (_msgid: string) => ({
      id: 98765,
      state: "under-review",
      checks: "pass",
      delegate: { username: "maintainer" },
    });

    const provider = new MessageHeaderViewProvider(mockExtensionUri, client);
    await provider.setMessage(samplePatchDetail);

    const html = provider.getHtmlForWebview();
    assert.ok(html.includes("Patchwork:</strong> #98765"), "Patchwork ID should be rendered");
    assert.ok(html.includes("under-review"), "Patchwork state should be rendered");
    assert.ok(html.includes("PASS"), "Patchwork PASS check should be rendered");
    assert.ok(html.includes("maintainer"), "Delegate name should be rendered");
    assert.ok(html.includes("Delegate to Me"), "Delegate button should be present");
  });

  it("should clear message details and return to empty state on clear()", async () => {
    const provider = new MessageHeaderViewProvider(mockExtensionUri, client);
    await provider.setMessage(samplePatchDetail);

    assert.strictEqual(provider.getMessage()?.message.id, 42);

    provider.clear();
    assert.strictEqual(provider.getMessage(), null);

    const html = provider.getHtmlForWebview();
    assert.ok(html.includes("No Message Selected"));
  });

  it("should resolve webview view and hook up event listeners", () => {
    const provider = new MessageHeaderViewProvider(mockExtensionUri, client);
    let messageHandler: ((msg: any) => Promise<void>) | undefined;

    const mockWebviewView: any = {
      webview: {
        options: {},
        html: "",
        onDidReceiveMessage: (handler: (msg: any) => Promise<void>) => {
          messageHandler = handler;
          return { dispose: () => {} };
        },
      },
      onDidDispose: () => ({ dispose: () => {} }),
    };

    provider.resolveWebviewView(mockWebviewView, {} as any, {} as any);

    assert.ok(mockWebviewView.webview.html.includes("No Message Selected"));
    assert.ok(typeof messageHandler === "function", "Message handler should be registered");
  });

  it("should render AI review message header with reviewed target patch card and without empty lore link", async () => {
    const aiReviewDetail: MessageDetail = {
      message: {
        id: 84,
        message_id: "",
        subject: "[Review] trace: optimize probe event lookup",
        from_addr: "ai@undine.local",
        from_name: "AI review",
        date: "2026-10-18 11:00:00",
        to_addr: "dev@example.com",
        cc_addr: "",
        is_patch: false,
        is_read: false,
        is_replied: false,
        is_ai_review: true,
        file_path: "AI-reviews/cur/84:2,S",
        body_plain: "=== AI Review Report ===\n\nNo issues found.",
      },
      reviewed_message: {
        id: 42,
        message_id: "20261018090000.1234-1-dev@kernel.org",
        folder_id: 1,
        folder_name: "INBOX",
        subject: "[PATCH v2] trace: optimize probe event lookup",
      },
    };

    const provider = new MessageHeaderViewProvider(mockExtensionUri, client);
    await provider.setMessage(aiReviewDetail);

    const html = provider.getHtmlForWebview();

    // AI Review badge & Subject
    assert.ok(html.includes("AI REVIEW"), "AI REVIEW badge should be present");
    assert.ok(html.includes("[Review] trace: optimize probe event lookup"), "Subject should be displayed");

    // Local ID should be shown instead of lore link
    assert.ok(html.includes("Local #84"), "Local message index should be displayed when message_id is empty");
    assert.ok(!html.includes("https://lore.kernel.org/r/"), "Empty lore URL should not be rendered");

    // Reviewed target patch card
    assert.ok(html.includes("Reviewed Target Patch"), "Reviewed Target Patch card should be rendered");
    assert.ok(html.includes("[PATCH v2] trace: optimize probe event lookup"), "Target patch subject should be in card");
    assert.ok(html.includes("selectReviewedMessage(42)"), "Inspect button should call selectReviewedMessage with target patch ID");

    // Review report action button
    assert.ok(html.includes("View Review Report"), "View Review Report button should be rendered");
  });
});
