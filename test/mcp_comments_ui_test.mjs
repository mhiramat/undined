import test from "node:test";
import assert from "node:assert/strict";

// Minimal browser globals for frontend ES modules in Node test environment
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
globalThis.document = {
  addEventListener: () => {},
  getElementById: () => null,
};

import { store } from "../frontend/src/store.js";
import {
  setCachedMessageDetail,
  getCachedMessageDetail,
  clearMessageDetailCache,
  invalidateMessageDetailCache,
  fetchMessageComments,
} from "../frontend/src/api.js";
import { NotificationChannel } from "../frontend/src/events.js";
import { renderSideDiff, findMatchingNote } from "../frontend/src/components/SideDiff.js";
import { renderViewerPane } from "../frontend/src/components/ViewerPane.js";

function createMockContainer(clientWidth = 1200) {
  return {
    clientWidth,
    innerHTML: "",
    querySelectorAll() {
      return [];
    },
    querySelector(sel) {
      if (sel === "#viewer-diff-body") {
        if (!this._bodyContainer) {
          this._bodyContainer = createMockContainer(clientWidth);
        }
        return this._bodyContainer;
      }
      return null;
    },
  };
}

const samplePatchDetail = {
  message: {
    id: 11530,
    folder_id: 3,
    message_id: "20261104073742.21202-2-dev@example.com",
    subject: "[PATCH 1/2] bootconfig: Allow keys with the maximum supported depth",
    from_name: "Kernel Developer",
    from_addr: "dev@example.com",
    to_addr: "linux-trace-kernel@vger.kernel.org",
    date: "2026-11-04T07:37:42Z",
    is_read: 1,
    is_patch: 1,
    body_plain: "Allow keys with max depth.\n\n---\n",
  },
  parsed_diff: {
    is_patch: true,
    preamble: "Allow keys with max depth.\nSigned-off-by: Kernel Developer <dev@example.com>",
    files: [
      {
        old_path: "lib/bootconfig.c",
        new_path: "lib/bootconfig.c",
        hunks: [
          {
            header: "@@ -428,9 +428,9 @@",
            section: "int __init xbc_node_compose_key_after(...)",
            paired_rows: [
              {
                row_idx: 0,
                left: { line_no: 431, type: "del", content: "\t\tkeys[depth++] = xbc_node_index(node);" },
                right: { line_no: null, type: "empty", content: "" },
              },
              {
                row_idx: 1,
                left: { line_no: 432, type: "ctx", content: "\t\tif (depth == XBC_DEPTH_MAX)" },
                right: { line_no: 431, type: "ctx", content: "\t\tif (depth == XBC_DEPTH_MAX)" },
              },
              {
                row_idx: 2,
                left: { line_no: 433, type: "ctx", content: "\t\t\treturn -ERANGE;" },
                right: { line_no: 432, type: "ctx", content: "\t\t\treturn -ERANGE;" },
              },
              {
                row_idx: 3,
                left: { line_no: null, type: "empty", content: "" },
                right: { line_no: 433, type: "add", content: "\t\tkeys[depth++] = xbc_node_index(node);" },
              },
            ],
            unified_lines: [
              { old_no: 431, new_no: null, type: "del", content: "\t\tkeys[depth++] = xbc_node_index(node);" },
              { old_no: 432, new_no: 431, type: "ctx", content: "\t\tif (depth == XBC_DEPTH_MAX)" },
              { old_no: 433, new_no: 432, type: "ctx", content: "\t\t\treturn -ERANGE;" },
              { old_no: null, new_no: 433, type: "add", content: "\t\tkeys[depth++] = xbc_node_index(node);" },
            ],
          },
        ],
      },
    ],
  },
  comments: [],
};

test("MCP review comments: fetchMessageComments unwraps { message_id, comments } and populates activeNotes", async () => {
  const mcpComment = {
    id: 70,
    message_id: "20261104073742.21202-2-dev@example.com",
    file_path: "lib/bootconfig.c",
    line_no: 433,
    side: "old",
    state: "draft",
    author: "agent",
    text: "Does returning -ERANGE here leave keys partially populated?",
  };

  globalThis.fetch = async (url) => {
    if (String(url).includes("/api/messages/11530/comments")) {
      return {
        ok: true,
        async json() {
          // Backend GET /api/messages/{id}/comments returns wrapper object
          return {
            message_id: "20261104073742.21202-2-dev@example.com",
            comments: [mcpComment],
          };
        },
      };
    }
    return { ok: true, async json() { return []; } };
  };

  const unwrapped = await fetchMessageComments(11530);
  assert.ok(Array.isArray(unwrapped), "fetchMessageComments must return an Array");
  assert.equal(unwrapped.length, 1);
  assert.equal(unwrapped[0].text, mcpComment.text);

  // Also verify store.setActiveMessageDetail handles both Array and wrapper object
  store.setActiveMessageDetail({
    ...samplePatchDetail,
    comments: {
      message_id: samplePatchDetail.message.message_id,
      comments: [mcpComment],
    },
  });

  const notesList = store.getNotesList();
  assert.equal(notesList.length, 1);
  assert.equal(notesList[0].file, "lib/bootconfig.c");
  assert.equal(notesList[0].lineNo, 433);
  assert.equal(notesList[0].side, "old");
  assert.equal(notesList[0].author, "agent");
});

test("MCP review comments: ViewerPane shows Discard Drafts button and SideDiff renders comment in message pane", () => {
  // 1. Initially no comments
  store.setActiveMessageDetail({
    ...samplePatchDetail,
    comments: [],
  });

  const viewerContainer = createMockContainer(1200);
  renderViewerPane(viewerContainer);
  store.setActiveMessageDetail({
    ...samplePatchDetail,
    comments: [],
  });

  assert.ok(
    !viewerContainer.innerHTML.includes('id="btn-discard-drafts"'),
    "Discard Drafts button should not appear when there are no draft comments"
  );

  // 2. Add MCP review comment on lib/bootconfig.c:433 (side="old")
  const mcpComment = {
    id: 70,
    message_id: "20261104073742.21202-2-dev@example.com",
    file_path: "lib/bootconfig.c",
    line_no: 433,
    side: "old",
    state: "draft",
    author: "agent",
    text: "MCP review comment on old line 433.",
  };

  store.setActiveMessageDetail({
    ...samplePatchDetail,
    comments: [mcpComment],
  });

  // Verify Discard Drafts (1) button is rendered in ViewerPane toolbar
  assert.ok(
    viewerContainer.innerHTML.includes('id="btn-discard-drafts"'),
    "ViewerPane must render #btn-discard-drafts button when MCP comment is present"
  );
  assert.ok(
    viewerContainer.innerHTML.includes("Discard Drafts (1)"),
    "ViewerPane must show Discard Drafts (1) count"
  );

  // Verify SideDiff (Side-by-Side >= 1050px) renders comment inline and highlights only left gutter
  const sbsContainer = createMockContainer(1200);
  renderSideDiff(sbsContainer, samplePatchDetail.parsed_diff);
  assert.ok(
    sbsContainer.innerHTML.includes("MCP review comment on old line 433."),
    "SideDiff (Side-by-Side) must display the MCP comment text in the message pane"
  );
  assert.ok(
    sbsContainer.innerHTML.includes("🤖 AI Review Comment"),
    "SideDiff must label agent comments as AI Review Comment"
  );
  assert.ok(
    sbsContainer.innerHTML.includes('class="diff-gutter has-note" data-side="left" data-file="lib/bootconfig.c" data-line="433"'),
    "SideDiff (Side-by-Side) must highlight left (old) gutter for line 433"
  );
  assert.ok(
    !sbsContainer.innerHTML.includes('class="diff-gutter right-side has-note" data-side="right" data-file="lib/bootconfig.c" data-line="433"'),
    "SideDiff (Side-by-Side) must NOT highlight right (new) gutter for line 433 when comment side is old"
  );

  // Verify SideDiff (Unified < 1050px) renders comment inline and highlights old gutter
  const unifiedContainer = createMockContainer(800);
  renderSideDiff(unifiedContainer, samplePatchDetail.parsed_diff);
  assert.ok(
    unifiedContainer.innerHTML.includes("MCP review comment on old line 433."),
    "SideDiff (Unified) must display the MCP comment text in the message pane"
  );
  assert.ok(
    unifiedContainer.innerHTML.includes('class="diff-gutter has-note" data-side="old" data-file="lib/bootconfig.c" data-line="433"'),
    "SideDiff (Unified) must highlight old gutter for line 433"
  );
  assert.ok(
    !unifiedContainer.innerHTML.includes('class="diff-gutter has-note" data-side="new" data-file="lib/bootconfig.c" data-line="433"'),
    "SideDiff (Unified) must NOT highlight new gutter for line 433 when comment side is old"
  );
});

test("MCP review comments: NotificationChannel live-updates active message and invalidates cache on SSE events", async () => {
  clearMessageDetailCache();

  const initialDetail = {
    ...samplePatchDetail,
    comments: [],
  };
  setCachedMessageDetail(11530, initialDetail);
  store.setActiveMessageDetail(initialDetail);

  const mcpComment = {
    id: 71,
    message_id: "20261104073742.21202-2-dev@example.com",
    file_path: "lib/bootconfig.c",
    line_no: 433,
    side: "new",
    state: "draft",
    author: "agent",
    text: "Live MCP comment via SSE.",
  };

  globalThis.fetch = async (url) => {
    if (String(url).includes("/api/messages/11530/comments")) {
      return {
        ok: true,
        async json() {
          return {
            message_id: "20261104073742.21202-2-dev@example.com",
            comments: [mcpComment],
          };
        },
      };
    }
    return { ok: true, async json() { return []; } };
  };

  const channel = new NotificationChannel(store);
  channel.debouncedSyncComments("20261104073742.21202-2-dev@example.com");

  // Wait for 200ms debounce
  await new Promise((r) => setTimeout(r, 260));

  const notes = store.getNotesList();
  assert.equal(notes.length, 1, "activeNotes should be updated after debouncedSyncComments");
  assert.equal(notes[0].text, "Live MCP comment via SSE.");

  // Cached detail must also reflect the updated comments
  const cachedAfter = getCachedMessageDetail(11530);
  assert.equal(cachedAfter?.comments?.length, 1);

  // Test invalidateMessageDetailCache by Message-ID clears DB-ID entry too
  invalidateMessageDetailCache("20261104073742.21202-2-dev@example.com");
  assert.equal(
    getCachedMessageDetail(11530),
    null,
    "invalidateMessageDetailCache(message_id) must evict cached entry keyed by db_id"
  );
});
