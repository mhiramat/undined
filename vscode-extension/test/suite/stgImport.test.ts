import * as assert from "assert";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import {
  extractMessageIdFromMailContent,
  resolveLkmlLink,
  insertLinkIntoStgTemplate,
  importPatchWithStgit,
  expandTargetGitRepoPath,
  resolveTargetGitRepo,
  registerB4Commands,
} from "../../src/commands/b4Commands";
import { mockVscode } from "../vscodeMock";

describe("StGit Patch Import (importpatch.sh logic)", () => {
  it("should extract Message-ID from raw mail headers matching getlkmllink.sh", () => {
    const rawMail = [
      "From: Kernel Developer <dev@example.com>",
      "Subject: [PATCH] tracing: fix probe lookup",
      "Message-Id: <20261108.12345-1-dev@kernel.org>\r",
      "",
      "Body text",
    ].join("\n");

    const mid = extractMessageIdFromMailContent(rawMail);
    assert.strictEqual(mid, "20261108.12345-1-dev@kernel.org");
  });

  it("should resolve LKML link and strip trailing T/#u suffix", async () => {
    const link = await resolveLkmlLink(
      "<20261108.12345-1-dev@kernel.org>",
      async (cmd, args) => {
        assert.strictEqual(cmd, "curl");
        assert.ok(args.includes("http://lkml.kernel.org/r/20261108.12345-1-dev@kernel.org"));
        return {
          stdout: "https://lore.kernel.org/linux-trace-kernel/20261108.12345-1-dev@kernel.org/T/#u\n",
          stderr: "",
        };
      }
    );

    assert.strictEqual(
      link,
      "https://lore.kernel.org/linux-trace-kernel/20261108.12345-1-dev@kernel.org/"
    );
  });

  it("should insert Link: trailer before the first commit trailer and stop at ---", () => {
    const template = [
      "From: Kernel Developer <dev@example.com>",
      "Subject: tracing: fix probe lookup",
      "",
      "Fix an issue in probe event lookup.",
      "",
      "Fixes: 123456789abc (\"tracing: initial probe support\")",
      "Signed-off-by: Kernel Developer <dev@example.com>",
      "---",
      " kernel/trace/trace_probe.c | 2 +-",
      " 1 file changed, 1 insertion(+), 1 deletion(-)",
      "",
    ].join("\n");

    const linkUrl = "https://lore.kernel.org/linux-trace-kernel/20261108.12345-1-dev@kernel.org/";
    const result = insertLinkIntoStgTemplate(template, linkUrl, "Cc: stable@vger.kernel.org");

    const expected = [
      "From: Kernel Developer <dev@example.com>",
      "Subject: tracing: fix probe lookup",
      "",
      "Fix an issue in probe event lookup.",
      "",
      `Link: ${linkUrl}`,
      "",
      "Fixes: 123456789abc (\"tracing: initial probe support\")",
      "Signed-off-by: Kernel Developer <dev@example.com>",
      "Cc: stable@vger.kernel.org",
      "",
    ].join("\n");

    assert.strictEqual(result, expected);
  });

  it("should execute the full StGit import workflow in the current branch", async () => {
    const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "undine-stg-test-"));
    const mailPath = path.join(tmpDir, "patch.eml");
    fs.writeFileSync(
      mailPath,
      [
        "From: Kernel Developer <dev@example.com>",
        "Subject: [PATCH] tracing: fix probe lookup",
        "Message-ID: <20261108.999-1-dev@kernel.org>",
        "",
        "Commit body.",
        "",
        "Signed-off-by: Kernel Developer <dev@example.com>",
      ].join("\n"),
      "utf-8"
    );

    const calls: Array<{ cmd: string; args: string[]; cwd?: string }> = [];
    let appliedTemplateContent = "";

    try {
      const res = await importPatchWithStgit({
        mailPath,
        targetRepo: tmpDir,
        ack: true,
        reject: true,
        runner: async (cmd, args, cwd) => {
          calls.push({ cmd, args, cwd });

          if (cmd === "curl") {
            return {
              stdout: "https://lore.kernel.org/all/20261108.999-1-dev@kernel.org/T/#u",
              stderr: "",
            };
          }

          if (cmd === "stg" && args[0] === "edit" && args[1]?.startsWith("--save-template=")) {
            const outPath = args[1].slice("--save-template=".length);
            fs.writeFileSync(
              outPath,
              [
                "From: Kernel Developer <dev@example.com>",
                "Subject: tracing: fix probe lookup",
                "",
                "Commit body.",
                "",
                "Signed-off-by: Kernel Developer <dev@example.com>",
                "---",
                "diffstat here",
              ].join("\n"),
              "utf-8"
            );
            return { stdout: "", stderr: "" };
          }

          if (cmd === "stg" && args[0] === "edit" && args[1] === "--file") {
            appliedTemplateContent = fs.readFileSync(args[2], "utf-8");
            return { stdout: "", stderr: "" };
          }

          if (cmd === "stg" && args[0] === "top") {
            return { stdout: "tracing-fix-probe-lookup\n", stderr: "" };
          }

          if (cmd === "git" && args[1] === "--abbrev-ref") {
            return { stdout: "probes/for-next\n", stderr: "" };
          }

          if (cmd === "git" && args[1] === "HEAD") {
            return { stdout: "abcdef1234567890\n", stderr: "" };
          }

          return { stdout: "", stderr: "" };
        },
      });

      assert.strictEqual(res.linkUrl, "https://lore.kernel.org/all/20261108.999-1-dev@kernel.org/");
      assert.strictEqual(res.branch, "probes/for-next");
      assert.strictEqual(res.patchName, "tracing-fix-probe-lookup");
      assert.strictEqual(res.headCommit, "abcdef1234567890");

      // Verify stg commands executed in exact importpatch.sh order
      const stgCalls = calls.filter((c) => c.cmd === "stg");
      assert.strictEqual(stgCalls.length, 6);
      assert.deepStrictEqual(stgCalls[0].args, ["import", "--reject", "-m", mailPath]);
      assert.ok(stgCalls[1].args[0] === "edit" && stgCalls[1].args[1].startsWith("--save-template="));
      assert.strictEqual(stgCalls[2].args[0], "edit");
      assert.strictEqual(stgCalls[2].args[1], "--file");
      assert.deepStrictEqual(stgCalls[3].args, ["edit", "--ack"]);
      assert.deepStrictEqual(stgCalls[4].args, ["edit", "--sign"]);
      assert.deepStrictEqual(stgCalls[5].args, ["top"]);

      assert.ok(
        appliedTemplateContent.includes(
          "Link: https://lore.kernel.org/all/20261108.999-1-dev@kernel.org/\n\nSigned-off-by: Kernel Developer <dev@example.com>"
        ),
        "Link: trailer should be inserted before Signed-off-by"
      );
      assert.ok(
        !appliedTemplateContent.includes("diffstat here"),
        "Lines after --- should be stripped from template"
      );
    } finally {
      fs.rmSync(tmpDir, { recursive: true, force: true });
    }
  });

  it("should expand workspace-relative, variable, and tilde targetGitRepo paths", () => {
    const wsRoot = "/workspace/wrapper";

    // Default "${workspaceFolder}"
    assert.strictEqual(
      expandTargetGitRepoPath("${workspaceFolder}", wsRoot),
      path.resolve("/workspace/wrapper")
    );

    // Sub-repository via "${workspaceFolder}/linux"
    assert.strictEqual(
      expandTargetGitRepoPath("${workspaceFolder}/linux", wsRoot),
      path.resolve("/workspace/wrapper/linux")
    );

    // Relative sub-repository path "linux" or "./linux"
    assert.strictEqual(
      expandTargetGitRepoPath("linux", wsRoot),
      path.resolve("/workspace/wrapper/linux")
    );
    assert.strictEqual(
      expandTargetGitRepoPath("./sub/linux", wsRoot),
      path.resolve("/workspace/wrapper/sub/linux")
    );

    // Home directory expansion "~" and "~/..."
    assert.strictEqual(
      expandTargetGitRepoPath("~/ksrc/linux", wsRoot),
      path.join(os.homedir(), "ksrc/linux")
    );

    // Absolute path
    assert.strictEqual(
      expandTargetGitRepoPath("/opt/src/linux", wsRoot),
      path.resolve("/opt/src/linux")
    );

    // Missing workspace folder with relative or ${workspaceFolder} setting returns ""
    assert.strictEqual(expandTargetGitRepoPath("${workspaceFolder}/linux", undefined), "");
    assert.strictEqual(expandTargetGitRepoPath("linux", undefined), "");
  });

  it("should resolve workspace-scoped targetGitRepo for applyPatch and importPatchStg", async () => {
    const origFolders = (mockVscode.workspace as any).workspaceFolders;
    const origGetConfig = mockVscode.workspace.getConfiguration;

    const folderUri = { fsPath: "/workspace/wrapper", path: "/workspace/wrapper" };
    let passedResourceScope: any = undefined;
    let configuredTargetRepo = "linux";

    (mockVscode.workspace as any).workspaceFolders = [{ uri: folderUri, name: "wrapper", index: 0 }];
    (mockVscode.workspace as any).getConfiguration = (_section?: string, scope?: any) => {
      passedResourceScope = scope;
      return {
        get: (key: string, defaultValue?: any) => {
          if (key === "targetGitRepo") {
            return configuredTargetRepo;
          }
          return defaultValue;
        },
      };
    };

    try {
      const resolved = await resolveTargetGitRepo();
      assert.strictEqual(resolved, path.resolve("/workspace/wrapper/linux"));
      assert.strictEqual(passedResourceScope, folderUri);

      configuredTargetRepo = "${workspaceFolder}/kernel/linux";
      const resolvedSub = await resolveTargetGitRepo();
      assert.strictEqual(resolvedSub, path.resolve("/workspace/wrapper/kernel/linux"));

      // Verify applyPatch command passes the resolved sub-repository path to client.applyPatch
      let appliedMsgId = "";
      let appliedRepo: string | undefined;
      const fakeClient: any = {
        applyPatch: async (mid: string, repo?: string) => {
          appliedMsgId = mid;
          appliedRepo = repo;
          return { success: true, branch: "review/test", head: "1234567890abcdef" };
        },
        abortPatchApply: async (params: any) => {
          appliedRepo = params?.target_git_repo;
          return { success: true };
        },
      };

      const fakeContext: any = { subscriptions: [] };
      registerB4Commands(fakeContext, fakeClient);

      await mockVscode.commands.executeCommand(
        "undine.applyPatch",
        "<20261108.111-1-dev@kernel.org>"
      );
      assert.strictEqual(appliedMsgId, "20261108.111-1-dev@kernel.org");
      assert.strictEqual(appliedRepo, path.resolve("/workspace/wrapper/kernel/linux"));

      appliedRepo = undefined;
      await mockVscode.commands.executeCommand("undine.abortApplyPatch");
      assert.strictEqual(appliedRepo, path.resolve("/workspace/wrapper/kernel/linux"));
    } finally {
      (mockVscode.workspace as any).workspaceFolders = origFolders;
      mockVscode.workspace.getConfiguration = origGetConfig;
    }
  });
});
