import * as assert from "assert";
import * as vscode from "vscode";
import { PatchContentProvider } from "../../src/providers/patchContentProvider";
import { UndineRestClient } from "../../src/client/restClient";
import { TokenManager } from "../../src/client/tokenManager";
import { MessageDetail } from "../../src/client/types";

describe("PatchContentProvider", () => {
  const dummyDetail: MessageDetail = {
    message: {
      id: 1,
      message_id: "test-patch-1@kernel.org",
      subject: "[PATCH] trace: test patch",
      from_addr: "dev@example.com",
      from_name: "Kernel Developer",
      date: "2026-10-18 10:00:00",
      is_patch: true,
      is_read: true,
      is_replied: false,
      body_plain: "Commit description preamble text.\nSecond line.\n",
      to_addr: "linux-kernel@vger.kernel.org",
      cc_addr: "",
      file_path: "/dummy",
    },
    parsed_diff: {
      is_patch: true,
      preamble: "Commit description preamble text.\nSecond line.",
      postscript: "-- \n2.45.0",
      files: [
        {
          old_path: "kernel/trace/ftrace.c",
          new_path: "kernel/trace/ftrace.c",
          hunks: [
            {
              header: "@@ -50,3 +50,3 @@",
              old_start: 50,
              old_count: 3,
              new_start: 50,
              new_count: 3,
              paired_rows: [
                {
                  row_idx: 0,
                  left: { line_no: 50, content: "\told_code();", type: "del" },
                  right: { line_no: 50, content: "\tnew_code();", type: "add" },
                },
                {
                  row_idx: 1,
                  left: { line_no: 51, content: "\tcontext_line();", type: "ctx" },
                  right: { line_no: 51, content: "\tcontext_line();", type: "ctx" },
                },
              ],
              unified_lines: [],
            },
          ],
        },
      ],
    },
  };

  it("should extract commit description preamble", async () => {
    const tm = new TokenManager();
    const client = new UndineRestClient("http://127.0.0.1:8080", tm);
    const provider = new PatchContentProvider(client);
    provider.setCachedDetail("test-patch-1@kernel.org", dummyDetail);

    const uri = vscode.Uri.parse("undine-patch://test-patch-1@kernel.org/DESCRIPTION");
    const content = await provider.provideTextDocumentContent(uri);

    assert.strictEqual(content, "Commit description preamble text.\nSecond line.");
  });

  it("should reconstruct pre-image (left) lines with deletions and context", async () => {
    const tm = new TokenManager();
    const client = new UndineRestClient("http://127.0.0.1:8080", tm);
    const provider = new PatchContentProvider(client);
    provider.setCachedDetail("test-patch-1@kernel.org", dummyDetail);

    const uri = vscode.Uri.parse("undine-patch://test-patch-1@kernel.org/a/kernel/trace/ftrace.c");
    const content = await provider.provideTextDocumentContent(uri);

    assert.strictEqual(content, "\told_code();\n\tcontext_line();");
  });

  it("should reconstruct post-image (right) lines with additions and context", async () => {
    const tm = new TokenManager();
    const client = new UndineRestClient("http://127.0.0.1:8080", tm);
    const provider = new PatchContentProvider(client);
    provider.setCachedDetail("test-patch-1@kernel.org", dummyDetail);

    const uri = vscode.Uri.parse("undine-patch://test-patch-1@kernel.org/b/kernel/trace/ftrace.c");
    const content = await provider.provideTextDocumentContent(uri);

    assert.strictEqual(content, "\tnew_code();\n\tcontext_line();");
  });
});
