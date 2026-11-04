import test from "node:test";
import assert from "node:assert/strict";
import { EditorHistory } from "../frontend/src/editorHistory.js";

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
