/**
 * UndineChatParticipant: Implements the @undine Chat Participant for GitHub Copilot.
 * Supports /review, /summarize, and /draft-reply slash commands.
 */

import * as vscode from "vscode";
import { UndineRestClient } from "../client/restClient";
import { MessageDetail } from "../client/types";

export function registerUndineChatParticipant(
  context: vscode.ExtensionContext,
  _client: UndineRestClient,
  getActiveMessageDetail: () => MessageDetail | null
): void {
  if (!vscode.chat || !vscode.chat.createChatParticipant) {
    // Language Models / Chat API may not be enabled in all environments
    return;
  }

  const participant = vscode.chat.createChatParticipant(
    "undine",
    async (
      request: vscode.ChatRequest,
      _chatContext: vscode.ChatContext,
      response: vscode.ChatResponseStream,
      token: vscode.CancellationToken
    ) => {
      const activeDetail = getActiveMessageDetail();

      if (!activeDetail) {
        response.markdown(
          "⚠️ No active patch email is currently selected. Please select a message in Undine's Thread Hierarchy first."
        );
        return;
      }

      const m = activeDetail.message;
      const parsed = activeDetail.parsed_diff;

      switch (request.command) {
        case "review": {
          response.markdown(`### 🐧 Linux Kernel Patch Review for \`${m.subject}\`\n\n`);
          response.markdown(`*Analyzing commit description and diff against Linux kernel conventions...*\n\n`);

          let diffSummary = "";
          if (parsed && parsed.files) {
            diffSummary = parsed.files
              .map((f) => {
                const fn = f.new_path || f.old_path;
                return `File: ${fn}\nHunks: ${f.hunks.length}`;
              })
              .join("\n");
          }

          const prompt =
            `You are a Linux kernel maintainer performing a patch review on LKML.\n` +
            `Subject: ${m.subject}\n` +
            `Author: ${m.from_name || m.from_addr}\n` +
            `Commit Description:\n${parsed?.preamble || m.body_plain || ""}\n\n` +
            `Changed Files Summary:\n${diffSummary}\n\n` +
            `Evaluate this patch against Linux Kernel Review Guidelines:\n` +
            `1. Locking & Concurrency (deadlocks, spinlock vs mutex, lockdep assertions)\n` +
            `2. Memory Safety (bounds checking, leaks on error paths, allocation flags GFP_KERNEL vs GFP_ATOMIC)\n` +
            `3. Error unwinding (reverse order cleanup, negative errno values)\n` +
            `4. Coding style (tab stops, function length, clean rationale in description)\n\n` +
            `Provide specific, constructive findings with line-level advice.`;

          await streamModelResponse(prompt, response, token);
          break;
        }

        case "summarize": {
          response.markdown(`### 📋 Thread Summary: \`${m.subject}\`\n\n`);
          const prompt =
            `Summarize the following Linux kernel discussion thread clearly and concisely:\n` +
            `Subject: ${m.subject}\n` +
            `From: ${m.from_name} <${m.from_addr}>\n` +
            `Date: ${m.date}\n\n` +
            `Body:\n${m.body_plain || ""}\n\n` +
            `List key points, questions raised, and current consensus or next steps.`;

          await streamModelResponse(prompt, response, token);
          break;
        }

        case "draft-reply": {
          response.markdown(`### ✉️ LKML Reply Draft Generator\n\n`);
          const prompt =
            `Draft a professional LKML review reply for the following patch:\n` +
            `Subject: ${m.subject}\n` +
            `From: ${m.from_name} <${m.from_addr}>\n\n` +
            `Commit Description:\n${parsed?.preamble || m.body_plain || ""}\n\n` +
            `Include appropriate reviewer remarks, question clarifications, and maintainer trailers (e.g. Reviewed-by:, Acked-by:).`;

          await streamModelResponse(prompt, response, token);
          break;
        }

        default: {
          response.markdown(
            `Welcome to **@undine**! Available review commands:\n\n` +
            `- \`@undine /review\`: Analyze active patch against Linux kernel quality standards.\n` +
            `- \`@undine /summarize\`: Generate concise executive summary of this thread.\n` +
            `- \`@undine /draft-reply\`: Prepare an LKML-compliant reply draft with review feedback.`
          );
          break;
        }
      }
    }
  );

  context.subscriptions.push(participant);
}

async function streamModelResponse(
  prompt: string,
  response: vscode.ChatResponseStream,
  token: vscode.CancellationToken
): Promise<void> {
  if (!vscode.lm || !vscode.lm.selectChatModels) {
    response.markdown("Language Model API is not available in this VS Code environment.");
    return;
  }

  try {
    const models = await vscode.lm.selectChatModels({ family: "gpt-4o" });
    const model = models[0] || (await vscode.lm.selectChatModels())[0];

    if (!model) {
      response.markdown("No compatible language model found for Copilot review.");
      return;
    }

    const messages = [vscode.LanguageModelChatMessage.User(prompt)];
    const chatResponse = await model.sendRequest(messages, {}, token);

    for await (const fragment of chatResponse.text) {
      response.markdown(fragment);
    }
  } catch (err: any) {
    response.markdown(`\n\n*(Review generation error: ${err.message})*`);
  }
}
