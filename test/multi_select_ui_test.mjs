import test from "node:test";
import assert from "node:assert/strict";

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
globalThis.confirm = () => true;

import { store } from "../frontend/src/store.js";
import { deleteMessagesBatch, setMessagesTagBatch } from "../frontend/src/api.js";
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
      trigger(type, ev = {}) {
        const fn = listeners.get(type);
        if (fn) return fn(ev);
      },
      querySelector(sel) {
        if (sel === ".summary-tbody") {
          if (!childListeners.has(".summary-tbody")) {
            const tbodyListeners = new Map();
            childListeners.set(".summary-tbody", {
              addEventListener(type, fn) {
                tbodyListeners.set(type, fn);
              },
              trigger(type, ev) {
                const fn = tbodyListeners.get(type);
                if (fn) return fn(ev);
              },
              querySelectorAll() {
                return [];
              },
              querySelector() {
                return null;
              },
            });
          }
          return childListeners.get(".summary-tbody");
        }
        if (sel.startsWith("#")) {
          if (!this.innerHTML.includes(`id="${sel.slice(1)}"`)) return null;
          if (!childListeners.has(sel)) {
            const elListeners = new Map();
            childListeners.set(sel, {
              disabled: false,
              checked: this.innerHTML.includes(`id="${sel.slice(1)}" checked`),
              style: { display: this.innerHTML.includes(`${sel.slice(1)}" style="display: none;"`) ? "none" : "block" },
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

const sampleMessages = [
  {
    id: 101,
    folder_id: 1,
    message_id: "msg-101@example.com",
    subject: "[PATCH 1/3] tracing: first patch",
    from_name: "Kernel Developer",
    from_addr: "dev@example.com",
    date: "2026-11-05T10:00:00Z",
    epoch_timestamp: 1762336800,
    is_read: 0,
    is_replied: 0,
    tag_key: "",
    tag_name: "",
    tag_color: "",
  },
  {
    id: 102,
    folder_id: 1,
    message_id: "msg-102@example.com",
    subject: "[PATCH 2/3] tracing: second patch",
    from_name: "Kernel Developer",
    from_addr: "dev@example.com",
    date: "2026-11-05T10:01:00Z",
    epoch_timestamp: 1762336860,
    is_read: 1,
    is_replied: 0,
    tag_key: "",
    tag_name: "",
    tag_color: "",
  },
  {
    id: 103,
    folder_id: 1,
    message_id: "msg-103@example.com",
    subject: "[PATCH 3/3] tracing: third patch",
    from_name: "Kernel Developer",
    from_addr: "dev@example.com",
    date: "2026-11-05T10:02:00Z",
    epoch_timestamp: 1762336920,
    is_read: 1,
    is_replied: 0,
    tag_key: "",
    tag_name: "",
    tag_color: "",
  },
];

test("Multi-selection: Ctrl+Click enters selection mode, renders checkboxes and top action bar", () => {
  const container = createSummaryMockContainer();
  store.setFolders([{ id: 1, name: "INBOX", unread_count: 1, sync_enabled: true }]);
  store.selectFolder(1);
  store.setMessages(sampleMessages.map((m) => ({ ...m })));
  renderSummaryPane(container);

  const selectionSlot = container.querySelector("#summary-selection-slot");
  const tableContainer = container.querySelector(".summary-table-container");

  // Initially not in multi-select mode
  assert.equal(store.getState().isMultiSelectMode, false);
  assert.equal(selectionSlot.innerHTML, "");
  assert.equal(tableContainer.innerHTML.includes("summary-select-checkbox"), false);

  // Simulate Ctrl+Click on row index 0 (id=103 since default sort is date desc)
  const firstRowMsgId = store.getState().messages[0].id;
  const tbody = tableContainer.querySelector(".summary-tbody");
  tbody.trigger("click", {
    ctrlKey: true,
    shiftKey: false,
    preventDefault() {},
    stopPropagation() {},
    target: {
      closest(sel) {
        if (sel === ".summary-row") {
          return {
            getAttribute(attr) {
              if (attr === "data-id") return String(firstRowMsgId);
              if (attr === "data-index") return "0";
              return null;
            },
          };
        }
        return null;
      },
    },
  });

  // Verify selection mode is active, action bar is rendered, and checkboxes are rendered on rows
  assert.equal(store.getState().isMultiSelectMode, true);
  assert.equal(store.getState().selectedMessageIds.has(firstRowMsgId), true);
  assert.ok(
    selectionSlot.innerHTML.includes("id=\"summary-selection-bar\""),
    "Expected top-line selection action bar to be rendered"
  );
  assert.ok(
    selectionSlot.innerHTML.includes("1 selected"),
    "Expected '1 selected' badge in top action bar"
  );
  assert.ok(
    selectionSlot.innerHTML.includes("🏷️ Tag them"),
    "Expected 'Tag them' button in top action bar"
  );
  assert.ok(
    selectionSlot.innerHTML.includes("🗑️ Trash them"),
    "Expected 'Trash them' button in top action bar"
  );
  assert.ok(
    tableContainer.innerHTML.includes("class=\"summary-select-checkbox\""),
    "Expected selection checkbox on each message row"
  );

  // Simulate Shift+Click on row index 2 to select range 0..2 (all 3 messages)
  const thirdRowMsgId = store.getState().messages[2].id;
  const tbodyAfter = tableContainer.querySelector(".summary-tbody");
  tbodyAfter.trigger("click", {
    ctrlKey: false,
    shiftKey: true,
    preventDefault() {},
    stopPropagation() {},
    target: {
      closest(sel) {
        if (sel === ".summary-row") {
          return {
            getAttribute(attr) {
              if (attr === "data-id") return String(thirdRowMsgId);
              if (attr === "data-index") return "2";
              return null;
            },
          };
        }
        return null;
      },
    },
  });

  assert.equal(store.getState().selectedMessageIds.size, 3);
  assert.ok(selectionSlot.innerHTML.includes("3 selected"));
});

test("Multi-selection: 'Tag them' updates color tag across all selected messages", async () => {
  const container = createSummaryMockContainer();
  store.clearMultiSelection();
  store.selectFolder(1);
  store.setMessages(sampleMessages.map((m) => ({ ...m })));
  renderSummaryPane(container);

  store.toggleMultiSelectMessage(101, 2);
  store.toggleMultiSelectMessage(102, 1);
  assert.equal(store.getState().selectedMessageIds.size, 2);

  const taggedCalls = [];
  globalThis.fetch = async (url, opts) => {
    if (String(url).endsWith("/tag") && opts?.method === "POST") {
      const body = JSON.parse(opts.body);
      taggedCalls.push({ url: String(url), tag: body.tag });
      return {
        ok: true,
        async json() {
          return {
            tag_key: "$cl_1",
            tag_name: "Red",
            tag_color: "#CC0000",
          };
        },
      };
    }
    return { ok: true, async json() { return {}; } };
  };

  const ids = Array.from(store.getState().selectedMessageIds);
  const res = await setMessagesTagBatch(ids, "$cl_1");
  store.updateMessagesTag(ids, res.tag_key, res.tag_name, res.tag_color);

  assert.equal(taggedCalls.length, 2);
  const tableHtml = container.querySelector(".summary-table-container").innerHTML;
  assert.ok(tableHtml.includes("Red"), "Expected tag badge 'Red' in summary table");
  assert.ok(tableHtml.includes("#CC0000"), "Expected tag color '#CC0000' in summary table");

  const msg101 = store.getState().rawMessages.find((m) => m.id === 101);
  const msg102 = store.getState().rawMessages.find((m) => m.id === 102);
  const msg103 = store.getState().rawMessages.find((m) => m.id === 103);
  assert.equal(msg101.tag_name, "Red");
  assert.equal(msg102.tag_name, "Red");
  assert.equal(msg103.tag_name, "");
});

test("Multi-selection: 'Trash them' deletes selected messages and exits selection mode", async () => {
  const container = createSummaryMockContainer();
  store.clearMultiSelection();
  store.selectFolder(1);
  store.setMessages(sampleMessages.map((m) => ({ ...m })));
  renderSummaryPane(container);

  store.toggleMultiSelectMessage(101);
  store.toggleMultiSelectMessage(103);
  assert.equal(store.getState().isMultiSelectMode, true);
  assert.equal(store.getState().selectedMessageIds.size, 2);

  const deletedUrls = [];
  globalThis.fetch = async (url, opts) => {
    if (opts?.method === "DELETE") {
      deletedUrls.push(String(url));
      return {
        ok: true,
        async json() {
          return { success: true };
        },
      };
    }
    if (String(url).endsWith("/api/folders")) {
      return {
        ok: true,
        async json() {
          return [{ id: 1, name: "INBOX", unread_count: 0, sync_enabled: true }];
        },
      };
    }
    return { ok: true, async json() { return {}; } };
  };

  const selectionSlot = container.querySelector("#summary-selection-slot");
  const trashBtn = selectionSlot.querySelector("#btn-bulk-trash");
  assert.ok(trashBtn, "Expected #btn-bulk-trash button to exist");
  await trashBtn.trigger("click");

  assert.equal(deletedUrls.length, 2);
  assert.ok(deletedUrls.includes("/api/messages/101"));
  assert.ok(deletedUrls.includes("/api/messages/103"));

  // Verify messages 101 and 103 are removed from store and UI, and selection mode exited
  assert.equal(store.getState().messages.length, 1);
  assert.equal(store.getState().messages[0].id, 102);
  assert.equal(store.getState().isMultiSelectMode, false);
  assert.equal(store.getState().selectedMessageIds.size, 0);
  assert.equal(selectionSlot.innerHTML, "");
});
