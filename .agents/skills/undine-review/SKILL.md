---
name: undine-review
description: Review Linux kernel patches in Undine and post line-specific review comments via Undine's Model Context Protocol (MCP) server.
---

# Undine Linux Kernel Patch Review Specialist

This skill guides AI coding and review agents in analyzing Linux kernel patch submissions within Undine and submitting structured, line-level diff review feedback using the Undine Model Context Protocol (MCP) server.

## Overview

Undine is a terminal-first, web-native Linux kernel mail client and review workstation. AI agents interact with Undine via its MCP server (STDIO or programmatic interface) to:
- Discover incoming patch submissions and series.
- Inspect full thread context and commit diffs.
- Post inline review comments targeted to exact `FILE:LINE` coordinates.
- Follow Linux kernel patch review standards and coding conventions.
- Generate aggregated LKML-compliant email review draft responses.

---

## MCP Tool Reference

Undine registers dedicated MCP tools for patch review workflows:

### 1. `post_review_comment` (and aliases `post_diff_comment`, `add_diff_comment`)
Posts an inline review comment onto a patch email diff at a specified `FILE:LINE` target.

- **Parameters**:
  - `message_id` (*str*, required): The Message-ID header (e.g. `<20261003...-1-author@kernel.org>`) or database ID of the target patch email.
  - `target` (*str*, required): Coordinate in `FILE:LINE` format (e.g. `kernel/trace/ftrace.c:51` or `b/kernel/trace/ftrace.c:51`). To comment on lines in the message body / commit description, specify `DESCRIPTION:LINE` (e.g. `DESCRIPTION:12`). Paths prefixed with `a/` or `b/` are automatically normalized.
  - `comment` (*str*, required): The review feedback, critique, or question.
  - `side` (*str*, optional): Diff side: `"new"` for additions and context lines (default), or `"old"` for deletions.
  - `target_message_id` (*str*, optional): Alias for `message_id` for backward compatibility.
- **Returns**:
  ```json
  {
    "success": true,
    "comment_id": 42,
    "message_id": "ftrace-patch-1@kernel.org",
    "file": "kernel/trace/ftrace.c",
    "line_no": 51,
    "side": "new",
    "comment": "Check new_code() error return handling.",
    "mapped": true
  }
  ```
  *Note*: If `target` points to an existing file in the patch diff but outside the hunk context window, `mapped` is `false`, but the comment is still persisted as a draft so review feedback is not lost.

### 2. `list_recent_emails`
Lists recent email threads and patch messages.
- **Parameters**:
  - `folder` (*str*, optional): Filter by folder name (e.g. `"b4-imports"` or `"Inbox"`).
  - `limit` (*int*, optional): Maximum number of messages to return (default: `20`).
- **Returns**: Array of message summaries with `message_id`, `subject`, `from_addr`, `is_patch`, `date`.

### 3. `search_emails`
Searches indexed email messages using SQLite full-text search (FTS5).
- **Parameters**:
  - `query` (*str*, required): Search terms or FTS query.
  - `limit` (*int*, optional): Maximum results (default: `20`).

### 4. `get_thread_context`
Retrieves the complete thread conversation and patch diff content.
- **Parameters**:
  - `message_id` (*str*, required): Root or member message ID of the thread.
- **Returns**: List of messages in thread hierarchy with full `body_plain` content and patch diffs.

### 5. `write_draft_message` (and aliases `write_draft`, `save_draft`)
Writes a complete draft response into the local `Drafts` folder for human review, editing, and transmission.
- **Parameters**:
  - `in_reply_to` (*str*, optional): Target Message-ID being replied to (auto-derives To, Cc, Subject, and References if omitted).
  - `body` (*str*, required): The draft message body content.
  - `to` (*str*, optional): Recipient email address(es).
  - `subject` (*str*, optional): Subject line.
  - `cc` (*str*, optional): CC email address(es).
  - `inline_notes` (*list[dict]*, optional): Optional structured inline review comments to merge as LKML quotes into the draft.
- **Returns**: `{"success": true, "draft_id": int, "message_id": str, "subject": str, "file_path": str}`.

### 6. `create_review_draft`
Aggregates inline review comments into an LKML quoted reply email saved to the local `Drafts` folder.
- **Parameters**:
  - `target_message_id` (*str*, required): Message-ID being replied to.
  - `general_feedback` (*str*, required): High-level series summary or general review remarks.
  - `inline_notes` (*list[dict]*, optional): Array of `{"file": "...", "line_no": 123, "comment": "..."}` dicts. Use `"file": "DESCRIPTION"` to attach comments to lines of the commit description / message body.
- **Returns**: `{"success": true, "draft_id": int, "subject": str, "file_path": str}`.

### 7. `import_patch_series_b4`
Downloads a patch series or thread from `lore.kernel.org` via `b4`.
- **Parameters**:
  - `message_id_or_url` (*str*, required): Lore Message-ID or public inbox URL.

---

## Agent Review Workflow

When performing an automated or assisted patch review, follow this 4-step sequence:

```
[1. Discover] ---> [2. Inspect] ---> [3. Critique & Post] ---> [4. Draft Response]
list_recent_emails  get_thread_context  post_review_comment       write_draft_message / create_review_draft
```

### Step 1: Discover Target Patches
```json
// Example call: list_recent_emails
{
  "folder": "b4-imports",
  "limit": 10
}
```
Identify patch submissions where `is_patch: true`.

### Step 2: Retrieve Full Diff and Context
```json
// Example call: get_thread_context
{
  "message_id": "20261003140000.12345-1-author@kernel.org"
}
```
Examine `body_plain` for the commit message, subsystem tags, and unified diff hunks.

### Step 3: Analyze Code & Post Line-Specific Comments
Evaluate the patch against Linux kernel quality standards. For each identified issue, call `post_review_comment`. Comments may target diff lines (e.g. `kernel/trace/ftrace.c:50`) or commit description lines (e.g. `DESCRIPTION:12`):
```json
// Example call: post_review_comment on diff line
{
  "message_id": "20261003140000.12345-1-author@kernel.org",
  "target": "kernel/trace/ftrace.c:50",
  "comment": "Missing lockdep assertion prior to accessing shared state. Consider guard(spinlock)(&ftrace_lock);",
  "side": "new"
}
```
Or to comment on the commit log / patch description:
```json
// Example call: post_review_comment on patch description
{
  "message_id": "20261003140000.12345-1-author@kernel.org",
  "target": "DESCRIPTION:12",
  "comment": "Please cite the relevant commit hash or bug report URL in the description.",
  "side": "new"
}
```

### Step 4: Aggregate into LKML Reply Draft (Optional)
If a complete review response is needed:
```json
// Example call: create_review_draft
{
  "target_message_id": "20261003140000.12345-1-author@kernel.org",
  "general_feedback": "Thanks for the patch. Overall logic looks sound, but please check the locking and error unwinding noted below.",
  "inline_notes": [
    {
      "file": "kernel/trace/ftrace.c",
      "line_no": 50,
      "comment": "Missing lockdep assertion prior to accessing shared state."
    }
  ]
}
```

---

## Prompt-Only (Oneshot) Review Execution

When invoked non-interactively or in prompt-only mode (e.g. via `agy --print "..."` or Undine's background review runner), the agent operates headlessly without human interactive dialogues.

### Prompt Format
The review runner invokes the agent with a prompt formatted as:
```
Review patch for message {message_id} (commit {commit_id}). Post diff review comments using the Undine MCP review comment tool.
```

### Execution Directives for Oneshot Mode:
1. **Autonomous Execution**:
   - **Do not** pause to ask the user questions, confirmations, or approvals.
   - Proceed immediately to gather context and perform the review.
2. **Context Retrieval**:
   - Call `get_thread_context` with the provided `message_id` to inspect the subject, commit log, patch diff, and prior discussion.
   - If running within a git worktree where the commit was applied, you may additionally inspect git history or full files (`git show {commit_id}`).
3. **Structured Review Analysis**:
   - Critically evaluate all diff hunks against the **Linux Kernel Review Conventions** (locking, memory lifecycle, error unwinding, coding style).
4. **Post Review Comments**:
   - For every concrete defect, question, or improvement, call `post_review_comment`:
     - `message_id`: Target message ID.
     - `target`: `FILE:LINE` coordinate from the diff (e.g. `kernel/trace/ftrace.c:51`).
     - `comment`: Clear, polite, actionable feedback with specific symbols and rationale.
     - `side`: `"new"` for added/modified lines or `"old"` for removed lines.
   - Submitting comments via `post_review_comment` ensures they appear directly inside Undine's Side-by-Side review interface and are saved for LKML draft replies.
5. **Final Output Summary**:
   - Print a concise markdown summary containing:
     - Overall patch verdict (`ACCEPTABLE`, `NEEDS_REVISION`, or `REQUEST_CHANGES`).
     - A bulleted list of posted comments (`FILE:LINE` and high-level note).
     - Any general subsystem architectural advice.

---

## Linux Kernel Review Conventions

When authoring comments for kernel developers, adhere to canonical LKML conventions:

1. **Concurrency and Locking**:
   - Check for race conditions, missing locks, or inconsistent lock ordering.
   - Favor modern cleanup attributes (`guard(mutex)(&lock)`, `scoped_guard()`) where supported.
   - Verify that interrupts/softirqs are disabled if spinlocks are shared with ISRs (`spin_lock_irqsave`).

2. **Memory Management**:
   - Check for memory leaks on all error exit paths (`goto err_free;` or `__free(kfree)`).
   - Ensure proper allocation flags (`GFP_KERNEL` vs `GFP_ATOMIC` in atomic context).
   - Verify bounds checking and potential integer overflows in buffer calculations.

3. **Error Paths and Return Codes**:
   - Return standard negative errno values (`-ENOMEM`, `-EINVAL`, `-EBUSY`, `-ERANGE`).
   - Ensure cleanup order strictly mirrors allocation order in reverse.
   - Check `PTR_ERR` / `IS_ERR` handling for pointer-returning kernel APIs.

4. **Coding Style & Maintainability**:
   - Follow kernel style guide (8-column tab indentation, 80-100 column line length, no C99 comments `//` in legacy subsystems).
   - Keep comments concise, polite, and actionable.
   - Quote specific function names and variable identifiers clearly.
