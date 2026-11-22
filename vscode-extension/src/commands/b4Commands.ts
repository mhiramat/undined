/**
 * B4Commands: Handlers for b4 thread hydration and StGit patch application.
 */

import * as vscode from "vscode";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { execFile } from "child_process";
import { UndineRestClient } from "../client/restClient";
import { MessageHeaderViewProvider } from "../views/messageHeaderView";

const TRAILER_LINE_REGEX =
  /^(Signed-off-by|Suggested-by|Acked-by|Link|Cc|Fixes|Reported-by|Reviewed-by|Tested-by|Assisted-by):/;

export type ExecFileRunner = (
  cmd: string,
  args: string[],
  cwd?: string
) => Promise<{ stdout: string; stderr: string }>;

/**
 * Builds a PATH string and resolves bare executable names across standard user/system
 * binary directories so commands like `stg`, `git`, and `curl` succeed even when the
 * VS Code extension host starts with a minimal PATH.
 */
export function resolveCommandExecutable(
  cmd: string,
  envPath: string = process.env.PATH || ""
): { resolvedCmd: string; augmentedPath: string } {
  const home = os.homedir();
  const extraDirs = [
    path.join(home, ".cargo", "bin"),
    path.join(home, ".local", "bin"),
    "/usr/local/bin",
    "/usr/bin",
    "/bin",
  ];
  const existingDirs = envPath.split(path.delimiter).filter(Boolean);
  const searchDirs = [...existingDirs];
  for (const dir of extraDirs) {
    if (!searchDirs.includes(dir)) {
      searchDirs.push(dir);
    }
  }
  const augmentedPath = searchDirs.join(path.delimiter);

  if (cmd.includes("/") || cmd.includes("\\") || path.isAbsolute(cmd)) {
    return { resolvedCmd: cmd, augmentedPath };
  }

  for (const dir of searchDirs) {
    const candidate = path.join(dir, cmd);
    try {
      const st = fs.statSync(candidate);
      if (st.isFile()) {
        fs.accessSync(candidate, fs.constants.X_OK);
        return { resolvedCmd: candidate, augmentedPath };
      }
    } catch {
      // Continue searching
    }
  }

  return { resolvedCmd: cmd, augmentedPath };
}

export function defaultExecFileRunner(
  cmd: string,
  args: string[],
  cwd?: string
): Promise<{ stdout: string; stderr: string }> {
  return new Promise((resolve, reject) => {
    if (cwd !== undefined) {
      let cwdStat: fs.Stats | undefined;
      try {
        cwdStat = fs.statSync(cwd);
      } catch {
        cwdStat = undefined;
      }
      if (!cwdStat || !cwdStat.isDirectory()) {
        reject(
          new Error(
            `${cmd} ${args.join(" ")} failed: target repository directory "${cwd}" does not exist (check undine.targetGitRepo)`
          )
        );
        return;
      }
    }

    const { resolvedCmd, augmentedPath } = resolveCommandExecutable(cmd);
    const childEnv = { ...process.env, PATH: augmentedPath };

    execFile(
      resolvedCmd,
      args,
      { cwd, env: childEnv, maxBuffer: 10 * 1024 * 1024 },
      (err, stdout, stderr) => {
        if (err) {
          if ((err as NodeJS.ErrnoException).code === "ENOENT") {
            if (cwd && !fs.existsSync(cwd)) {
              reject(
                new Error(
                  `${cmd} ${args.join(" ")} failed: target repository directory "${cwd}" does not exist (check undine.targetGitRepo)`
                )
              );
              return;
            }
            reject(
              new Error(
                `${cmd} ${args.join(" ")} failed: executable "${cmd}" not found in PATH (${augmentedPath})`
              )
            );
            return;
          }
          const detail = (stderr || stdout || err.message || "").trim();
          reject(new Error(`${cmd} ${args.join(" ")} failed: ${detail}`));
          return;
        }
        resolve({ stdout: stdout || "", stderr: stderr || "" });
      }
    );
  });
}

/**
 * Extracts Message-ID from raw email content, matching getlkmllink.sh:
 * grep -i ^"Message-Id:" "$1" | cut -f2 -d: | tr -d '\r <>'
 */
export function extractMessageIdFromMailContent(rawContent: string): string {
  const lines = rawContent.split(/\r?\n/);
  for (const line of lines) {
    if (line === "") {
      break;
    }
    if (/^message-id:/i.test(line)) {
      const idx = line.indexOf(":");
      if (idx !== -1) {
        return line
          .slice(idx + 1)
          .replace(/[\r <>]/g, "")
          .trim();
      }
    }
  }
  return "";
}

/**
 * Resolves the LKML archive URL for a Message-ID or mail file, matching getlkmllink.sh:
 * URL=`curl -Ls -o /dev/null -w %{url_effective} http://lkml.kernel.org/r/$MID`
 * echo ${URL%T/#u}
 */
export async function resolveLkmlLink(
  midOrMailPath: string,
  runner: ExecFileRunner = defaultExecFileRunner
): Promise<string> {
  let mid = midOrMailPath.trim();
  if (fs.existsSync(mid)) {
    try {
      const raw = fs.readFileSync(mid, "utf-8");
      const extracted = extractMessageIdFromMailContent(raw);
      if (extracted) {
        mid = extracted;
      }
    } catch {
      // Ignore read errors and fall back to string value
    }
  }
  mid = mid.replace(/[\r <>]/g, "").trim();
  if (!mid) {
    return "";
  }

  try {
    const { stdout } = await runner("curl", [
      "-Ls",
      "-o",
      "/dev/null",
      "-w",
      "%{url_effective}",
      `http://lkml.kernel.org/r/${mid}`,
    ]);
    const effective = stdout.trim();
    if (effective) {
      return effective.replace(/T\/#u$/, "");
    }
  } catch {
    // Fallback if curl fails or network is unavailable
  }

  return `http://lkml.kernel.org/r/${mid}`;
}

/**
 * Transforms an StGit patch template by inserting `Link: <linkUrl>` before the first
 * commit trailer and stopping at `---`, matching the awk logic in importpatch.sh:
 * awk 'BEGIN{s=0} /^---$/{exit 0} /^(Signed-off-by|Suggested-by|Acked-by|Link|Cc|Fixes|Reported-by|Reviewed-by|Tested-by|Assisted-by):/{if (s == 0) {print "Link: '$LINK'\n"; s=1}} //{print $0}'
 */
export function insertLinkIntoStgTemplate(
  templateContent: string,
  linkUrl: string,
  addLine?: string
): string {
  const normalized = templateContent.replace(/\r\n/g, "\n");
  const lines = normalized.split("\n");
  if (lines.length > 0 && lines[lines.length - 1] === "") {
    lines.pop();
  }

  const outLines: string[] = [];
  let inserted = false;

  for (const line of lines) {
    if (line === "---") {
      break;
    }
    if (!inserted && linkUrl && TRAILER_LINE_REGEX.test(line)) {
      outLines.push(`Link: ${linkUrl}`);
      outLines.push("");
      inserted = true;
    }
    outLines.push(line);
  }

  if (addLine) {
    outLines.push(addLine);
  }

  return outLines.join("\n") + "\n";
}

export interface StgImportOptions {
  mailPath: string;
  targetRepo: string;
  messageId?: string;
  ack?: boolean;
  reject?: boolean;
  addLine?: string;
  runner?: ExecFileRunner;
}

export interface StgImportResult {
  linkUrl: string;
  branch: string;
  patchName: string;
  headCommit: string;
}

/**
 * Imports a patch email into the current StGit branch using the exact workflow
 * from importpatch.sh:
 * 1. Resolve LKML archive Link URL via getlkmllink.sh logic
 * 2. Run `stg import [opts] -m <mailPath>`
 * 3. Run `stg edit --save-template=<tmpIn>`, insert `Link: <URL>` before trailers,
 *    and apply via `stg edit --file <tmpOut>`
 * 4. Run `stg edit --ack` (if ack is enabled)
 * 5. Run `stg edit --sign`
 */
export async function importPatchWithStgit(
  options: StgImportOptions
): Promise<StgImportResult> {
  const runner = options.runner || defaultExecFileRunner;
  const { mailPath, targetRepo } = options;

  const linkInput = options.messageId || mailPath;
  const linkUrl = await resolveLkmlLink(linkInput, runner);

  const importArgs = ["import"];
  if (options.reject) {
    importArgs.push("--reject");
  }
  importArgs.push("-m", mailPath);
  await runner("stg", importArgs, targetRepo);

  const tmpPrefix = path.join(os.tmpdir(), `undine-stg-${process.pid}-${Date.now()}`);
  const tmpFileIn = `${tmpPrefix}-in.txt`;
  const tmpFileOut = `${tmpPrefix}-out.txt`;

  try {
    await runner("stg", ["edit", `--save-template=${tmpFileIn}`], targetRepo);
    const templateContent = fs.readFileSync(tmpFileIn, "utf-8");
    const updatedContent = insertLinkIntoStgTemplate(
      templateContent,
      linkUrl,
      options.addLine
    );
    fs.writeFileSync(tmpFileOut, updatedContent, { encoding: "utf-8", mode: 0o600 });
    await runner("stg", ["edit", "--file", tmpFileOut], targetRepo);
  } finally {
    try {
      if (fs.existsSync(tmpFileIn)) fs.unlinkSync(tmpFileIn);
    } catch {}
    try {
      if (fs.existsSync(tmpFileOut)) fs.unlinkSync(tmpFileOut);
    } catch {}
  }

  if (options.ack) {
    await runner("stg", ["edit", "--ack"], targetRepo);
  }

  await runner("stg", ["edit", "--sign"], targetRepo);

  let branch = "";
  let patchName = "";
  let headCommit = "";
  try {
    const branchRes = await runner("git", ["rev-parse", "--abbrev-ref", "HEAD"], targetRepo);
    branch = branchRes.stdout.trim();
  } catch {}
  try {
    const topRes = await runner("stg", ["top"], targetRepo);
    patchName = topRes.stdout.trim();
  } catch {}
  try {
    const headRes = await runner("git", ["rev-parse", "HEAD"], targetRepo);
    headCommit = headRes.stdout.trim();
  } catch {}

  return {
    linkUrl,
    branch,
    patchName,
    headCommit,
  };
}

/**
 * Expands a configured `undine.targetGitRepo` setting against an optional workspace folder path.
 * Supports:
 * - "${workspaceFolder}" or "${workspaceFolder}/linux" (and "${workspaceRoot}")
 * - "${workspaceFolder:folderName}" for multi-root workspaces
 * - Relative sub-repository paths such as "linux" or "./kernel/linux" (resolved against workspaceFolderPath)
 * - Home directory prefix "~", "~/...", "${userHome}", "$HOME", and "${env:VAR}"
 * - Absolute filesystem paths
 */
export function expandTargetGitRepoPath(
  configuredPath: string | undefined,
  workspaceFolderPath?: string,
  workspaceFolders?: ReadonlyArray<{ name?: string; uri?: { fsPath?: string; path?: string } }>
): string {
  let trimmed = (configuredPath || "").trim();
  if (
    (trimmed.startsWith('"') && trimmed.endsWith('"')) ||
    (trimmed.startsWith("'") && trimmed.endsWith("'"))
  ) {
    trimmed = trimmed.slice(1, -1).trim();
  }
  const wsPath = (workspaceFolderPath || "").trim();

  if (!trimmed) {
    return wsPath ? path.resolve(wsPath) : "";
  }

  let expanded = trimmed;
  const home = os.homedir();

  // Expand ${userHome}, $HOME, and ${env:VAR}
  expanded = expanded.replace(/\$\{userHome\}/g, home);
  expanded = expanded.replace(/\$\{env:([^}]+)\}/g, (_m, varName: string) => {
    if (varName === "HOME") {
      return process.env.HOME || home;
    }
    return process.env[varName] || "";
  });
  if (expanded === "$HOME") {
    expanded = home;
  } else if (expanded.startsWith("$HOME/") || expanded.startsWith("$HOME\\")) {
    expanded = path.join(home, expanded.slice("$HOME/".length));
  }

  // Expand ${workspaceFolder:name}
  if (/\$\{workspaceFolder:[^}]+\}/.test(expanded)) {
    let unresolvedNamedFolder = false;
    expanded = expanded.replace(/\$\{workspaceFolder:([^}]+)\}/g, (_m, folderName: string) => {
      const targetName = folderName.trim();
      const matched = (workspaceFolders || []).find((f) => {
        const fPath = f.uri?.fsPath || f.uri?.path || "";
        return f.name === targetName || (fPath && path.basename(fPath) === targetName);
      });
      const matchedPath = (matched?.uri?.fsPath || matched?.uri?.path || "").trim();
      if (matchedPath) {
        return matchedPath;
      }
      if (wsPath && path.basename(wsPath) === targetName) {
        return wsPath;
      }
      unresolvedNamedFolder = true;
      return "";
    });
    if (unresolvedNamedFolder) {
      return "";
    }
  }

  if (expanded.includes("${workspaceFolder}") || expanded.includes("${workspaceRoot}")) {
    if (!wsPath) {
      return "";
    }
    expanded = expanded
      .replace(/\$\{workspaceFolder\}/g, wsPath)
      .replace(/\$\{workspaceRoot\}/g, wsPath);
  }

  if (expanded === "~") {
    expanded = home;
  } else if (expanded.startsWith("~/") || expanded.startsWith("~\\")) {
    expanded = path.join(home, expanded.slice(2));
  }

  if (!path.isAbsolute(expanded)) {
    if (!wsPath) {
      return "";
    }
    expanded = path.resolve(wsPath, expanded);
  } else {
    expanded = path.resolve(expanded);
  }

  return expanded;
}

function isExistingGitRepoOrDir(dirPath: string): { isGitRepo: boolean; isDir: boolean } {
  if (!dirPath) {
    return { isGitRepo: false, isDir: false };
  }
  try {
    const st = fs.statSync(dirPath);
    if (!st.isDirectory()) {
      return { isGitRepo: false, isDir: false };
    }
    const gitEntry = path.join(dirPath, ".git");
    return { isGitRepo: fs.existsSync(gitEntry), isDir: true };
  } catch {
    return { isGitRepo: false, isDir: false };
  }
}

/**
 * Resolves the workspace-scoped `undine.targetGitRepo` setting, prompting the user
 * if no workspace folder or valid path can be determined.
 */
export async function resolveTargetGitRepo(promptText?: string): Promise<string | undefined> {
  const activeUri = (vscode.window as any).activeTextEditor?.document?.uri;
  const getWsFolder = (vscode.workspace as any).getWorkspaceFolder;
  const activeFolder =
    activeUri && typeof getWsFolder === "function" ? getWsFolder.call(vscode.workspace, activeUri) : undefined;
  const folders: ReadonlyArray<any> = vscode.workspace.workspaceFolders || [];

  const candidateFolders = activeFolder
    ? [activeFolder, ...folders.filter((f) => f !== activeFolder)]
    : [...folders];

  let targetRepo = "";

  if (candidateFolders.length > 0) {
    let firstExpanded = "";
    let firstExistingDir = "";

    for (const folder of candidateFolders) {
      const wsPath: string | undefined = folder?.uri?.fsPath || folder?.uri?.path;
      const cfg = vscode.workspace.getConfiguration("undine", folder?.uri);
      const configured = cfg.get<string>("targetGitRepo");
      const expanded = expandTargetGitRepoPath(configured, wsPath, folders);
      if (!expanded) {
        continue;
      }
      if (!firstExpanded) {
        firstExpanded = expanded;
      }
      const check = isExistingGitRepoOrDir(expanded);
      if (check.isGitRepo) {
        targetRepo = expanded;
        break;
      }
      if (check.isDir && !firstExistingDir) {
        firstExistingDir = expanded;
      }
    }

    if (!targetRepo) {
      targetRepo = firstExistingDir || firstExpanded;
    }
  } else {
    const cfg = vscode.workspace.getConfiguration("undine");
    const configured = cfg.get<string>("targetGitRepo");
    targetRepo = expandTargetGitRepoPath(configured, undefined, folders);
  }

  const defaultWsPath: string | undefined =
    candidateFolders[0]?.uri?.fsPath || candidateFolders[0]?.uri?.path;

  if (!targetRepo && promptText) {
    const input = await vscode.window.showInputBox({
      prompt: promptText,
      placeHolder: "/path/to/linux",
    });
    if (input && input.trim()) {
      const rawInput = input.trim();
      const expanded = expandTargetGitRepoPath(rawInput, defaultWsPath, folders);
      targetRepo = expanded || rawInput;
    }
  }

  return targetRepo || undefined;
}

export function registerB4Commands(
  context: vscode.ExtensionContext,
  client: UndineRestClient,
  onRefreshNeeded?: () => void
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand("undine.importB4", async (messageIdOrItem?: any) => {
      try {
        let msgId: string | undefined;
        if (typeof messageIdOrItem === "string") {
          msgId = messageIdOrItem;
        } else if (messageIdOrItem?.patch?.msgid) {
          msgId = messageIdOrItem.patch.msgid;
        } else if (messageIdOrItem?.message?.message_id) {
          msgId = messageIdOrItem.message.message_id;
        } else if (messageIdOrItem?.msgid) {
          msgId = messageIdOrItem.msgid;
        }

        if (!msgId) {
          msgId = await vscode.window.showInputBox({
            prompt: "Enter Message-ID or lore.kernel.org URL to import via b4:",
            placeHolder: "e.g. 20261003...-1-author@kernel.org",
          });
        }

        if (!msgId) return;

        const cleanId = msgId.replace(/^<|>$/g, "");

        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: `Importing thread <${cleanId}> via b4...`,
            cancellable: false,
          },
          async () => {
            const res = await client.importB4(cleanId);
            vscode.window.showInformationMessage(
              `b4 import completed: ${res.imported_count} message(s) imported to b4-imports folder.`
            );
            if (onRefreshNeeded) onRefreshNeeded();
          }
        );
      } catch (err: any) {
        vscode.window.showErrorMessage(`b4 import failed: ${err.message}`);
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("undine.applyPatch", async (messageIdOrItem?: any) => {
      try {
        let msgId: string | undefined;
        if (typeof messageIdOrItem === "string") {
          msgId = messageIdOrItem;
        } else if (messageIdOrItem?.msgId) {
          msgId = messageIdOrItem.msgId;
        } else if (messageIdOrItem?.patch?.msgid) {
          msgId = messageIdOrItem.patch.msgid;
        } else if (messageIdOrItem?.message?.message_id) {
          msgId = messageIdOrItem.message.message_id;
        } else if (messageIdOrItem?.msgid) {
          msgId = messageIdOrItem.msgid;
        } else if (messageIdOrItem?.message_id) {
          msgId = messageIdOrItem.message_id;
        }

        if (!msgId) {
          const currentDetail = MessageHeaderViewProvider.instance?.getMessage();
          if (currentDetail?.message?.message_id) {
            msgId = currentDetail.message.message_id;
          }
        }

        if (!msgId) {
          msgId = await vscode.window.showInputBox({
            prompt: "Enter Message-ID of patch series to apply via b4 am:",
            placeHolder: "e.g. 20261003...-1-author@kernel.org",
          });
        }

        if (!msgId) return;

        const cleanId = msgId.replace(/^<|>$/g, "");
        const targetRepo = await resolveTargetGitRepo("Target Git repository path to apply patch to:");

        if (!targetRepo) return;

        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: `Applying patch series <${cleanId}> to ${targetRepo} via b4 am...`,
            cancellable: false,
          },
          async () => {
            const res = await client.applyPatch(cleanId, targetRepo);
            if (res.success) {
              const headHash = res.head || (res as any).commit_id || "";
              const commitDesc = headHash ? ` (commit ${headHash.slice(0, 10)})` : "";
              vscode.window.showInformationMessage(
                `Patch successfully applied to branch "${res.branch || "current"}"${commitDesc}`
              );
            } else {
              vscode.window.showErrorMessage(`Failed to apply patch: ${res.error || res.detail || "git am error"}`);
            }
          }
        );
      } catch (err: any) {
        vscode.window.showErrorMessage(`Patch application failed: ${err.message}`);
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("undine.importPatchStg", async (messageIdOrItem?: any) => {
      try {
        let targetDbId: number | undefined;
        let msgId: string | undefined;

        if (typeof messageIdOrItem === "number") {
          targetDbId = messageIdOrItem;
        } else if (typeof messageIdOrItem === "string") {
          if (/^\d+$/.test(messageIdOrItem.trim())) {
            targetDbId = parseInt(messageIdOrItem.trim(), 10);
          } else {
            msgId = messageIdOrItem;
          }
        } else if (messageIdOrItem) {
          if (typeof messageIdOrItem.id === "number") {
            targetDbId = messageIdOrItem.id;
          } else if (typeof messageIdOrItem.message?.id === "number") {
            targetDbId = messageIdOrItem.message.id;
          }
          if (messageIdOrItem.msgId) {
            msgId = messageIdOrItem.msgId;
          } else if (messageIdOrItem.patch?.msgid) {
            msgId = messageIdOrItem.patch.msgid;
          } else if (messageIdOrItem.message?.message_id) {
            msgId = messageIdOrItem.message.message_id;
          } else if (messageIdOrItem.msgid) {
            msgId = messageIdOrItem.msgid;
          } else if (messageIdOrItem.message_id) {
            msgId = messageIdOrItem.message_id;
          }
        }

        // Fall back to currently selected message in MessageHeaderViewProvider if invoked without args
        if (!targetDbId && !msgId) {
          const currentDetail = MessageHeaderViewProvider.instance?.getMessage();
          if (currentDetail?.message) {
            targetDbId = currentDetail.message.id;
            msgId = currentDetail.message.message_id;
          }
        }

        if (!targetDbId && !msgId) {
          msgId = await vscode.window.showInputBox({
            prompt: "Enter Message-ID of patch mail to import into current StGit branch:",
            placeHolder: "e.g. 20261003...-1-author@kernel.org",
          });
        }

        if (!targetDbId && !msgId) return;

        const cleanId = (msgId || "").replace(/^<|>$/g, "").trim();
        const lookupTarget: string | number = targetDbId || cleanId;

        const targetRepo = await resolveTargetGitRepo(
          "Target StGit repository path to import patch into:"
        );

        if (!targetRepo) return;

        const displayLabel = cleanId ? `<${cleanId}>` : `#${targetDbId}`;

        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: `Importing patch ${displayLabel} into current StGit branch in ${targetRepo}...`,
            cancellable: false,
          },
          async () => {
            let mailPath = "";
            let resolvedMsgId = cleanId;
            let tempMailFile = "";

            try {
              // 1. Resolve mbox/maildir path on disk (hydrates on server if needed)
              try {
                const mboxInfo = await client.getMessageMboxPath(lookupTarget);
                if (mboxInfo?.file_path && fs.existsSync(mboxInfo.file_path)) {
                  mailPath = mboxInfo.file_path;
                }
              } catch {
                // Fall back to getMessageDetail below
              }

              // 2. Fetch message detail if needed for Message-ID or fallback .eml file creation
              if (!mailPath || !resolvedMsgId) {
                const detail = await client.getMessageDetail(lookupTarget);
                if (!resolvedMsgId && detail.message.message_id) {
                  resolvedMsgId = detail.message.message_id.replace(/^<|>$/g, "").trim();
                }
                if (!mailPath && detail.message.file_path && fs.existsSync(detail.message.file_path)) {
                  mailPath = detail.message.file_path;
                }
                if (!mailPath && detail.message.body_plain) {
                  tempMailFile = path.join(
                    os.tmpdir(),
                    `undine-patch-${process.pid}-${Date.now()}.eml`
                  );
                  const fromHdr = detail.message.from_name
                    ? `${detail.message.from_name} <${detail.message.from_addr}>`
                    : detail.message.from_addr || "dev@example.com";
                  const rawLines = [
                    `From: ${fromHdr}`,
                    `Subject: ${detail.message.subject || "[PATCH]"}`,
                  ];
                  if (detail.message.date) {
                    rawLines.push(`Date: ${detail.message.date}`);
                  }
                  if (resolvedMsgId) {
                    rawLines.push(`Message-ID: <${resolvedMsgId}>`);
                  }
                  rawLines.push("", detail.message.body_plain, "");
                  fs.writeFileSync(tempMailFile, rawLines.join("\n"), {
                    encoding: "utf-8",
                    mode: 0o600,
                  });
                  mailPath = tempMailFile;
                }
              }

              if (!mailPath) {
                throw new Error("Could not locate local patch email file to import.");
              }

              const res = await importPatchWithStgit({
                mailPath,
                targetRepo: targetRepo!,
                messageId: resolvedMsgId || undefined,
              });

              const patchDesc = res.patchName ? `"${res.patchName}" ` : "";
              const branchDesc = res.branch ? `on branch "${res.branch}" ` : "";
              const commitDesc = res.headCommit ? `(${res.headCommit.slice(0, 12)})` : "";
              vscode.window.showInformationMessage(
                `StGit patch ${patchDesc}imported ${branchDesc}${commitDesc}`.trim()
              );
            } finally {
              if (tempMailFile) {
                try {
                  if (fs.existsSync(tempMailFile)) fs.unlinkSync(tempMailFile);
                } catch {}
              }
            }
          }
        );
      } catch (err: any) {
        vscode.window.showErrorMessage(`StGit patch import failed: ${err.message}`);
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand("undine.abortApplyPatch", async () => {
      try {
        const targetRepo = await resolveTargetGitRepo();
        const res = await client.abortPatchApply(
          targetRepo ? { target_git_repo: targetRepo } : {}
        );
        if (res.success) {
          vscode.window.showInformationMessage("Patch application aborted successfully.");
        } else {
          vscode.window.showErrorMessage(`Abort failed: ${res.error || res.detail || "unknown error"}`);
        }
      } catch (err: any) {
        vscode.window.showErrorMessage(`Abort failed: ${err.message}`);
      }
    })
  );
}
