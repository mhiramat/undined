import * as assert from "assert";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import {
  extractMessageIdFromMailContent,
  resolveLkmlLink,
  insertLinkIntoStgTemplate,
  importPatchWithStgit,
} from "../../src/commands/b4Commands";

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
});
