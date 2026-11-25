import test from "node:test";
import assert from "node:assert/strict";
import { EditorHistory } from "../frontend/src/editorHistory.js";
import {
  wrapCommentText,
  wrapReplyBodyComments,
  autoWrapEditorLine,
} from "../frontend/src/components/InternalEditor.js";

test("EditorHistory: initial state has no undo or redo", () => {
  const h = new EditorHistory("hello", 0, 0);
  assert.equal(h.canUndo(), false);
  assert.equal(h.canRedo(), false);
  assert.deepEqual(h.getCurrent(), { value: "hello", selectionStart: 0, selectionEnd: 0 });
  assert.equal(h.undo(), null);
  assert.equal(h.redo(), null);
});

test("EditorHistory: groups continuous typing within timeout", () => {
  const h = new EditorHistory("", 0, 0, { groupTimeout: 500 });

  // Type 'a'
  h.record("a", 1, 1, "input");
  assert.equal(h.canUndo(), true);
  assert.equal(h.canRedo(), false);
  assert.equal(h.stack.length, 2);

  // Type 'b' immediately (< 500ms)
  h.record("ab", 2, 2, "input");
  assert.equal(h.stack.length, 2); // collapsed into group
  assert.equal(h.getCurrent().value, "ab");

  // Type 'c' immediately
  h.record("abc", 3, 3, "input");
  assert.equal(h.stack.length, 2);
  assert.equal(h.getCurrent().value, "abc");

  // Undo should restore initial empty string
  const undone = h.undo();
  assert.deepEqual(undone, { value: "", selectionStart: 0, selectionEnd: 0 });
  assert.equal(h.canUndo(), false);
  assert.equal(h.canRedo(), true);

  // Redo should restore full typing group 'abc'
  const redone = h.redo();
  assert.deepEqual(redone, { value: "abc", selectionStart: 3, selectionEnd: 3 });
  assert.equal(h.canUndo(), true);
  assert.equal(h.canRedo(), false);
});

test("EditorHistory: breaks group after timeout", async () => {
  const h = new EditorHistory("", 0, 0, { groupTimeout: 50 });

  h.record("hello", 5, 5, "input");
  assert.equal(h.stack.length, 2);

  // Wait past groupTimeout
  await new Promise((r) => setTimeout(r, 70));

  h.record("hello world", 11, 11, "input");
  assert.equal(h.stack.length, 3);

  // First undo -> "hello"
  assert.equal(h.undo().value, "hello");
  // Second undo -> ""
  assert.equal(h.undo().value, "");
  assert.equal(h.canUndo(), false);

  // Redo 1 -> "hello"
  assert.equal(h.redo().value, "hello");
  // Redo 2 -> "hello world"
  assert.equal(h.redo().value, "hello world");
  assert.equal(h.canRedo(), false);
});

test("EditorHistory: forceNew creates distinct undo steps (e.g. Tab, tag)", () => {
  const h = new EditorHistory("line1\n", 6, 6);

  // Rapid typing
  h.record("line1\nfoo", 9, 9, "input");

  // Tab pressed (forceNew = true)
  h.record("line1\nfoo\t", 10, 10, "tab", true);
  assert.equal(h.stack.length, 3);

  // Undo tab
  const step1 = h.undo();
  assert.equal(step1.value, "line1\nfoo");

  // Undo typing
  const step2 = h.undo();
  assert.equal(step2.value, "line1\n");
});

test("EditorHistory: breaks group when switching between typing and deletion", () => {
  const h = new EditorHistory("", 0, 0);

  h.record("test", 4, 4, "input");
  // Immediately backspace to "tes"
  h.record("tes", 3, 3, "delete");
  assert.equal(h.stack.length, 3); // input and delete in separate steps

  assert.equal(h.undo().value, "test");
  assert.equal(h.undo().value, "");
});

test("EditorHistory: overwrites future redo branch on new input after undo", () => {
  const h = new EditorHistory("start", 5, 5, { groupTimeout: 50 });

  h.record("start A", 7, 7, "input", true);
  h.record("start B", 7, 7, "input", true);
  assert.equal(h.stack.length, 3);

  // Undo to start A
  h.undo();
  assert.equal(h.getCurrent().value, "start A");
  assert.equal(h.canRedo(), true);

  // Type new branch 'start C'
  h.record("start C", 7, 7, "input", true);
  assert.equal(h.stack.length, 3);
  assert.equal(h.getCurrent().value, "start C");
  assert.equal(h.canRedo(), false); // Redo branch 'start B' cleared!

  assert.equal(h.undo().value, "start A");
  assert.equal(h.undo().value, "start");
});

test("EditorHistory: caps at maxSize", () => {
  const h = new EditorHistory("0", 0, 0, { maxSize: 3 });

  h.record("1", 1, 1, "input", true);
  h.record("2", 1, 1, "input", true);
  h.record("3", 1, 1, "input", true);
  h.record("4", 1, 1, "input", true);

  assert.equal(h.stack.length, 3);
  assert.equal(h.getCurrent().value, "4");
  assert.equal(h.undo().value, "3");
  assert.equal(h.undo().value, "2");
  assert.equal(h.canUndo(), false); // '0' and '1' dropped
});

test("wrapCommentText: wraps long prose and spaceless dummy lines around 75 columns", () => {
  const longProse =
    "This is a dummy long comment on the diff line that exceeds seventy-five columns and should be wrapped automatically when composing the reply mail.";
  const wrapped = wrapCommentText(longProse, 75);
  const lines = wrapped.split("\n");
  assert.ok(lines.length >= 2);
  for (const l of lines) {
    assert.ok(l.length <= 75, `Line exceeded 75 cols: ${l}`);
  }

  const spaceless = "a".repeat(160);
  const wrappedSpaceless = wrapCommentText(spaceless, 75).split("\n");
  assert.equal(wrappedSpaceless.length, 3);
  assert.equal(wrappedSpaceless[0].length, 75);
  assert.equal(wrappedSpaceless[1].length, 75);
  assert.equal(wrappedSpaceless[2].length, 10);
});

test("wrapReplyBodyComments: wraps unquoted comment lines while preserving quotes and signature", () => {
  const longQuote = "> +	if (unlikely(kprobe_running() && !arch_kprobe_allow_reentry(p, regs))) return 0;";
  const longComment =
    "This is a dummy long comment on the diff that should wrap at 75 columns while keeping the quoted diff line above untouched.";
  const body = `On Thu, 05 Nov 2026, Kernel Developer <dev@kernel.org> wrote:\n${longQuote}\n\n${longComment}\n\n-- \nKernel Developer`;
  const wrapped = wrapReplyBodyComments(body, 75);

  assert.ok(wrapped.includes(longQuote), "Quoted line must remain untouched");
  assert.ok(wrapped.includes("-- \nKernel Developer"), "Signature must remain untouched");
  for (const line of wrapped.split("\n")) {
    if (line.startsWith(">") || line.startsWith("On ")) continue;
    assert.ok(line.length <= 75, `Unquoted line exceeded 75 cols: ${line}`);
  }
});

test("autoWrapEditorLine: wraps unquoted line when typing past column 75", () => {
  const prefix72 = "This is a comment line that reaches right up to seventy-two columns here";
  assert.equal(prefix72.length, 72);
  const typed = `${prefix72} and`; // 76 chars
  const res = autoWrapEditorLine(typed, typed.length, 75);
  assert.ok(res !== null);
  assert.equal(res.value, `${prefix72}\nand`);
  assert.equal(res.cursor, typed.length);

  // Quoted lines should never be auto-wrapped
  const quotedLong = `> ${typed}`;
  assert.equal(autoWrapEditorLine(quotedLong, quotedLong.length, 75), null);
});

test("autoWrapEditorLine: does not wrap long kernel/mail trailer tag lines", () => {
  const noWrapTagLines = [
    'Fixes: 1234567890ab ("tracing: Fix a very long commit subject that exceeds seventy-five columns")',
    "Link: https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u",
    "Closes: https://bugzilla.kernel.org/show_bug.cgi?id=12345678901234567890123456789",
    "Signed-off-by: Very Long Kernel Developer Name <very.long.developer.address@example.kernel.org>",
    "Reviewed-by: Another Kernel Developer Name <another.developer.address@example.kernel.org>",
    "Acked-by: Subsystem Maintainer Name <subsystem.maintainer.address@example.kernel.org>",
    "Co-developed-by: Collaborating Developer <collaborating.developer@example.kernel.org>",
    "Cc: stable@vger.kernel.org # v6.1+: 1234567890ab: tracing: prerequisite patch subject",
    "Message-ID: <20261104135327.23dbae14572e88b3.very.long.message.id@example.kernel.org>",
    "BugLink: https://bugs.example.org/projects/kernel/issues/very-long-issue-identifier-123456",
    "base-commit: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
    '  fixes: 1234567890ab ("indented lowercase fixes tag exceeding seventy-five columns")',
  ];

  for (const line of noWrapTagLines) {
    assert.ok(line.length > 75, `Test line should exceed 75 cols: ${line}`);
    assert.equal(
      autoWrapEditorLine(line, line.length, 75),
      null,
      `Trailer tag line must not auto-wrap: ${line}`
    );
  }

  // Regular prose starting with a non-trailer colon label must still wrap
  const noteProse =
    "Note: This is a regular prose explanation that exceeds seventy-five columns and should wrap.";
  const wrappedNote = autoWrapEditorLine(noteProse, noteProse.length, 75);
  assert.ok(wrappedNote !== null, "Normal prose starting with 'Note:' should still wrap");
  assert.ok(wrappedNote.value.includes("\n"));
});

test("autoWrapEditorLine: does not wrap URLs or reference URL lines", () => {
  const noWrapUrlLines = [
    "https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u",
    "<https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u>",
    "(https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u)",
    "git://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git/tree/kernel/trace/ftrace.c",
    "ftp://ftp.kernel.org/pub/linux/kernel/v6.x/testing/linux-6.12-rc1-very-long-archive-name.tar.xz",
    "<mailto:very.long.developer.address.exceeding.seventy.five.columns@example.kernel.org>",
    "[1] https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u",
    "[1]: https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u",
    "- https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u",
    "1. https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u",
    "Please check https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u",
    "See (https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u)",
    "詳細はこちら：https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u",
  ];

  for (const line of noWrapUrlLines) {
    assert.ok(line.length > 75, `Test line should exceed 75 cols: ${line}`);
    assert.equal(
      autoWrapEditorLine(line, line.length, 75),
      null,
      `URL line must not auto-wrap: ${line}`
    );
  }

  // Typing prose after a long URL should wrap after the URL without breaking it
  const url = "https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u";
  const proseAfterInlineUrl = `Please check ${url} for`;
  const wrappedAfterInline = autoWrapEditorLine(
    proseAfterInlineUrl,
    proseAfterInlineUrl.length,
    75
  );
  assert.ok(wrappedAfterInline !== null);
  assert.equal(wrappedAfterInline.value, `Please check ${url}\nfor`);
  assert.equal(wrappedAfterInline.cursor, proseAfterInlineUrl.length);

  const proseAfterStandaloneUrl = `${url} details`;
  const wrappedAfterStandalone = autoWrapEditorLine(
    proseAfterStandaloneUrl,
    proseAfterStandaloneUrl.length,
    75
  );
  assert.ok(wrappedAfterStandalone !== null);
  assert.equal(wrappedAfterStandalone.value, `${url}\ndetails`);
  assert.equal(wrappedAfterStandalone.cursor, proseAfterStandaloneUrl.length);
});

test("wrapCommentText & wrapReplyBodyComments: preserve tag lines and URLs", () => {
  const fixesLine =
    'Fixes: 1234567890ab ("tracing: Fix a very long commit subject that exceeds seventy-five columns")';
  const linkLine =
    "Link: https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u";
  const closesLine =
    "Closes: https://bugzilla.kernel.org/show_bug.cgi?id=12345678901234567890123456789";
  const refUrlLine =
    "[1] https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u";
  const parenUrl =
    "(https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u)";

  const comment = [
    fixesLine,
    linkLine,
    closesLine,
    refUrlLine,
    `Please inspect the previous discussion ${parenUrl} for full context on this issue.`,
  ].join("\n");

  const wrapped = wrapCommentText(comment, 75);
  assert.ok(wrapped.includes(fixesLine), "Fixes: line must stay unwrapped");
  assert.ok(wrapped.includes(linkLine), "Link: line must stay unwrapped");
  assert.ok(wrapped.includes(closesLine), "Closes: line must stay unwrapped");
  assert.ok(wrapped.includes(refUrlLine), "Reference URL line must stay unwrapped");
  assert.ok(wrapped.includes(`\n${parenUrl}\n`), "Parenthesized URL must stay intact");

  const replyBody = `On Thu, 05 Nov 2026, Kernel Developer <dev@kernel.org> wrote:\n> patch line\n\n${fixesLine}\n${linkLine}\n\n-- \nKernel Developer`;
  const wrappedReply = wrapReplyBodyComments(replyBody, 75);
  assert.ok(wrappedReply.includes(fixesLine));
  assert.ok(wrappedReply.includes(linkLine));
});

