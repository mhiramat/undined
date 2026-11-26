import test from "node:test";
import assert from "node:assert/strict";

const toasts = [];

globalThis.window = {
  location: { origin: "http://127.0.0.1:8080", hash: "" },
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
  getElementById(id) {
    if (id === "toast-container") {
      return {
        appendChild(el) {
          toasts.push({ className: el.className, text: el.innerText });
        },
      };
    }
    return null;
  },
  createElement() {
    return {
      className: "",
      innerText: "",
      remove() {},
    };
  },
};
globalThis.confirm = () => true;

import { store } from "../frontend/src/store.js";
import { EVENT_FOLDER_SELECTED } from "../frontend/src/constants.js";
import { renderSummaryPane } from "../frontend/src/components/SummaryPane.js";

function createSummaryMockContainer() {
  const slots = new Map();
  const makeSlot = () => {
    const listeners = new Map();
    const childListeners = new Map();
    const slot = {
      innerHTML: "",
      style: { display: "block" },
      addEventListener(type, fn) {
        listeners.set(type, fn);
      },
      querySelector(sel) {
        if (sel.startsWith("#")) {
          if (!this.innerHTML.includes(`id="${sel.slice(1)}"`)) return null;
          if (!childListeners.has(sel)) {
            const elListeners = new Map();
            childListeners.set(sel, {
              disabled: false,
              innerHTML: "",
              addEventListener(type, fn) {
                elListeners.set(type, fn);
              },
              trigger(type, ev = { stopPropagation() {}, preventDefault() {} }) {
                const fn = elListeners.get(type);
                if (fn) return fn(ev);
              },
              querySelectorAll() {
                return [];
              },
            });
          }
          return childListeners.get(sel);
        }
        return null;
      },
      querySelectorAll() {
        return [];
      },
      _resetChildren() {
        childListeners.clear();
      },
    };
    return new Proxy(slot, {
      set(target, prop, value) {
        if (prop === "innerHTML") {
          target._resetChildren();
        }
        target[prop] = value;
        return true;
      },
    });
  };

  return {
    innerHTML: "",
    querySelector(sel) {
      if (!slots.has(sel)) {
        slots.set(sel, makeSlot());
      }
      return slots.get(sel);
    },
  };
}

test("SummaryPane renders Drafts folder with numeric series version without throwing", () => {
  toasts.length = 0;
  const container = createSummaryMockContainer();
  renderSummaryPane(container);

  const observedFolderEvents = [];
  const unsub = store.subscribe((event, payload) => {
    if (event === EVENT_FOLDER_SELECTED) {
      observedFolderEvents.push(payload);
    }
  });

  try {
    store.setFolders([
      { id: 1, name: "INBOX", unread_count: 0, is_imap: true },
      { id: 2, name: "Drafts", unread_count: 0, is_imap: false },
    ]);
    store.setSeriesList([
      {
        series_id: "ser_test_001",
        series_key: "topic/demo:v2",
        branch: "topic/demo",
        version: 2,
        to_addrs: ["dev@kernel.org"],
        cc_addrs: [],
        status: "draft",
        updated_at: "2026-11-27T00:00:00Z",
        items: [
          { seq_index: 0, draft_msg_id: "201", subject: "[PATCH v2 0/1] demo cover", status: "draft" },
          { seq_index: 1, draft_msg_id: "202", subject: "[PATCH v2 1/1] demo change", status: "draft" },
        ],
      },
    ]);

    store.selectFolder(2);
    assert.deepEqual(observedFolderEvents, [2]);

    store.setMessages([
      { id: 201, folder_id: 2, subject: "[PATCH v2 0/1] demo cover", from_addr: "dev@example.com", date: "2026-11-27T00:00:00Z", is_read: 1 },
      { id: 202, folder_id: 2, subject: "[PATCH v2 1/1] demo change", from_addr: "dev@example.com", date: "2026-11-27T00:00:01Z", is_read: 1 },
      { id: 203, folder_id: 2, subject: "Standalone draft", from_addr: "dev@example.com", date: "2026-11-27T00:00:02Z", is_read: 1 },
    ]);

    const tableHtml = container.querySelector(".summary-table-container").innerHTML;
    assert.equal(tableHtml.includes("series-queue-row"), true);
    assert.equal(tableHtml.includes("[Series v2 · 2 msgs]"), true);
    assert.equal(tableHtml.includes("[PATCH v2 0/1] demo cover"), true);
    assert.equal(tableHtml.includes("Standalone draft"), true);
    // Individual series item rows must be collapsed into the series queue row
    assert.equal(tableHtml.includes('data-id="201"'), false);
    assert.equal(tableHtml.includes('data-id="202"'), false);
    assert.equal(tableHtml.includes('data-id="203"'), true);
    assert.equal(tableHtml.includes("summary-render-error"), false);

    // Selecting a series then switching away from Drafts clears selectedSeriesId
    store.selectSeries("ser_test_001");
    assert.equal(store.getState().selectedSeriesId, "ser_test_001");
    store.selectFolder(1);
    assert.equal(store.getState().selectedSeriesId, null);
  } finally {
    unsub();
    store.setSeriesList([]);
    store.setSummaryError(null);
  }
});

test("SummaryPane surfaces folder load errors via inline banner and toast", () => {
  toasts.length = 0;
  const container = createSummaryMockContainer();
  renderSummaryPane(container);

  store.setFolders([
    { id: 1, name: "INBOX", unread_count: 0, is_imap: true },
    { id: 2, name: "Drafts", unread_count: 0, is_imap: false },
  ]);
  store.selectFolder(2);

  store.setSummaryError("Failed to load folder messages: HTTP 500 <boom>");

  const errorSlot = container.querySelector("#summary-error-slot");
  assert.equal(errorSlot.innerHTML.includes('id="summary-error-banner"'), true);
  assert.equal(errorSlot.innerHTML.includes("Failed to load folder messages: HTTP 500 &lt;boom&gt;"), true);
  assert.equal(errorSlot.innerHTML.includes('id="btn-summary-retry-load"'), true);
  assert.equal(toasts.some((t) => t.className.includes("error") && t.text.includes("HTTP 500")), true);

  // Dismiss button clears the error banner
  errorSlot.querySelector("#btn-summary-dismiss-error").trigger("click");
  assert.equal(store.getState().summaryError, null);
  assert.equal(errorSlot.innerHTML, "");

  // Setting messages also clears any active summary error
  store.setSummaryError("Temporary load failure");
  assert.equal(errorSlot.innerHTML.includes("Temporary load failure"), true);
  store.setMessages([]);
  assert.equal(store.getState().summaryError, null);
  assert.equal(errorSlot.innerHTML, "");
});

test("SummaryPane catches unexpected render exceptions, shows WebUI error state, and isolates store listeners", () => {
  toasts.length = 0;
  const container = createSummaryMockContainer();
  renderSummaryPane(container);

  const downstreamEvents = [];
  const unsub = store.subscribe((event, payload) => {
    if (event === EVENT_FOLDER_SELECTED) {
      downstreamEvents.push(payload);
    }
  });

  try {
    store.setFolders([
      { id: 1, name: "INBOX", unread_count: 0, is_imap: true },
      { id: 2, name: "Drafts", unread_count: 0, is_imap: false },
    ]);
    store.selectFolder(1);

    // Inject a broken series entry whose getter throws during Drafts rendering
    const brokenSeries = {
      series_id: "ser_broken",
      get items() {
        throw new Error("corrupted series payload");
      },
    };
    store.state.seriesList = [brokenSeries];

    // Switching to Drafts must not throw or prevent downstream listeners from running
    assert.doesNotThrow(() => {
      store.selectFolder(2);
    });
    assert.deepEqual(downstreamEvents, [1, 2]);

    const tableHtml = container.querySelector(".summary-table-container").innerHTML;
    assert.equal(tableHtml.includes('id="summary-render-error"'), true);
    assert.equal(tableHtml.includes("Failed to render folder message list"), true);
    assert.equal(tableHtml.includes("corrupted series payload"), true);
    assert.equal(toasts.some((t) => t.className.includes("error") && t.text.includes("corrupted series payload")), true);
  } finally {
    unsub();
    store.setSeriesList([]);
    store.setSummaryError(null);
  }
});
