import test from "node:test";
import assert from "node:assert/strict";

const toastMessages = [];
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
globalThis.document = {
  addEventListener: () => {},
  getElementById: (id) => {
    if (id === "toast-container") {
      return {
        appendChild(el) {
          toastMessages.push(el.innerText || el.textContent || "");
        },
      };
    }
    return null;
  },
  createElement: () => ({
    className: "",
    innerText: "",
    textContent: "",
    style: {},
    classList: { add() {}, remove() {} },
    remove() {},
  }),
};

import { store } from "../frontend/src/store.js";
import { translateMessage, clearTranslationCache } from "../frontend/src/api.js";
import { renderViewerPane } from "../frontend/src/components/ViewerPane.js";

function createMockElement(id = "") {
  const listeners = new Map();
  const children = new Map();
  const classSet = new Set();

  const el = {
    id,
    _innerHTML: "",
    textContent: "",
    innerText: "",
    title: "",
    disabled: false,
    style: { display: "block" },
    classList: {
      add(c) {
        classSet.add(c);
      },
      remove(c) {
        classSet.delete(c);
      },
      toggle(c, force) {
        if (force === undefined) {
          if (classSet.has(c)) classSet.delete(c);
          else classSet.add(c);
        } else if (force) {
          classSet.add(c);
        } else {
          classSet.delete(c);
        }
      },
      contains(c) {
        return classSet.has(c);
      },
    },
    get innerHTML() {
      return this._innerHTML;
    },
    set innerHTML(val) {
      this._innerHTML = String(val || "");
      children.clear();
    },
    addEventListener(type, fn) {
      listeners.set(type, fn);
    },
    async trigger(type, ev = { stopPropagation() {}, preventDefault() {} }) {
      const fn = listeners.get(type);
      if (fn) return await fn(ev);
    },
    querySelector(sel) {
      if (sel.startsWith("#")) {
        const targetId = sel.slice(1);
        const selfHas = this._innerHTML.includes(`id="${targetId}"`);
        const bodyChild = children.get("#viewer-diff-body");
        if (!selfHas && bodyChild && bodyChild !== this) {
          const foundInBody = bodyChild.querySelector(sel);
          if (foundInBody) return foundInBody;
        }
        if (!selfHas) return null;
        if (!children.has(sel)) {
          const child = createMockElement(targetId);
          // Populate initial textContent if button is in innerHTML
          const btnMatch = this._innerHTML.match(
            new RegExp(`id="${targetId}"[^>]*>([\\s\\S]*?)<\\/button>`)
          );
          if (btnMatch) {
            child.textContent = btnMatch[1].replace(/<[^>]+>/g, "").trim();
          }
          children.set(sel, child);
        }
        return children.get(sel);
      }
      return null;
    },
    querySelectorAll() {
      return [];
    },
  };
  return el;
}

test("translateMessage API throws descriptive error when local translation server is offline and does not cache failure", async () => {
  clearTranslationCache();
  let serverOnline = false;
  let callCount = 0;

  globalThis.fetch = async (url) => {
    callCount++;
    if (!serverOnline) {
      return {
        ok: false,
        status: 503,
        json: async () => ({
          error:
            "Local translation server is not running at http://127.0.0.1:8787. Please start local-jp-translate and try again.",
          code: "translate_server_unreachable",
        }),
      };
    }
    return {
      ok: true,
      status: 200,
      json: async () => ({
        id: 101,
        message_id: "msg-101@kernel.org",
        source_lang: "en",
        target_lang: "ja",
        model: "plamo-3-translate",
        formatted_body: "Unwrapped English sentence.",
        translated_body: "折り返し解除された英語の文です。",
        translated_subject: "件名の翻訳",
      }),
    };
  };

  await assert.rejects(
    async () => {
      await translateMessage(101);
    },
    (err) => {
      assert.ok(err.message.includes("Local translation server is not running"));
      return true;
    }
  );
  assert.equal(callCount, 1);

  // Now bring server online and verify retry succeeds (error was not cached)
  serverOnline = true;
  const res = await translateMessage(101);
  assert.equal(callCount, 2);
  assert.equal(res.translated_body, "折り返し解除された英語の文です。");

  // Subsequent call uses cache
  const cachedRes = await translateMessage(101);
  assert.equal(callCount, 2);
  assert.equal(cachedRes.translated_subject, "件名の翻訳");
});

test("ViewerPane renders Translate button, shows error toast when server is not running, and toggles translated view", async () => {
  clearTranslationCache();
  toastMessages.length = 0;
  let serverOnline = false;

  globalThis.fetch = async (url) => {
    const u = String(url);
    if (u.endsWith("/translate")) {
      if (!serverOnline) {
        return {
          ok: false,
          status: 503,
          json: async () => ({
            error:
              "Local translation server is not running at http://127.0.0.1:8787. Please start local-jp-translate and try again.",
          }),
        };
      }
      return {
        ok: true,
        status: 200,
        json: async () => ({
          id: 202,
          message_id: "msg-202@kernel.org",
          source_lang: "en",
          target_lang: "ja",
          model: "plamo-3-translate",
          formatted_body:
            "This is a line wrapped around 80 columns.\n\n> Quoted reply sentence.",
          translated_body:
            "これは80桁で折り返された行です。\n\n> 引用された返信文です。",
          translated_subject: "Re: [PATCH] リングバッファ修正",
        }),
      };
    }
    return { ok: true, status: 200, json: async () => ({}) };
  };

  store.setConfig({
    translate: {
      enabled: true,
      url: "http://127.0.0.1:8787",
      source_lang: "en",
      target_lang: "ja",
      style: "default",
    },
  });

  const container = createMockElement("viewer-pane");
  renderViewerPane(container);

  store.setActiveMessageDetail({
    message: {
      id: 202,
      message_id: "msg-202@kernel.org",
      subject: "Re: [PATCH] ring buffer fix",
      from_name: "Kernel Developer",
      from_email: "dev@example.com",
      to_addr: "linux-kernel@vger.kernel.org",
      date: "2026-11-10",
      is_read: 1,
      is_patch: 0,
      body_plain: "This is a line wrapped\naround 80 columns.\n\n> Quoted reply\n> sentence.",
    },
    comments: [],
    attachments: [],
  });

  const translateBtn = container.querySelector("#btn-translate-message");
  assert.ok(translateBtn, "Translate button should exist in ViewerPane toolbar");
  assert.equal(translateBtn.textContent, "🌐 Translate (JA)");

  // 1. Click while local-jp-translate is offline
  await translateBtn.trigger("click");
  assert.equal(translateBtn.disabled, false, "Button should be re-enabled after failure");
  assert.equal(translateBtn.textContent, "🌐 Translate (JA)");
  assert.ok(
    toastMessages.some((m) => m.includes("Local translation server is not running")),
    `Expected error toast when server is offline, got: ${JSON.stringify(toastMessages)}`
  );
  const bodyEl = container.querySelector("#viewer-diff-body");
  assert.ok(
    !bodyEl.innerHTML.includes("viewer-translated-box"),
    "Translated box must not appear when translation server is offline"
  );

  // 2. Turn server online and click Translate again
  serverOnline = true;
  await translateBtn.trigger("click");
  assert.equal(translateBtn.textContent, "🌐 Original");
  assert.ok(
    bodyEl.innerHTML.includes("viewer-translated-box"),
    "Translated box should be rendered in body container"
  );
  assert.ok(
    bodyEl.innerHTML.includes("これは80桁で折り返された行です。"),
    "Translated prose should be rendered"
  );
  assert.ok(
    bodyEl.innerHTML.includes("translated-quote-line") &&
      bodyEl.innerHTML.includes("&gt; 引用された返信文です。"),
    "Translated quote line should preserve '>' prefix and quote class"
  );

  // 3. Toggle to Reflowed English inside the translated box
  const toggleSourceBtn = bodyEl.querySelector("#btn-toggle-translated-source");
  assert.ok(toggleSourceBtn, "Toggle source button should exist inside translated box");
  await toggleSourceBtn.trigger("click");
  assert.ok(
    bodyEl.innerHTML.includes("This is a line wrapped around 80 columns."),
    "Reflowed English text should be displayed"
  );

  // 4. Click toolbar Translate (Original) button to close translation
  await translateBtn.trigger("click");
  assert.equal(translateBtn.textContent, "🌐 Translate (JA)");
  assert.ok(
    !bodyEl.innerHTML.includes("viewer-translated-box"),
    "Translated box should be hidden when toggled back to Original"
  );
});

test("Background translation queue and message switching preserves in-flight state and queues in FIFO order", async () => {
  clearTranslationCache();
  const startedOrder = [];
  const resolvers = new Map();

  globalThis.fetch = async (url) => {
    const u = String(url);
    const m = u.match(/\/api\/messages\/(\d+)\/translate$/);
    if (m) {
      const id = Number(m[1]);
      startedOrder.push(id);
      return new Promise((resolve) => {
        resolvers.set(id, () =>
          resolve({
            ok: true,
            status: 200,
            json: async () => ({
              id,
              message_id: `msg-${id}@kernel.org`,
              source_lang: "en",
              target_lang: "ja",
              model: "plamo-3-translate",
              formatted_body: `Formatted ${id}`,
              translated_body: `翻訳結果 ${id}`,
              translated_subject: `件名 ${id}`,
            }),
          })
        );
      });
    }
    return { ok: true, status: 200, json: async () => ({}) };
  };

  const container = createMockElement("viewer-pane");
  renderViewerPane(container);

  const msgA = {
    message: {
      id: 301,
      message_id: "msg-301@kernel.org",
      subject: "Patch A",
      from_name: "Kernel Developer",
      from_email: "dev@example.com",
      to_addr: "linux-kernel@vger.kernel.org",
      date: "2026-11-10",
      is_read: 1,
      is_patch: 0,
      body_plain: "Body A",
    },
    comments: [],
    attachments: [],
  };

  const msgB = {
    message: {
      id: 302,
      message_id: "msg-302@kernel.org",
      subject: "Patch B",
      from_name: "Kernel Developer",
      from_email: "dev@example.com",
      to_addr: "linux-kernel@vger.kernel.org",
      date: "2026-11-10",
      is_read: 1,
      is_patch: 0,
      body_plain: "Body B",
    },
    comments: [],
    attachments: [],
  };

  // 1. Select Message A and click Translate (do not await completion since fetch is held open)
  store.setActiveMessageDetail(msgA);
  let btnA = container.querySelector("#btn-translate-message");
  btnA.trigger("click");
  await new Promise((r) => origSetTimeout(r, 5));

  assert.equal(btnA.disabled, true);
  assert.equal(btnA.textContent, "⏳ Translating...");
  assert.deepEqual(startedOrder, [301], "Message 301 should start immediately");

  // 2. Switch to Message B while Message A is still translating, and click Translate on Message B
  store.setActiveMessageDetail(msgB);
  const btnB = container.querySelector("#btn-translate-message");
  assert.equal(btnB.disabled, false, "Message B button should initially be enabled");
  assert.equal(btnB.textContent, "🌐 Translate (JA)");

  btnB.trigger("click");
  await new Promise((r) => origSetTimeout(r, 5));
  assert.equal(btnB.disabled, true);
  assert.equal(btnB.textContent, "⏳ Translating...");
  // Message 302 is queued behind 301 and has not called fetch yet
  assert.deepEqual(startedOrder, [301], "Message 302 should wait in queue until 301 finishes");

  // 3. Switch back to Message A while it is still translating -> button must still show Translating...
  store.setActiveMessageDetail(msgA);
  btnA = container.querySelector("#btn-translate-message");
  assert.equal(btnA.disabled, true, "Returning to in-flight Message A must keep button disabled");
  assert.equal(btnA.textContent, "⏳ Translating...");

  // 4. Complete Message A -> Message A view updates immediately and Message B starts in background
  resolvers.get(301)();
  await new Promise((r) => origSetTimeout(r, 10));

  const bodyEl = container.querySelector("#viewer-diff-body");
  assert.ok(bodyEl.innerHTML.includes("翻訳結果 301"), "Message A should auto-render its translation upon completion");
  assert.equal(btnA.textContent, "🌐 Original");
  assert.deepEqual(startedOrder, [301, 302], "Message 302 should now start after 301 completed");

  // 5. Complete Message B while still viewing Message A, then switch to Message B -> already translated!
  resolvers.get(302)();
  await new Promise((r) => origSetTimeout(r, 10));

  store.setActiveMessageDetail(msgB);
  const bodyElB = container.querySelector("#viewer-diff-body");
  assert.ok(bodyElB.innerHTML.includes("翻訳結果 302"), "Message B should show completed background translation when selected");
});

