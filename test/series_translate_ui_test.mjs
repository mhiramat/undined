import test from "node:test";
import assert from "node:assert/strict";

const toasts = [];
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
globalThis.document = {
  addEventListener: () => {},
  getElementById(id) {
    if (id === "toast-container") {
      return {
        appendChild(el) {
          toasts.push({ className: el.className, text: el.innerText || el.textContent || "" });
        },
      };
    }
    return null;
  },
  createElement() {
    return {
      className: "",
      innerText: "",
      textContent: "",
      remove() {},
    };
  },
};
globalThis.confirm = () => true;

import { store } from "../frontend/src/store.js";
import { clearTranslationCache } from "../frontend/src/api.js";
import { renderSeriesWorkspace } from "../frontend/src/components/SeriesWorkspace.js";

function createWorkspaceContainer() {
  let html = "";
  const idListeners = new Map();
  const patchItemButtons = new Map();
  const inputValues = new Map();

  const makeButtonMock = (key, storeMap, attrs = {}) => {
    if (!storeMap.has(key)) {
      const listeners = new Map();
      storeMap.set(key, {
        disabled: false,
        get value() {
          return inputValues.get(key) ?? "";
        },
        set value(v) {
          inputValues.set(key, String(v));
        },
        getAttribute(name) {
          return attrs[name] ?? null;
        },
        addEventListener(type, fn) {
          listeners.set(type, fn);
        },
        async trigger(type, ev = { stopPropagation() {}, preventDefault() {} }) {
          const fn = listeners.get(type);
          if (fn) return await fn(ev);
        },
      });
    }
    return storeMap.get(key);
  };

  return {
    get innerHTML() {
      return html;
    },
    set innerHTML(val) {
      html = String(val || "");
      idListeners.clear();
      patchItemButtons.clear();
    },
    setInputValue(sel, val) {
      inputValues.set(sel, val);
    },
    querySelector(sel) {
      if (!sel.startsWith("#")) return null;
      const targetId = sel.slice(1);
      if (!html.includes(`id="${targetId}"`)) return null;
      return makeButtonMock(sel, idListeners);
    },
    querySelectorAll(sel) {
      if (sel === ".series-patch-item") {
        const matches = [...html.matchAll(/class="series-patch-item[^"]*"[^>]*data-seq-index="(\d+)"/g)];
        return matches.map((m) => makeButtonMock(`seq:${m[1]}`, patchItemButtons, { "data-seq-index": m[1] }));
      }
      return [];
    },
    getPatchButton(seqIndex) {
      return patchItemButtons.get(`seq:${seqIndex}`) || null;
    },
  };
}

const sampleSeriesDetail = {
  series_id: "ser_trans_001",
  series_key: "topic/demo:v1",
  branch: "topic/demo",
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
      series_id: "ser_trans_001",
      seq_index: 0,
      is_cover_letter: true,
      draft_msg_id: "5001",
      rfc_message_id: "<cover-5001@example.com>",
      subject: "[PATCH 0/1] demo: cover letter",
      body: "This series introduces a sample cleanup.",
      status: "draft",
      extra_to: [],
      extra_cc: [],
    },
    {
      series_id: "ser_trans_001",
      seq_index: 1,
      is_cover_letter: false,
      draft_msg_id: "5002",
      rfc_message_id: "<patch-5002@example.com>",
      subject: "[PATCH 1/1] demo: clean up helper",
      body: "Clean up helper logic.\n\nSigned-off-by: Kernel Developer <dev@example.com>\n---\ndiff --git a/demo.c b/demo.c\n@@ -1 +1 @@\n-old();\n+new();",
      status: "draft",
      extra_to: [],
      extra_cc: [],
    },
  ],
};

test("SeriesWorkspace hides Translate button when disabled and shows translated view when enabled", async () => {
  clearTranslationCache();
  toasts.length = 0;

  const translateCalls = [];
  const origFetch = globalThis.fetch;
  globalThis.fetch = async (url, opts = {}) => {
    const urlStr = String(url);
    if (urlStr.endsWith("/api/series/ser_trans_001") && (!opts.method || opts.method === "GET")) {
      return {
        ok: true,
        status: 200,
        json: async () => structuredClone(sampleSeriesDetail),
      };
    }
    if (urlStr.endsWith("/api/messages/5001/translate")) {
      translateCalls.push("5001");
      return {
        ok: true,
        status: 200,
        json: async () => ({
          id: 5001,
          source_lang: "en",
          target_lang: "ja",
          model: "local-test-model",
          formatted_body: "This series introduces a sample cleanup.",
          translated_body: "このシリーズはサンプルのクリーンアップを導入します。",
          translated_subject: "[PATCH 0/1] demo: カバーレター",
        }),
      };
    }
    if (urlStr.endsWith("/api/messages/5002/translate")) {
      translateCalls.push("5002");
      return {
        ok: true,
        status: 200,
        json: async () => ({
          id: 5002,
          source_lang: "en",
          target_lang: "ja",
          model: "local-test-model",
          formatted_body: "Clean up helper logic.",
          translated_body: "ヘルパーのロジックを整理します。",
          formatted_preamble: "Clean up helper logic.",
          translated_preamble: "ヘルパーのロジックを整理します。",
          translated_subject: "[PATCH 1/1] demo: ヘルパーを整理",
        }),
      };
    }
    if (urlStr.endsWith("/api/series/ser_trans_001/items/0") && opts.method === "PUT") {
      return {
        ok: true,
        status: 200,
        json: async () => ({ item: sampleSeriesDetail.items[0], warnings: [] }),
      };
    }
    throw new Error(`Unexpected fetch: ${urlStr}`);
  };

  try {
    store.selectSeries("ser_trans_001");
    store.setConfig({ translate: { enabled: false, target_lang: "ja" } });

    const container = createWorkspaceContainer();
    const stateRef = {};
    await renderSeriesWorkspace(container, "ser_trans_001", stateRef);

    // Disabled by default -> button must NOT appear
    assert.equal(container.querySelector("#btn-series-translate"), null);

    // Enable translation in config and re-render
    store.setConfig({ translate: { enabled: true, target_lang: "ja" } });
    await renderSeriesWorkspace(container, "ser_trans_001", stateRef);

    const translateBtn = container.querySelector("#btn-series-translate");
    assert.notEqual(translateBtn, null);
    assert.equal(container.innerHTML.includes("🌐 Translate (JA)"), true);

    // Translate cover letter (0/1)
    await translateBtn.trigger("click");
    assert.deepEqual(translateCalls, ["5001"]);
    assert.equal(container.innerHTML.includes('id="series-translated-box"'), true);
    assert.equal(container.innerHTML.includes("このシリーズはサンプルのクリーンアップを導入します。"), true);
    assert.equal(container.innerHTML.includes("🌐 Original"), true);

    // Switch to Reflowed English and back
    await container.querySelector("#btn-series-toggle-translated-source").trigger("click");
    assert.equal(container.innerHTML.includes("📐 Reflowed Email (Unwrapped English)"), true);
    assert.equal(container.innerHTML.includes("This series introduces a sample cleanup."), true);

    // Close translation via box button
    await container.querySelector("#btn-series-close-translation").trigger("click");
    assert.equal(container.innerHTML.includes('id="series-translated-box"'), false);

    // Switch to patch 1/1 (has diff --git) and translate -> shows translated description above diff
    await container.getPatchButton(1).trigger("click");
    await container.querySelector("#btn-series-translate").trigger("click");
    assert.deepEqual(translateCalls, ["5001", "5002"]);
    assert.equal(container.innerHTML.includes("🌐 Translated Patch Description (JA)"), true);
    assert.equal(container.innerHTML.includes("ヘルパーのロジックを整理します。"), true);
    assert.equal(container.innerHTML.includes("diff --git a/demo.c b/demo.c"), true);

    // Switch back to cover letter, edit and save -> invalidates cached translation
    await container.getPatchButton(0).trigger("click");
    await container.querySelector("#btn-series-mode-edit").trigger("click");
    container.setInputValue("#series-item-subject-input", "[PATCH 0/1] demo: updated cover");
    container.setInputValue("#series-item-body-textarea", "Updated cover text.");
    await container.querySelector("#btn-series-save-item").trigger("click");
    await container.querySelector("#btn-series-mode-preview").trigger("click");
    await container.querySelector("#btn-series-translate").trigger("click");
    assert.deepEqual(translateCalls, ["5001", "5002", "5001"]);
  } finally {
    globalThis.fetch = origFetch;
    store.selectSeries(null);
    store.setConfig(null);
    clearTranslationCache();
  }
});

test("SeriesWorkspace shows error toast and keeps button usable when translation server fails", async () => {
  clearTranslationCache();
  toasts.length = 0;

  const origFetch = globalThis.fetch;
  globalThis.fetch = async (url) => {
    const urlStr = String(url);
    if (urlStr.endsWith("/api/series/ser_trans_001")) {
      return {
        ok: true,
        status: 200,
        json: async () => structuredClone(sampleSeriesDetail),
      };
    }
    if (urlStr.endsWith("/api/messages/5001/translate")) {
      return {
        ok: false,
        status: 503,
        json: async () => ({ error: "Local translation server is not running" }),
      };
    }
    throw new Error(`Unexpected fetch: ${urlStr}`);
  };

  try {
    store.selectSeries("ser_trans_001");
    store.setConfig({ translate: { enabled: true, target_lang: "ja" } });

    const container = createWorkspaceContainer();
    await renderSeriesWorkspace(container, "ser_trans_001", {});

    await container.querySelector("#btn-series-translate").trigger("click");
    assert.equal(container.innerHTML.includes('id="series-translated-box"'), false);
    assert.equal(
      toasts.some((t) => t.className.includes("error") && t.text.includes("Local translation server is not running")),
      true
    );
    assert.equal(container.innerHTML.includes("🌐 Translate (JA)"), true);
  } finally {
    globalThis.fetch = origFetch;
    store.selectSeries(null);
    store.setConfig(null);
    clearTranslationCache();
  }
});
