import test from "node:test";
import assert from "node:assert/strict";

// Minimal browser globals for frontend ES modules in Node test environment
globalThis.window = {
  location: { origin: "http://127.0.0.1:8080", search: "" },
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
import { clearMessageDetailCache } from "../frontend/src/api.js";
import { NotificationChannel } from "../frontend/src/events.js";
import { renderViewerPane } from "../frontend/src/components/ViewerPane.js";

function createMockContainer(clientWidth = 1200) {
  return {
    clientWidth,
    innerHTML: "",
    querySelectorAll() {
      return [];
    },
    querySelector(sel) {
      if (sel === "#viewer-diff-body" || sel === "#viewer-markdown-body") {
        if (!this._bodyContainer) {
          this._bodyContainer = createMockContainer(clientWidth);
        }
        return this._bodyContainer;
      }
      return null;
    },
  };
}

class MockEventSource {
  constructor(url) {
    this.url = url;
    this.listeners = new Map();
  }
  addEventListener(type, fn) {
    if (!this.listeners.has(type)) {
      this.listeners.set(type, []);
    }
    this.listeners.get(type).push(fn);
  }
  emit(type, payload) {
    const handlers = this.listeners.get(type) || [];
    for (const fn of handlers) {
      fn({ data: JSON.stringify(payload) });
    }
  }
  close() {}
}

test("store.getAiReviewStatusForTarget returns null once review completes, fails, or cancels", () => {
  const patchMid = "20261114195914.2678414-1-dev@example.com";
  const reviewDbId = 84;

  // Running state
  store.setAiReviewState({
    isRunning: true,
    messageId: patchMid,
    activeReviewDbId: reviewDbId,
    status: "running",
    queue: [],
  });
  assert.equal(store.getAiReviewStatusForTarget(patchMid), "in-progress");
  assert.equal(store.getAiReviewStatusForTarget(`<${patchMid}>`), "in-progress");
  assert.equal(store.getAiReviewStatusForTarget(reviewDbId), "in-progress");
  assert.equal(store.isAiReviewTarget(patchMid), true);
  assert.equal(store.isAiReviewTarget(reviewDbId), true);

  // Completed state — even if messageId and activeReviewDbId remain populated
  for (const terminalStatus of ["completed", "error", "cancelled", "idle"]) {
    store.setAiReviewState({
      isRunning: false,
      messageId: patchMid,
      activeReviewDbId: reviewDbId,
      status: terminalStatus,
      queue: [],
    });
    assert.equal(
      store.getAiReviewStatusForTarget(patchMid),
      null,
      `expected null for patch target in ${terminalStatus} state`
    );
    assert.equal(
      store.getAiReviewStatusForTarget(reviewDbId),
      null,
      `expected null for AI-reviews DB ID in ${terminalStatus} state`
    );
    assert.equal(store.isAiReviewTarget(patchMid), false);
    assert.equal(store.isAiReviewTarget(reviewDbId), false);
  }
});

test("ViewerPane hides Cancel Review button in AI-reviews folder after review completes via SSE", async () => {
  const patchMid = "20261114195914.2678414-1-dev@example.com";
  const patchDbId = 501;
  const reviewDbId = 84;

  const inProgressDetail = {
    message: {
      id: reviewDbId,
      folder_id: 9,
      message_id: "",
      subject: "AI review: [PATCH] sample: check bounds",
      from_name: "AI review",
      from_addr: "",
      date: "2026-11-15T10:00:00Z",
      is_read: 1,
      is_ai_review: true,
      review_commit_id: "abc123456789",
      body_plain: "In-progress",
    },
    reviewed_message: {
      id: patchDbId,
      folder_id: 1,
      folder_name: "INBOX",
      message_id: patchMid,
      subject: "[PATCH] sample: check bounds",
    },
    comments: [],
  };

  const completedDetail = {
    ...inProgressDetail,
    message: {
      ...inProgressDetail.message,
      body_plain: "## AI Review Summary\n\nNo regressions found in bounds check.",
    },
  };

  let mockEs = null;
  const origEventSource = globalThis.EventSource;
  const origFetch = globalThis.fetch;
  globalThis.EventSource = function (url) {
    mockEs = new MockEventSource(url);
    return mockEs;
  };
  globalThis.fetch = async (url) => {
    const u = String(url);
    if (u.includes(`/api/messages/${reviewDbId}`)) {
      return {
        ok: true,
        json: async () => completedDetail,
      };
    }
    if (u.includes("/api/folders")) {
      return {
        ok: true,
        json: async () => [{ id: 9, name: "AI-reviews", unread_count: 0 }],
      };
    }
    if (u.includes("/api/messages")) {
      return {
        ok: true,
        json: async () => [completedDetail.message],
      };
    }
    return { ok: true, json: async () => ({}) };
  };

  try {
    clearMessageDetailCache();
    const container = createMockContainer();
    renderViewerPane(container);

    const channel = new NotificationChannel(store);
    channel.connect();

    // 1. While AI review is running, Cancel Review button must be visible
    store.setAiReviewState({
      isRunning: true,
      messageId: patchMid,
      activeReviewDbId: reviewDbId,
      status: "running",
      queue: [],
    });
    store.setActiveMessageDetail(inProgressDetail);

    assert.ok(
      container.innerHTML.includes('id="btn-cancel-ai-review"'),
      "expected Cancel Review button while review is in-progress"
    );

    // 2. Backend emits terminal review.status_updated SSE event (carrying active_message_id & active_review_db_id)
    mockEs.emit("review.status_updated", {
      is_running: false,
      active_message_id: patchMid,
      active_commit_id: "abc123456789",
      active_review_db_id: reviewDbId,
      status: "completed",
      output: "## AI Review Summary\n\nNo regressions found in bounds check.",
      queue: [],
      queue_length: 0,
    });

    // Even immediately before fetchMessageDetail resolves, Cancel Review must be gone
    assert.ok(
      !container.innerHTML.includes('id="btn-cancel-ai-review"'),
      "Cancel Review button must be hidden immediately on completed status event"
    );
    assert.equal(store.getState().activeReview.messageId, null);
    assert.equal(store.getState().activeReview.activeReviewDbId, null);

    // Wait for async fetchMessageDetail + debouncedSyncFolders triggered by SSE handler
    await new Promise((r) => setTimeout(r, 330));

    assert.ok(
      !container.innerHTML.includes('id="btn-cancel-ai-review"'),
      "Cancel Review button must remain hidden after completed report body loads"
    );

    // 3. Re-opening the completed AI-review message later must also NOT show Cancel Review
    store.setActiveMessageDetail(completedDetail);
    assert.ok(
      !container.innerHTML.includes('id="btn-cancel-ai-review"'),
      "Cancel Review button must not appear when opening completed AI-review report"
    );
  } finally {
    globalThis.EventSource = origEventSource;
    globalThis.fetch = origFetch;
  }
});
