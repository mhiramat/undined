import test from "node:test";
import assert from "node:assert/strict";

const origSetTimeout = globalThis.setTimeout;
globalThis.setTimeout = (fn, ms, ...args) => {
  const timer = origSetTimeout(fn, ms, ...args);
  if (timer && typeof timer.unref === "function") {
    timer.unref();
  }
  return timer;
};

globalThis.window = {
  location: { origin: "http://127.0.0.1:8080" },
  addEventListener: () => {},
  removeEventListener: () => {},
};
globalThis.localStorage = {
  _map: new Map(),
  getItem(k) {
    return this._map.get(k) ?? null;
  },
  setItem(k, v) {
    this._map.set(k, String(v));
  },
};

function createMockElement(id = "") {
  const listeners = new Map();
  const children = new Map();

  const el = {
    id,
    _innerHTML: "",
    textContent: "",
    innerText: "",
    title: "",
    value: "",
    checked: true,
    disabled: false,
    style: { display: "block", opacity: "1" },
    classList: {
      add() {},
      remove() {},
      contains() {
        return false;
      },
    },
    set innerHTML(val) {
      this._innerHTML = String(val);
      children.clear();
      const inputRe = /<input\b([^>]*?)id="([^"]+)"([^>]*?)>/gi;
      let m;
      while ((m = inputRe.exec(this._innerHTML)) !== null) {
        const attrs = `${m[1]} ${m[3]}`;
        const inputId = m[2];
        const child = createMockElement(inputId);
        const valMatch = attrs.match(/\bvalue="([^"]*)"/i);
        if (valMatch) {
          child.value = valMatch[1];
        }
        child.checked = /\bchecked\b/i.test(attrs);
        children.set(`#${inputId}`, child);
      }
      const btnRe = /<button\b([^>]*?)id="([^"]+)"([^>]*?)>([\s\S]*?)<\/button>/gi;
      while ((m = btnRe.exec(this._innerHTML)) !== null) {
        const attrs = `${m[1]} ${m[3]}`;
        const btnId = m[2];
        const inner = m[4];
        const child = createMockElement(btnId);
        child.textContent = inner.replace(/<[^>]+>/g, "").trim();
        child.innerText = child.textContent;
        child.disabled = /\bdisabled\b/i.test(attrs);
        children.set(`#${btnId}`, child);
      }
      const divIds = [
        "viewer-diff-body",
        "apply-patch-form",
        "apply-error-banner",
        "group-branch-name",
        "group-base-commit",
      ];
      for (const dId of divIds) {
        if (this._innerHTML.includes(`id="${dId}"`)) {
          children.set(`#${dId}`, createMockElement(dId));
        }
      }
    },
    get innerHTML() {
      return this._innerHTML;
    },
    addEventListener(evt, fn) {
      listeners.set(evt, fn);
    },
    async trigger(evt, eventObj = { preventDefault() {} }) {
      const fn = listeners.get(evt);
      if (fn) {
        return await fn(eventObj);
      }
    },
    querySelector(sel) {
      if (children.has(sel)) {
        return children.get(sel);
      }
      for (const child of children.values()) {
        const nested = child.querySelector(sel);
        if (nested) return nested;
      }
      return null;
    },
    querySelectorAll() {
      return [];
    },
    focus() {},
    select() {},
    remove() {
      if (globalThis.document.body._lastAppended === el) {
        globalThis.document.body._lastAppended = null;
      }
    },
  };
  return el;
}

globalThis.document = {
  addEventListener: () => {},
  getElementById: () => null,
  createElement: () => createMockElement("modal-backdrop"),
  body: {
    _lastAppended: null,
    appendChild(el) {
      this._lastAppended = el;
    },
  },
};

import { store } from "../frontend/src/store.js";
import { extractBaseCommit, openApplyPatchDialog, closeApplyPatchDialog } from "../frontend/src/components/ApplyPatchDialog.js";
import { renderViewerPane } from "../frontend/src/components/ViewerPane.js";

test("extractBaseCommit extracts git base-commit SHA from patch postscript and rejects invalid flags", () => {
  const sampleBody = [
    "tracing_buffers_ioctl() calls ring_buffer_map_get_reader() without",
    "trace_access_lock, breaking concurrent readers.",
    "",
    "Signed-off-by: Kernel Developer <dev@example.com>",
    "---",
    "diff --git a/kernel/trace/trace.c b/kernel/trace/trace.c",
    "--- a/kernel/trace/trace.c",
    "+++ b/kernel/trace/trace.c",
    "@@ -7404,3 +7404,4 @@",
    " \treturn 0;",
    "",
    "base-commit: 6628e7d4c6b25c8d18f1345e3a20d21829aae7e4",
    "-- ",
    "2.56.1.703.gf848ed807c-goog",
  ].join("\n");

  assert.equal(
    extractBaseCommit(sampleBody),
    "6628e7d4c6b25c8d18f1345e3a20d21829aae7e4"
  );
  assert.equal(extractBaseCommit("No base commit here"), "");
  assert.equal(extractBaseCommit("base-commit: -bad-flag"), "");
});

test("ViewerPane renders Apply with git am for patch with base-commit and ApplyPatchDialog auto-populates base-commit", async () => {
  const baseSha = "6628e7d4c6b25c8d18f1345e3a20d21829aae7e4";
  const patchDetail = {
    message: {
      id: 13114,
      folder_id: 1,
      message_id: "20261117082932.3441173-1-dev@example.com",
      subject: "[PATCH] tracing: Take trace_access_lock() in tracing_buffers_ioctl()",
      from_name: "Kernel Developer",
      from_addr: "dev@example.com",
      to_addr: "linux-trace-kernel@vger.kernel.org",
      date: "Tue, 17 Nov 2026 08:29:32 +0000",
      is_read: 1,
      is_patch: true,
      body_plain:
        "Take trace_access_lock() around ring_buffer_map_get_reader().\n\n" +
        "Signed-off-by: Kernel Developer <dev@example.com>\n---\n" +
        "diff --git a/kernel/trace/trace.c b/kernel/trace/trace.c\n" +
        "--- a/kernel/trace/trace.c\n" +
        "+++ b/kernel/trace/trace.c\n" +
        "@@ -7404,3 +7404,4 @@\n" +
        "+\ttrace_access_lock(iter->cpu_file);\n\n" +
        `base-commit: ${baseSha}\n-- \n2.56.1\n`,
    },
    base_commit: baseSha,
    parsed_diff: {
      is_patch: true,
      base_commit: baseSha,
      preamble: "Take trace_access_lock() around ring_buffer_map_get_reader().",
      files: [
        {
          old_path: "kernel/trace/trace.c",
          new_path: "kernel/trace/trace.c",
          hunks: [
            {
              header: "@@ -7404,3 +7404,4 @@",
              paired_rows: [],
              unified_lines: [],
            },
          ],
        },
      ],
      postscript: `base-commit: ${baseSha}\n-- \n2.56.1`,
      body_continues_after: false,
    },
    comments: [],
    attachments: [],
  };

  const container = createMockElement("viewer-pane");
  renderViewerPane(container);
  store.setActiveMessageDetail(patchDetail);

  const applyBtn = container.querySelector("#btn-apply-git");
  assert.ok(applyBtn, "Expected 'Apply with git am' button to be rendered for patch with base-commit");

  await applyBtn.trigger("click");
  const modal = globalThis.document.body._lastAppended;
  assert.ok(modal, "Expected ApplyPatchDialog modal to open");

  const baseCommitInput = modal.querySelector("#apply-base-commit-input");
  assert.ok(baseCommitInput, "Expected #apply-base-commit-input inside ApplyPatchDialog");
  assert.equal(
    baseCommitInput.value,
    baseSha,
    "ApplyPatchDialog should auto-populate Base Commit / Ref with the email's base-commit SHA"
  );
  assert.ok(
    modal.innerHTML.includes(`base-commit: ${baseSha}`),
    "ApplyPatchDialog hint should indicate auto-populated base-commit"
  );
  closeApplyPatchDialog();

  // Verify fallback to "HEAD" when patch has no base-commit
  store.setActiveMessageDetail(null);
  openApplyPatchDialog({
    id: 13115,
    message_id: "20261117-no-base@example.com",
    subject: "[PATCH] tracing: simple cleanup",
    body_plain: "Simple patch without base-commit.",
  });
  const modalNoBase = globalThis.document.body._lastAppended;
  const noBaseInput = modalNoBase.querySelector("#apply-base-commit-input");
  assert.equal(noBaseInput.value, "HEAD", "ApplyPatchDialog should default to HEAD when no base-commit is present");
  closeApplyPatchDialog();
});
