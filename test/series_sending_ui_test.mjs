import test from "node:test";
import assert from "node:assert/strict";

const toasts = [];
let appendedModal = null;

const origSetTimeout = globalThis.setTimeout;
globalThis.setTimeout = (fn, ms, ...args) => {
  const timer = origSetTimeout(fn, ms, ...args);
  if (timer && typeof timer.unref === "function") {
    timer.unref();
  }
  return timer;
};

globalThis.window = {
  location: { origin: "http://127.0.0.1:8080", hash: "", pathname: "/", search: "" },
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

function createElementMock() {
  let inner = "";
  const listeners = new Map();
  const childMocks = new Map();
  const el = {
    id: "",
    className: "",
    innerText: "",
    textContent: "",
    disabled: false,
    get innerHTML() {
      return inner;
    },
    set innerHTML(val) {
      inner = String(val || "");
      childMocks.clear();
    },
    addEventListener(type, fn) {
      listeners.set(type, fn);
    },
    async trigger(type, ev = { stopPropagation() {}, preventDefault() {} }) {
      const fn = listeners.get(type);
      if (fn) return await fn(ev);
    },
    querySelector(sel) {
      if (!sel.startsWith("#")) return null;
      const targetId = sel.slice(1);
      if (!inner.includes(`id="${targetId}"`)) return null;
      if (!childMocks.has(sel)) {
        const child = createElementMock();
        child.id = targetId;
        childMocks.set(sel, child);
      }
      return childMocks.get(sel);
    },
    querySelectorAll() {
      return [];
    },
    remove() {
      if (appendedModal === el) {
        appendedModal = null;
      }
    },
  };
  return el;
}

globalThis.document = {
  addEventListener: () => {},
  body: {
    appendChild(el) {
      appendedModal = el;
    },
  },
  getElementById(id) {
    if (id === "toast-container") {
      return {
        appendChild(el) {
          toasts.push({ className: el.className, text: el.innerText || el.textContent || "" });
        },
      };
    }
    if (id === "series-confirm-modal-backdrop") {
      return appendedModal;
    }
    return null;
  },
  createElement() {
    return createElementMock();
  },
};
globalThis.confirm = () => true;

import { store } from "../frontend/src/store.js";
import { renderSeriesWorkspace } from "../frontend/src/components/SeriesWorkspace.js";
import { renderSummaryPane } from "../frontend/src/components/SummaryPane.js";

function createWorkspaceContainer() {
  let html = "";
  const idListeners = new Map();
  const makeBtn = (sel) => {
    if (!idListeners.has(sel)) {
      const listeners = new Map();
      idListeners.set(sel, {
        disabled: false,
        addEventListener(type, fn) {
          listeners.set(type, fn);
        },
        async trigger(type, ev = { stopPropagation() {}, preventDefault() {} }) {
          const fn = listeners.get(type);
          if (fn) return await fn(ev);
        },
      });
    }
    return idListeners.get(sel);
  };
  return {
    get innerHTML() {
      return html;
    },
    set innerHTML(val) {
      html = String(val || "");
      idListeners.clear();
    },
    querySelector(sel) {
      if (!sel.startsWith("#")) return null;
      const targetId = sel.slice(1);
      if (!html.includes(`id="${targetId}"`)) return null;
      return makeBtn(sel);
    },
    querySelectorAll() {
      return [];
    },
  };
}

function isButtonDisabledInHtml(html, id) {
  const re = new RegExp(`<button[^>]*id="${id}"[^>]*>`);
  const match = html.match(re);
  if (!match) return null;
  return /\bdisabled\b/.test(match[0]);
}

const baseSeriesDetail = {
  series_id: "ser_send_001",
  series_key: "topic/send-anim:v1",
  branch: "topic/send-anim",
  version: 1,
  from_email: "dev@example.com",
  to_addrs: ["dev@kernel.org"],
  cc_addrs: [],
  in_reply_to: "",
  status: "draft",
  send_delay_sec: 2,
  total_count: 2,
  sent_count: 0,
  is_sending_now: false,
  warnings: [],
  items: [
    {
      series_id: "ser_send_001",
      seq_index: 0,
      is_cover_letter: true,
      draft_msg_id: "6001",
      rfc_message_id: "<cover-6001@example.com>",
      subject: "[PATCH 0/1] demo cover",
      body: "Cover body",
      status: "draft",
      extra_to: [],
      extra_cc: [],
    },
    {
      series_id: "ser_send_001",
      seq_index: 1,
      is_cover_letter: false,
      draft_msg_id: "6002",
      rfc_message_id: "<patch-6002@example.com>",
      subject: "[PATCH 1/1] demo patch",
      body: "Patch body",
      status: "draft",
      extra_to: [],
      extra_cc: [],
    },
  ],
};

test("SeriesWorkspace shows spinning hourglass and disables controls while sending series", async () => {
  toasts.length = 0;
  appendedModal = null;

  let currentServerDetail = structuredClone(baseSeriesDetail);
  let resolveSendCall;
  const sendCallPromise = new Promise((resolve) => {
    resolveSendCall = resolve;
  });

  const origFetch = globalThis.fetch;
  globalThis.fetch = async (url, opts = {}) => {
    const urlStr = String(url);
    if (urlStr.endsWith("/api/series/ser_send_001") && (!opts.method || opts.method === "GET")) {
      return {
        ok: true,
        status: 200,
        json: async () => structuredClone(currentServerDetail),
      };
    }
    if (urlStr.endsWith("/api/series/ser_send_001/send") && opts.method === "POST") {
      await sendCallPromise;
      currentServerDetail = {
        ...structuredClone(baseSeriesDetail),
        status: "sending",
        is_sending_now: true,
        items: [
          { ...baseSeriesDetail.items[0], status: "sending" },
          { ...baseSeriesDetail.items[1], status: "draft" },
        ],
      };
      return {
        ok: true,
        status: 202,
        json: async () => ({ success: true, series_id: "ser_send_001", status: "sending" }),
      };
    }
    if (urlStr.endsWith("/api/series/ser_send_001/cancel") && opts.method === "POST") {
      return {
        ok: true,
        status: 200,
        json: async () => ({ success: true, series_id: "ser_send_001", cancelled: true }),
      };
    }
    throw new Error(`Unexpected fetch: ${urlStr}`);
  };

  try {
    store.setConfig({ has_external_editor: true, translate: { enabled: true, target_lang: "ja" } });
    store.setFolders([{ id: 2, name: "Drafts", unread_count: 0, is_imap: false }]);
    store.setSeriesList([structuredClone(baseSeriesDetail)]);
    store.selectFolder(2);
    store.selectSeries("ser_send_001");

    const container = createWorkspaceContainer();
    const stateRef = {};
    await renderSeriesWorkspace(container, "ser_send_001", stateRef);

    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-send"), false);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-test-self"), false);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-discard"), false);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-ext-editor"), false);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-translate"), false);
    assert.equal(container.innerHTML.includes("spinner-hourglass"), false);

    // Click Send Series -> Confirm in modal
    await container.querySelector("#btn-series-send").trigger("click");
    assert.notEqual(appendedModal, null);
    const confirmPromise = appendedModal.querySelector("#btn-confirm-send-series").trigger("click");

    // Immediately while POST /send is in-flight, hourglass spinner appears and buttons are disabled
    assert.equal(container.innerHTML.includes("spinner-hourglass"), true);
    assert.equal(container.innerHTML.includes("⏳"), true);
    assert.equal(store.isAnySeriesSending(), true);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-send"), true);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-test-self"), true);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-discard"), true);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-save-headers"), true);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-mode-preview"), true);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-mode-edit"), true);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-translate"), true);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-ext-editor"), true);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-pause"), false);

    // Finish POST /send
    resolveSendCall();
    await confirmPromise;

    assert.equal(container.innerHTML.includes("series-item-badge sending"), true);
    assert.equal(container.innerHTML.includes("spinner-hourglass"), true);

    // Per-item sent SSE progress must keep series status as "sending" (not prematurely "sent")
    store.updateSeriesProgress({
      series_id: "ser_send_001",
      seq_index: 0,
      status: "sent",
      sent_count: 1,
      total_count: 2,
    });
    assert.equal(store.getState().seriesList[0].status, "sending");
    assert.equal(store.isAnySeriesSending(), true);

    // Clicking Pause disables the Pause button with hourglass animation
    await container.querySelector("#btn-series-pause").trigger("click");
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-pause"), true);
    assert.equal(container.innerHTML.includes("Pausing…"), true);

    // Final series-level sent event clears busy state
    store.updateSeriesProgress({
      series_id: "ser_send_001",
      status: "sent",
      sent_count: 2,
      total_count: 2,
    });
    assert.equal(store.getState().seriesList[0].status, "sent");
    assert.equal(store.isAnySeriesSending(), false);
  } finally {
    globalThis.fetch = origFetch;
    store.setSeriesBusy("ser_send_001", false);
    store.selectSeries(null);
    store.setSeriesList([]);
    store.setConfig(null);
  }
});

test("SeriesWorkspace locks buttons and shows spinning hourglass during Test Send to Self", async () => {
  toasts.length = 0;

  let resolveTestSend;
  const testSendPromise = new Promise((resolve) => {
    resolveTestSend = resolve;
  });

  const origFetch = globalThis.fetch;
  globalThis.fetch = async (url, opts = {}) => {
    const urlStr = String(url);
    if (urlStr.endsWith("/api/series/ser_send_001") && (!opts.method || opts.method === "GET")) {
      return {
        ok: true,
        status: 200,
        json: async () => structuredClone(baseSeriesDetail),
      };
    }
    if (urlStr.endsWith("/api/series/ser_send_001/test-send-self") && opts.method === "POST") {
      await testSendPromise;
      return {
        ok: true,
        status: 200,
        json: async () => ({ success: true, sent_count: 2 }),
      };
    }
    throw new Error(`Unexpected fetch: ${urlStr}`);
  };

  try {
    store.setConfig({ has_external_editor: true, translate: { enabled: true, target_lang: "ja" } });
    store.setFolders([{ id: 2, name: "Drafts", unread_count: 0, is_imap: false }]);
    store.setSeriesList([structuredClone(baseSeriesDetail)]);
    store.selectFolder(2);
    store.selectSeries("ser_send_001");

    const summarySlots = new Map();
    const summaryContainer = {
      innerHTML: "",
      querySelector(sel) {
        if (!summarySlots.has(sel)) {
          summarySlots.set(sel, {
            innerHTML: "",
            addEventListener() {},
            querySelector() {
              return null;
            },
            querySelectorAll() {
              return [];
            },
          });
        }
        return summarySlots.get(sel);
      },
    };
    renderSummaryPane(summaryContainer);

    const container = createWorkspaceContainer();
    const stateRef = {};
    await renderSeriesWorkspace(container, "ser_send_001", stateRef);

    const clickPromise = container.querySelector("#btn-series-test-self").trigger("click");

    // While test-send-self is running: buttons locked, hourglass spinning in workspace & SummaryPane
    assert.equal(container.innerHTML.includes("spinner-hourglass"), true);
    assert.equal(container.innerHTML.includes("Sending Test…"), true);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-test-self"), true);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-send"), true);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-discard"), true);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-save-headers"), true);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-mode-edit"), true);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-ext-editor"), true);
    assert.equal(
      summaryContainer.querySelector(".summary-table-container").innerHTML.includes("spinner-hourglass"),
      true
    );

    resolveTestSend();
    await clickPromise;

    // After test-send-self completes: buttons re-enabled and hourglass cleared
    assert.equal(container.innerHTML.includes("spinner-hourglass"), false);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-test-self"), false);
    assert.equal(isButtonDisabledInHtml(container.innerHTML, "btn-series-send"), false);
    assert.equal(store.isAnySeriesSending(), false);
  } finally {
    globalThis.fetch = origFetch;
    store.setSeriesBusy("ser_send_001", false);
    store.selectSeries(null);
    store.setSeriesList([]);
    store.setConfig(null);
  }
});
