/**
 * SummaryPane: Top-right pane displaying threaded/flat summary table with keyboard selection,
 * thread expand/collapse controls, hamburger menu for view switching, and message filtering.
 */

import { store } from "../store.js";
import { cancelFolderSync, deleteMessagesBatch, fetchFolders, markMessageRead, setMessagesTagBatch } from "../api.js";
import { getTags } from "../tags.js";
import { openFilterDialog } from "./FilterDialog.js";
import { isDraftFolder, triggerNewMessage } from "./ViewerPane.js";
import {
  EVENT_MESSAGES_UPDATED,
  EVENT_FOLDER_SELECTED,
  EVENT_FILTER_UPDATED,
  EVENT_MESSAGE_SELECTED,
  EVENT_MESSAGE_READ_UPDATED,
  EVENT_MESSAGE_REPLIED_UPDATED,
  EVENT_MESSAGE_TAG_UPDATED,
  EVENT_FOLDER_SYNC_STARTED,
  EVENT_FOLDER_SYNC_FINISHED,
  EVENT_FOLDER_SYNC_PROGRESS,
  EVENT_SELECTION_CHANGED,
} from "../constants.js";

function showToast(message, type = "info") {
  const container = document.getElementById("toast-container");
  if (!container) return;
  const toast = document.createElement("div");
  toast.className = `toast ${type}`;
  toast.innerText = message;
  container.appendChild(toast);
  setTimeout(() => toast.remove(), 4000);
}

export function renderSummaryPane(container) {
  // Set up container layout slots once
  container.innerHTML = `
    <div id="summary-selection-slot"></div>
    <div id="summary-banner-slot"></div>
    <div id="summary-filter-slot"></div>
    <div class="summary-table-container"></div>
  `;

  const selectionSlot = container.querySelector("#summary-selection-slot");
  const bannerSlot = container.querySelector("#summary-banner-slot");
  const filterSlot = container.querySelector("#summary-filter-slot");
  const tableContainer = container.querySelector(".summary-table-container");

  function renderSelectionBar() {
    const { isMultiSelectMode, selectedMessageIds, messages } = store.getState();
    if (!isMultiSelectMode) {
      selectionSlot.innerHTML = "";
      return;
    }

    const selectedCount = selectedMessageIds ? selectedMessageIds.size : 0;
    const visibleCount = Array.isArray(messages) ? messages.length : 0;
    const allVisibleSelected =
      visibleCount > 0 &&
      messages.every((m) => m && m.id != null && selectedMessageIds.has(Number(m.id)));

    selectionSlot.innerHTML = `
      <div class="summary-selection-bar" id="summary-selection-bar">
        <div class="selection-bar-left">
          <label class="selection-all-label" title="Select or deselect all visible messages">
            <input type="checkbox" class="summary-select-all-checkbox" id="chk-select-all-messages" ${allVisibleSelected ? "checked" : ""} />
            <span>Select all</span>
          </label>
          <span class="selection-count-badge" id="selection-count-badge">${selectedCount} selected</span>
        </div>
        <div class="selection-bar-actions">
          <div class="bulk-tag-menu-wrapper" style="position: relative; display: inline-block;">
            <button type="button" class="filter-action-btn bulk-action-btn" id="btn-bulk-tag" ${selectedCount === 0 ? "disabled" : ""} title="Set or clear color tag for selected messages">
              🏷️ Tag them ▾
            </button>
            <div class="header-tag-dropdown bulk-tag-dropdown" id="bulk-tag-dropdown" style="display: none;"></div>
          </div>
          <button type="button" class="filter-action-btn text-danger bulk-action-btn" id="btn-bulk-trash" ${selectedCount === 0 ? "disabled" : ""} title="Move selected messages to Trash / Delete them">
            🗑️ Trash them
          </button>
          <button type="button" class="filter-action-btn bulk-cancel-btn" id="btn-bulk-cancel" title="Exit selection mode (Esc)">
            ✕ Cancel
          </button>
        </div>
      </div>
    `;

    const chkAll = selectionSlot.querySelector("#chk-select-all-messages");
    if (chkAll) {
      chkAll.addEventListener("change", (e) => {
        e.stopPropagation();
        if (chkAll.checked) {
          store.selectAllMessages(true);
        } else {
          store.selectAllMessages(false);
        }
      });
    }

    const cancelBtn = selectionSlot.querySelector("#btn-bulk-cancel");
    if (cancelBtn) {
      cancelBtn.addEventListener("click", (e) => {
        e.stopPropagation();
        store.clearMultiSelection();
      });
    }

    const trashBtn = selectionSlot.querySelector("#btn-bulk-trash");
    if (trashBtn) {
      trashBtn.addEventListener("click", async (e) => {
        e.stopPropagation();
        const ids = Array.from(store.getState().selectedMessageIds || []);
        if (ids.length === 0) return;
        const label = ids.length === 1 ? "1 selected message" : `${ids.length} selected messages`;
        if (typeof confirm === "function" && !confirm(`Are you sure you want to delete ${label}?`)) {
          return;
        }
        trashBtn.disabled = true;
        try {
          await deleteMessagesBatch(ids);
          store.removeMessages(ids);
          store.clearMultiSelection();
          try {
            const folders = await fetchFolders();
            store.setFolders(folders);
          } catch (folderErr) {
            console.warn("Could not refresh folders after bulk delete:", folderErr);
          }
          showToast(`Deleted ${label}.`, "info");
        } catch (err) {
          showToast(`Failed to delete messages: ${err.message}`, "error");
          trashBtn.disabled = false;
        }
      });
    }

    const tagBtn = selectionSlot.querySelector("#btn-bulk-tag");
    const tagDropdown = selectionSlot.querySelector("#bulk-tag-dropdown");
    if (tagBtn && tagDropdown) {
      tagBtn.addEventListener("click", async (e) => {
        e.stopPropagation();
        const isOpen = tagDropdown.style.display !== "none";
        if (isOpen) {
          tagDropdown.style.display = "none";
          return;
        }
        tagDropdown.style.display = "block";
        tagDropdown.innerHTML = `<div style="padding: 6px 10px; color: var(--text-muted); font-size: 11px;">Loading tags...</div>`;
        try {
          const availableTags = await getTags();
          let itemsHtml = `
            <button type="button" class="header-tag-item bulk-tag-item" data-tag-key="">
              <span class="tag-color-dot" style="background-color: transparent; border: 1px dashed #64748b;"></span>
              <span>None (Clear)</span>
              <span style="margin-left: auto; color: var(--text-muted); font-size: 10px;">0</span>
            </button>
            <div style="height: 1px; background-color: var(--border-color); margin: 3px 0;"></div>
          `;
          let idx = 1;
          for (const [key, val] of Object.entries(availableTags)) {
            const isCl = key.startsWith("$cl_");
            const num = isCl ? parseInt(key.slice(4), 10) : (key.startsWith("$label") ? parseInt(key.slice(6), 10) : idx++);
            const shortcutHint = (num >= 1 && num <= 9) ? `${num}` : "";
            itemsHtml += `
              <button type="button" class="header-tag-item bulk-tag-item" data-tag-key="${escapeHtml(key)}">
                <span class="tag-color-dot" style="background-color: ${escapeHtml(val.color || "")};"></span>
                <span>${escapeHtml(val.tag || key)}</span>
                ${shortcutHint ? `<span style="margin-left: auto; color: var(--text-muted); font-size: 10px;">${shortcutHint}</span>` : ""}
              </button>
            `;
          }
          tagDropdown.innerHTML = itemsHtml;
          tagDropdown.querySelectorAll(".bulk-tag-item").forEach((itemBtn) => {
            itemBtn.addEventListener("click", async (ev) => {
              ev.stopPropagation();
              tagDropdown.style.display = "none";
              const selectedKey = itemBtn.getAttribute("data-tag-key") || null;
              const ids = Array.from(store.getState().selectedMessageIds || []);
              if (ids.length === 0) return;
              try {
                const res = await setMessagesTagBatch(ids, selectedKey);
                const tagDef = selectedKey && availableTags[selectedKey] ? availableTags[selectedKey] : null;
                const tagKey = res?.tag_key !== undefined ? res.tag_key : (selectedKey || "");
                const tagName = res?.tag_name !== undefined ? res.tag_name : (tagDef ? tagDef.tag : "");
                const tagColor = res?.tag_color !== undefined ? res.tag_color : (tagDef ? tagDef.color : "");
                store.updateMessagesTag(ids, tagKey, tagName, tagColor);
                showToast(
                  selectedKey
                    ? `Tagged ${ids.length} message(s) as ${tagName || selectedKey}.`
                    : `Cleared tag on ${ids.length} message(s).`,
                  "info"
                );
              } catch (err) {
                showToast(`Failed to tag messages: ${err.message}`, "error");
              }
            });
          });
        } catch (err) {
          tagDropdown.innerHTML = `<div style="padding: 6px 10px; color: var(--accent-red); font-size: 11px;">Error loading tags</div>`;
        }
      });
    }
  }

  function renderSyncBanner() {
    const { selectedFolderId } = store.getState();
    const isSyncing = store.isFolderSyncing(selectedFolderId);
    const progress = store.getFolderSyncProgress(selectedFolderId);

    if (!isSyncing) {
      bannerSlot.innerHTML = "";
      return;
    }

    let infoText = "Syncing email summaries...";
    let pctWidth = 0;
    if (progress && progress.total > 0) {
      const pct = Math.round((progress.synced / progress.total) * 100);
      pctWidth = Math.min(100, Math.max(0, pct));
      infoText = `Syncing email summaries: ${progress.synced} of ${progress.total} messages synced (${pctWidth}%)`;
    } else if (progress && progress.synced > 0) {
      infoText = `Syncing email summaries: ${progress.synced} messages synced...`;
    }

    bannerSlot.innerHTML = `
      <div class="summary-sync-banner">
        <div class="sync-banner-content">
          <div class="sync-banner-text">
            <span class="spinner" style="font-size: 11px; margin-right: 6px;">🔄</span>
            <span>${escapeHtml(infoText)}</span>
          </div>
          <button type="button" class="sync-cancel-btn" id="btn-cancel-sync" title="Cancel summary sync">
            ✕ Cancel
          </button>
        </div>
        ${progress && progress.total > 0 ? `
          <div class="sync-progress-bar-bg">
            <div class="sync-progress-bar-fill" style="width: ${pctWidth}%;"></div>
          </div>
        ` : ""}
      </div>
    `;

    const btnCancel = bannerSlot.querySelector("#btn-cancel-sync");
    if (btnCancel && selectedFolderId) {
      btnCancel.addEventListener("click", async (e) => {
        e.stopPropagation();
        btnCancel.disabled = true;
        btnCancel.innerText = "Cancelling...";
        try {
          await cancelFolderSync(selectedFolderId);
          showToast("Cancelling summary download...", "info");
        } catch (err) {
          showToast(err.detail || err.message || "Failed to cancel summary sync", "error");
        }
      });
    }
  }

  function renderFilterBanner() {
    const { filterQuery, rawMessages, messages } = store.getState();
    if (!filterQuery) {
      filterSlot.innerHTML = "";
      return;
    }

    filterSlot.innerHTML = `
      <div class="summary-filter-banner">
        <div class="filter-banner-content">
          <span class="filter-icon">🔍</span>
          <span class="filter-text">Filter: <strong>${escapeHtml(filterQuery)}</strong> (${messages.length} of ${rawMessages.length} messages)</span>
        </div>
        <div class="filter-banner-actions">
          <button type="button" class="filter-action-btn" id="btn-edit-filter" title="Edit filter">✏️ Edit</button>
          <button type="button" class="filter-action-btn text-danger" id="btn-clear-filter-banner" title="Clear filter">✕ Clear</button>
        </div>
      </div>
    `;

    const editBtn = filterSlot.querySelector("#btn-edit-filter");
    if (editBtn) {
      editBtn.addEventListener("click", () => openFilterDialog());
    }

    const clearBtn = filterSlot.querySelector("#btn-clear-filter-banner");
    if (clearBtn) {
      clearBtn.addEventListener("click", () => store.clearFilter());
    }
  }

  function renderTable() {
    const {
      selectedFolderId,
      messages,
      rawMessages,
      selectedMessageId,
      isMultiSelectMode,
      selectedMessageIds,
      sortKey,
      sortOrder,
      threaded,
      filterQuery,
    } = store.getState();

    const isDraft = isDraftFolder(selectedFolderId);
    const hasFilter = Boolean(filterQuery && filterQuery.trim());
    const firstColWidth = isMultiSelectMode
      ? (isDraft ? "84px" : "64px")
      : (isDraft ? "68px" : "44px");

    function sortIndicator(col) {
      if (sortKey !== col) return "";
      return `<span class="sort-indicator">${sortOrder === "desc" ? "▼" : "▲"}</span>`;
    }

    let html = `
      <table class="summary-table ${isMultiSelectMode ? "multi-select-active" : ""}">
        <thead>
          <tr>
            <th class="summary-menu-th" style="width: ${firstColWidth}; text-align: center;">
              <div class="summary-menu-wrapper" style="display: flex; align-items: center; justify-content: center; gap: 4px;">
                <button type="button" class="summary-hamburger-btn ${hasFilter ? "has-filter" : ""}" id="btn-summary-hamburger" title="Message list options & filter">
                  ☰${hasFilter ? `<span class="filter-badge-dot">●</span>` : ""}
                </button>
                ${isDraft ? `
                  <button type="button" class="summary-new-draft-btn" id="btn-summary-header-new-msg" title="Edit new message (c)" style="background: none; border: none; cursor: pointer; padding: 2px 4px; font-size: 13px; border-radius: 4px; color: var(--text-color);">
                    ✏️
                  </button>
                ` : ""}
                <div class="summary-dropdown-menu" id="summary-dropdown-menu" style="display: none;">
                  ${isDraft ? `
                    <div class="summary-dropdown-section">Drafts</div>
                    <button type="button" class="summary-dropdown-item" id="menu-new-message">
                      <span class="dropdown-icon">✏️</span>
                      <span>Edit new message</span>
                    </button>
                    <div class="summary-dropdown-divider"></div>
                  ` : ""}
                  <div class="summary-dropdown-section">Selection</div>
                  <button type="button" class="summary-dropdown-item" id="menu-toggle-select-mode">
                    <span class="dropdown-icon">☑️</span>
                    <span>${isMultiSelectMode ? "Exit selection mode" : "Select messages (Ctrl+Click)"}</span>
                  </button>
                  <div class="summary-dropdown-divider"></div>
                  <div class="summary-dropdown-section">Filter</div>
                  <button type="button" class="summary-dropdown-item" id="menu-open-filter">
                    <span class="dropdown-icon">🔍</span>
                    <span>Filter messages...</span>
                  </button>
                  ${hasFilter ? `
                    <button type="button" class="summary-dropdown-item text-danger" id="menu-clear-filter">
                      <span class="dropdown-icon">✕</span>
                      <span>Clear filter</span>
                    </button>
                  ` : ""}
                  <div class="summary-dropdown-divider"></div>
                  <div class="summary-dropdown-section">View Mode</div>
                  <button type="button" class="summary-dropdown-item ${threaded ? "selected" : ""}" id="menu-view-threaded">
                    <span class="dropdown-check">${threaded ? "✓" : ""}</span>
                    <span>Thread view</span>
                  </button>
                  <button type="button" class="summary-dropdown-item ${!threaded ? "selected" : ""}" id="menu-view-flat">
                    <span class="dropdown-check">${!threaded ? "✓" : ""}</span>
                    <span>Flat view</span>
                  </button>
                  ${threaded ? `
                    <div class="summary-dropdown-divider"></div>
                    <div class="summary-dropdown-section">Threads</div>
                    <button type="button" class="summary-dropdown-item" id="menu-expand-all">
                      <span class="dropdown-icon">⊞</span>
                      <span>Expand all</span>
                    </button>
                    <button type="button" class="summary-dropdown-item" id="menu-collapse-all">
                      <span class="dropdown-icon">⊟</span>
                      <span>Collapse all</span>
                    </button>
                  ` : ""}
                </div>
              </div>
            </th>
            <th class="sortable" data-sort="subject" style="width: 48%;">
              Subject ${sortIndicator("subject")}
            </th>
            <th class="sortable" data-sort="from" style="width: 22%;">
              From ${sortIndicator("from")}
            </th>
            <th class="sortable" data-sort="tag" style="width: 12%;">
              Tag ${sortIndicator("tag")}
            </th>
            <th class="sortable" data-sort="date" style="width: 18%;">
              Date ${sortIndicator("date")}
            </th>
          </tr>
        </thead>
        <tbody class="summary-tbody">
    `;

    if (messages.length === 0) {
      const emptyMsg = hasFilter
        ? "No messages match the current filter."
        : (isDraft ? "No drafts in this folder." : "No messages in this folder.");
      html += `
        <tr>
          <td colspan="5" style="text-align: center; color: var(--text-muted); padding: 24px;">
            ${escapeHtml(emptyMsg)}
            ${isDraft ? `
              <div style="margin-top: 12px;">
                <button type="button" class="action-btn primary" id="btn-summary-new-message" style="padding: 6px 14px; font-size: 12px;">
                  ✏️ Edit New Message
                </button>
              </div>
            ` : ""}
            ${hasFilter ? `<div style="margin-top: 8px;"><button type="button" class="filter-action-btn" id="btn-empty-clear-filter">Clear filter</button></div>` : ""}
          </td>
        </tr>
      `;
    } else {
      messages.forEach((m, idx) => {
        const isActive = m.id === selectedMessageId ? "active" : "";
        const isUnread = !m.is_read ? "unread" : "";
        const unreadDot = !m.is_read ? `<span class="unread-dot">●</span>` : "";
        const repliedMark = m.is_replied ? `<span class="replied-dot" title="Replied">↩</span>` : "";
        const isAiReview = Boolean(m.is_ai_review || m.folder_name === "AI-reviews" || m.from_name === "AI review");
        const treeGuide = m.thread_prefix
          ? `<span class="thread-prefix">${escapeHtml(m.thread_prefix)}</span>`
          : (!isAiReview && m.in_reply_to && !threaded ? `<span style="color: #64748b; margin-right: 4px;">↳</span>` : "");

        const tagColor = escapeHtml(m.tag_color || "");
        const tagName = m.tag_name || m.tag_key || "";
        const tagBadgeHtml = tagName
          ? `<span class="summary-tag-badge" style="color: ${tagColor || "inherit"}; border-color: ${tagColor || "currentColor"};">${escapeHtml(tagName)}</span>`
          : "";

        const rowColorStyle = tagColor ? `color: ${tagColor};` : "";
        const dateColorStyle = tagColor ? `color: ${tagColor}; opacity: 0.85;` : `color: var(--text-muted);`;

        let threadToggleHtml = "";
        const isTopLevel = m.thread_depth === 0;
        if (threaded && isTopLevel && m.has_children) {
          if (m.is_collapsed) {
            const unreadSuffix = m.unread_child_count > 0 ? ` (${m.unread_child_count} unread)` : "";
            const unreadBadgeClass = m.unread_child_count > 0 ? "has-unread" : "";
            threadToggleHtml = `
              <button type="button" class="thread-collapse-btn collapsed ${unreadBadgeClass}" data-thread-toggle="${m.id}" title="Expand thread (${m.child_count} replies${unreadSuffix})">
                ▶
              </button>
              <span class="thread-collapse-count ${unreadBadgeClass}" data-thread-toggle="${m.id}" title="${m.child_count} replies in thread${unreadSuffix}">(+${m.child_count})</span>
            `;
          } else {
            threadToggleHtml = `
              <button type="button" class="thread-collapse-btn expanded" data-thread-toggle="${m.id}" title="Collapse thread (${m.child_count} replies)">
                ▼
              </button>
            `;
          }
        }

        const repliedTitle = m.is_replied ? " (Replied)" : "";
        let unreadTitle = (m.is_read ? "Click to mark as unread" : "Click to mark as read") + repliedTitle;
        if (m.is_collapsed && m.has_unread_children && m.orig_is_read) {
          unreadTitle = `Thread has unread replies. Click to mark thread as read${repliedTitle}`;
        }

        const isSelectedMulti = Boolean(isMultiSelectMode && selectedMessageIds && selectedMessageIds.has(Number(m.id)));
        const multiClass = isSelectedMulti ? "selected-multi" : "";
        const checkboxHtml = isMultiSelectMode
          ? `<input type="checkbox" class="summary-select-checkbox" data-select-id="${m.id}" ${isSelectedMulti ? "checked" : ""} title="Select message" />`
          : "";

        html += `
          <tr class="summary-row ${isActive} ${isUnread} ${multiClass}" data-index="${idx}" data-id="${m.id}" data-has-tag="${Boolean(tagColor)}" style="${rowColorStyle}">
            <td class="summary-unread-cell" data-unread-toggle="${m.id}" title="${unreadTitle}">${checkboxHtml}<span class="summary-unread-indicators">${unreadDot}${repliedMark}</span></td>
            <td class="summary-subject-cell">${treeGuide}${threadToggleHtml}${escapeHtml(m.subject)}</td>
            <td class="summary-from-cell">${escapeHtml(m.from_name || m.from_addr)}</td>
            <td class="summary-tag-cell">${tagBadgeHtml}</td>
            <td class="summary-date-cell" style="${dateColorStyle}">${formatDate(m.date)}</td>
          </tr>
        `;
      });
    }

    html += `
        </tbody>
      </table>
    `;

    tableContainer.innerHTML = html;

    // Attach click listeners for sorting headers
    tableContainer.querySelectorAll(".summary-table th.sortable").forEach((th) => {
      th.addEventListener("click", () => {
        const column = th.getAttribute("data-sort");
        store.toggleSort(column);
      });
    });

    // Attach hamburger dropdown listeners
    const btnHamburger = tableContainer.querySelector("#btn-summary-hamburger");
    const dropdownMenu = tableContainer.querySelector("#summary-dropdown-menu");

    if (btnHamburger && dropdownMenu) {
      const positionMenu = () => {
        dropdownMenu.style.position = "fixed";
        dropdownMenu.style.zIndex = "10000";
        dropdownMenu.style.display = "block";
        const rect = btnHamburger.getBoundingClientRect();
        const menuWidth = dropdownMenu.offsetWidth || 180;
        const menuHeight = dropdownMenu.offsetHeight || 200;
        let top = rect.bottom + 4;
        let left = rect.left;

        if (left + menuWidth > window.innerWidth - 8) {
          left = window.innerWidth - menuWidth - 8;
        }
        if (left < 8) {
          left = 8;
        }
        if (top + menuHeight > window.innerHeight - 8) {
          top = Math.max(8, rect.top - menuHeight - 4);
        }

        dropdownMenu.style.top = `${top}px`;
        dropdownMenu.style.left = `${left}px`;
      };

      btnHamburger.addEventListener("click", (e) => {
        e.stopPropagation();
        const isOpen = dropdownMenu.style.display !== "none";
        if (isOpen) {
          dropdownMenu.style.display = "none";
        } else {
          positionMenu();
        }
      });

      tableContainer.querySelector("#menu-view-threaded")?.addEventListener("click", (e) => {
        e.stopPropagation();
        dropdownMenu.style.display = "none";
        store.setThreaded(true);
      });

      tableContainer.querySelector("#menu-view-flat")?.addEventListener("click", (e) => {
        e.stopPropagation();
        dropdownMenu.style.display = "none";
        store.setThreaded(false);
      });

      tableContainer.querySelector("#menu-expand-all")?.addEventListener("click", (e) => {
        e.stopPropagation();
        dropdownMenu.style.display = "none";
        store.expandAllThreads();
      });

      tableContainer.querySelector("#menu-collapse-all")?.addEventListener("click", (e) => {
        e.stopPropagation();
        dropdownMenu.style.display = "none";
        store.collapseAllThreads();
      });

      tableContainer.querySelector("#menu-open-filter")?.addEventListener("click", (e) => {
        e.stopPropagation();
        dropdownMenu.style.display = "none";
        openFilterDialog();
      });

      tableContainer.querySelector("#menu-new-message")?.addEventListener("click", (e) => {
        e.stopPropagation();
        dropdownMenu.style.display = "none";
        triggerNewMessage();
      });

      tableContainer.querySelector("#btn-summary-header-new-msg")?.addEventListener("click", (e) => {
        e.stopPropagation();
        triggerNewMessage();
      });

      tableContainer.querySelector("#menu-clear-filter")?.addEventListener("click", (e) => {
        e.stopPropagation();
        dropdownMenu.style.display = "none";
        store.clearFilter();
      });

      tableContainer.querySelector("#menu-toggle-select-mode")?.addEventListener("click", (e) => {
        e.stopPropagation();
        dropdownMenu.style.display = "none";
        const curr = store.getState().isMultiSelectMode;
        if (curr) {
          store.clearMultiSelection();
        } else {
          store.setMultiSelectMode(true);
        }
      });
    }

    // Attach new message button inside empty list
    tableContainer.querySelector("#btn-summary-new-message")?.addEventListener("click", () => {
      triggerNewMessage();
    });

    // Attach clear button inside empty list
    tableContainer.querySelector("#btn-empty-clear-filter")?.addEventListener("click", () => {
      store.clearFilter();
    });

    // Attach delegated click listener to tbody (one listener instead of thousands)
    const tbody = tableContainer.querySelector(".summary-tbody");
    if (tbody) {
      tbody.addEventListener("click", (e) => {
        const selectCheckbox = e.target.closest(".summary-select-checkbox");
        if (selectCheckbox) {
          e.stopPropagation();
          const row = selectCheckbox.closest(".summary-row");
          if (!row) return;
          const msgId = parseInt(row.getAttribute("data-id"), 10);
          const idx = parseInt(row.getAttribute("data-index"), 10);
          store.toggleMultiSelectMessage(msgId, idx, { range: Boolean(e.shiftKey) });
          return;
        }

        const toggleBtn = e.target.closest("[data-thread-toggle]");
        if (toggleBtn) {
          e.stopPropagation();
          const tid = parseInt(toggleBtn.getAttribute("data-thread-toggle"), 10);
          store.toggleThreadCollapse(tid);
          return;
        }

        const row = e.target.closest(".summary-row");
        if (!row) return;
        const msgId = parseInt(row.getAttribute("data-id"), 10);
        const idx = parseInt(row.getAttribute("data-index"), 10);

        if (e.ctrlKey || e.metaKey || (store.getState().isMultiSelectMode && e.shiftKey)) {
          e.preventDefault();
          e.stopPropagation();
          store.toggleMultiSelectMessage(msgId, idx, { range: Boolean(e.shiftKey) });
          return;
        }

        const unreadCell = e.target.closest(".summary-unread-cell, [data-unread-toggle]");
        if (unreadCell) {
          e.stopPropagation();
          const computedMsg = store.getState().messages.find((m) => m.id === msgId);
          const rawMsg = store.getState().rawMessages.find((m) => m.id === msgId);
          if (!rawMsg) return;

          if (computedMsg && computedMsg.is_collapsed && computedMsg.has_children) {
            if (!computedMsg.is_read) {
              store.markThreadRead(msgId, true);
            } else {
              store.updateMessageRead(msgId, false);
              markMessageRead(msgId, false).catch((err) => {
                console.error("Failed to update message read state:", err);
              });
            }
            return;
          }

          const nextIsRead = !rawMsg.is_read;
          store.updateMessageRead(msgId, nextIsRead);
          markMessageRead(msgId, nextIsRead).catch((err) => {
            console.error("Failed to update message read state:", err);
          });
          return;
        }

        store.selectMessageIndex(idx);
      });
    }

    // Scroll active row into view
    const activeEl = tableContainer.querySelector(".summary-row.active");
    if (activeEl) {
      activeEl.scrollIntoView({ block: "nearest" });
    }
  }

  // Close hamburger or bulk tag dropdown on outside click, window resize, or tableContainer scroll
  document.addEventListener("click", (e) => {
    const dropdown = tableContainer.querySelector("#summary-dropdown-menu");
    const btn = tableContainer.querySelector("#btn-summary-hamburger");
    if (dropdown && dropdown.style.display !== "none" && !dropdown.contains(e.target) && !btn?.contains(e.target)) {
      dropdown.style.display = "none";
    }
    const bulkTagDropdown = selectionSlot.querySelector("#bulk-tag-dropdown");
    const bulkTagBtn = selectionSlot.querySelector("#btn-bulk-tag");
    if (bulkTagDropdown && bulkTagDropdown.style.display !== "none" && !bulkTagDropdown.contains(e.target) && !bulkTagBtn?.contains(e.target)) {
      bulkTagDropdown.style.display = "none";
    }
  });

  window.addEventListener("resize", () => {
    const dropdown = tableContainer.querySelector("#summary-dropdown-menu");
    if (dropdown && dropdown.style.display !== "none") {
      dropdown.style.display = "none";
    }
    const bulkTagDropdown = selectionSlot.querySelector("#bulk-tag-dropdown");
    if (bulkTagDropdown && bulkTagDropdown.style.display !== "none") {
      bulkTagDropdown.style.display = "none";
    }
  });

  tableContainer.addEventListener("scroll", () => {
    const dropdown = tableContainer.querySelector("#summary-dropdown-menu");
    if (dropdown && dropdown.style.display !== "none") {
      dropdown.style.display = "none";
    }
  }, { passive: true });

  function updateSelection(selectedId, autoScroll = true) {
    if (selectedId == null) return;
    const tbody = tableContainer.querySelector(".summary-tbody");
    if (!tbody) return;

    // Deselect previous row(s)
    const prevActives = tbody.querySelectorAll(".summary-row.active");
    prevActives.forEach((r) => {
      if (r.getAttribute("data-id") !== String(selectedId)) {
        r.classList.remove("active");
      }
    });

    // Select target row
    const nextRow = tbody.querySelector(`.summary-row[data-id="${selectedId}"]`);
    if (nextRow) {
      nextRow.classList.add("active");
      if (autoScroll) {
        nextRow.scrollIntoView({ block: "nearest" });
      }
    }
  }

  function updateRowReadState(messageId, isRead) {
    if (messageId == null) return;
    const tbody = tableContainer.querySelector(".summary-tbody");
    if (!tbody) return;
    const row = tbody.querySelector(`.summary-row[data-id="${messageId}"]`);
    if (!row) return;

    const rawMsg = store.getState().rawMessages.find((item) => item.id === messageId);
    const computedMsg = store.getState().messages.find((item) => item.id === messageId);
    const effectiveRead = computedMsg ? Boolean(computedMsg.is_read) : Boolean(isRead);
    const repliedHtml = (rawMsg && rawMsg.is_replied) ? `<span class="replied-dot" title="Replied">↩</span>` : "";
    const repliedTitle = (rawMsg && rawMsg.is_replied) ? " (Replied)" : "";
    const dotCell = row.cells ? row.cells[0] : row.querySelector(".summary-unread-cell");
    const indicatorsEl = dotCell ? (dotCell.querySelector(".summary-unread-indicators") || dotCell) : null;

    if (effectiveRead) {
      row.classList.remove("unread");
      if (indicatorsEl) {
        indicatorsEl.innerHTML = repliedHtml;
      }
      if (dotCell) {
        dotCell.setAttribute("title", `Click to mark as unread${repliedTitle}`);
      }
    } else {
      row.classList.add("unread");
      if (indicatorsEl) {
        indicatorsEl.innerHTML = `<span class="unread-dot">●</span>${repliedHtml}`;
      }
      if (dotCell) {
        dotCell.setAttribute("title", `Click to mark as read${repliedTitle}`);
      }
    }
  }

  function updateRowTagState(messageId, tagKey, tagName, tagColor) {
    if (messageId == null) return;
    const tbody = tableContainer.querySelector(".summary-tbody");
    if (!tbody) return;
    const row = tbody.querySelector(`.summary-row[data-id="${messageId}"]`);
    if (!row) return;

    const tagCell = row.cells ? row.cells[3] : row.querySelector(".summary-tag-cell");
    const dateCell = row.cells ? row.cells[4] : row.querySelector(".summary-date-cell");
    const safeTagColor = tagColor ? escapeHtml(tagColor) : "";
    const safeTagName = tagName || tagKey || "";

    if (tagCell) {
      tagCell.innerHTML = safeTagName
        ? `<span class="summary-tag-badge" style="color: ${safeTagColor || "inherit"}; border-color: ${safeTagColor || "currentColor"};">${escapeHtml(safeTagName)}</span>`
        : "";
    }
    if (safeTagColor) {
      row.style.color = safeTagColor;
      row.setAttribute("data-has-tag", "true");
      if (dateCell) {
        dateCell.style.color = safeTagColor;
        dateCell.style.opacity = "0.85";
      }
    } else {
      row.style.color = "";
      row.removeAttribute("data-has-tag");
      if (dateCell) {
        dateCell.style.color = "var(--text-muted)";
        dateCell.style.opacity = "";
      }
    }
  }

  store.subscribe((event, payload) => {
    if (
      event === EVENT_MESSAGES_UPDATED ||
      event === EVENT_FOLDER_SELECTED ||
      event === EVENT_FILTER_UPDATED
    ) {
      renderSelectionBar();
      renderTable();
      renderSyncBanner();
      renderFilterBanner();
    } else if (event === EVENT_SELECTION_CHANGED) {
      renderSelectionBar();
      renderTable();
    } else if (event === EVENT_MESSAGE_SELECTED) {
      updateSelection(payload?.id, true);
    } else if (event === EVENT_MESSAGE_READ_UPDATED) {
      updateRowReadState(payload?.id, Boolean(payload?.is_read));
    } else if (event === EVENT_MESSAGE_REPLIED_UPDATED) {
      const rawMsg = store.getState().rawMessages.find((item) => item.id === payload?.id);
      const isRead = rawMsg ? Boolean(rawMsg.is_read) : true;
      updateRowReadState(payload?.id, isRead);
    } else if (event === EVENT_MESSAGE_TAG_UPDATED) {
      updateRowTagState(payload?.messageId, payload?.tagKey, payload?.tagName, payload?.tagColor);
    } else if (
      event === EVENT_FOLDER_SYNC_STARTED ||
      event === EVENT_FOLDER_SYNC_FINISHED ||
      event === EVENT_FOLDER_SYNC_PROGRESS
    ) {
      renderSyncBanner();
    }
  });

  renderSelectionBar();
  renderSyncBanner();
  renderFilterBanner();
  renderTable();
}

function formatDate(dateStr) {
  if (!dateStr) return "";
  try {
    const d = new Date(dateStr);
    if (isNaN(d.getTime())) return dateStr;
    return d.toLocaleDateString(undefined, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  } catch {
    return dateStr;
  }
}

function escapeHtml(str) {
  if (!str) return "";
  return str.replace(/[&<>"']/g, (m) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[m]));
}
