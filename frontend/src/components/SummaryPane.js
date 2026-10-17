/**
 * SummaryPane: Top-right pane displaying threaded/flat summary table with keyboard selection,
 * thread expand/collapse controls, hamburger menu for view switching, and message filtering.
 */

import { store } from "../store.js";
import { cancelFolderSync, markMessageRead } from "../api.js";
import { openFilterDialog } from "./FilterDialog.js";
import { isDraftFolder, triggerNewMessage } from "./ViewerPane.js";
import {
  EVENT_MESSAGES_UPDATED,
  EVENT_FOLDER_SELECTED,
  EVENT_FILTER_UPDATED,
  EVENT_MESSAGE_SELECTED,
  EVENT_MESSAGE_READ_UPDATED,
  EVENT_MESSAGE_REPLIED_UPDATED,
  EVENT_FOLDER_SYNC_STARTED,
  EVENT_FOLDER_SYNC_FINISHED,
  EVENT_FOLDER_SYNC_PROGRESS,
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
    <div id="summary-banner-slot"></div>
    <div id="summary-filter-slot"></div>
    <div class="summary-table-container"></div>
  `;

  const bannerSlot = container.querySelector("#summary-banner-slot");
  const filterSlot = container.querySelector("#summary-filter-slot");
  const tableContainer = container.querySelector(".summary-table-container");

  function renderSyncBanner() {
    const { selectedFolderId } = store.getState();
    const isSyncing = store.isFolderSyncing(selectedFolderId);
    const progress = store.getFolderSyncProgress(selectedFolderId);

    if (!isSyncing) {
      bannerSlot.innerHTML = "";
      return;
    }

    let infoText = "Downloading full email summaries...";
    let pctWidth = 0;
    if (progress && progress.total > 0) {
      const pct = Math.round((progress.synced / progress.total) * 100);
      pctWidth = Math.min(100, Math.max(0, pct));
      infoText = `Downloading full email summaries: ${progress.synced} of ${progress.total} messages synced (${pctWidth}%)`;
    } else if (progress && progress.synced > 0) {
      infoText = `Downloading full email summaries: ${progress.synced} messages synced...`;
    }

    bannerSlot.innerHTML = `
      <div class="summary-sync-banner">
        <div class="sync-banner-content">
          <div class="sync-banner-text">
            <span class="spinner" style="font-size: 11px; margin-right: 6px;">🔄</span>
            <span>${escapeHtml(infoText)}</span>
          </div>
          <button type="button" class="sync-cancel-btn" id="btn-cancel-sync" title="Cancel summary download">
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
      sortKey,
      sortOrder,
      threaded,
      filterQuery,
    } = store.getState();

    const isDraft = isDraftFolder(selectedFolderId);
    const hasFilter = Boolean(filterQuery && filterQuery.trim());

    function sortIndicator(col) {
      if (sortKey !== col) return "";
      return `<span class="sort-indicator">${sortOrder === "desc" ? "▼" : "▲"}</span>`;
    }

    let html = `
      <table class="summary-table">
        <thead>
          <tr>
            <th class="summary-menu-th" style="width: ${isDraft ? "68px" : "44px"}; text-align: center;">
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

        const tagColor = m.tag_color || "";
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

        html += `
          <tr class="summary-row ${isActive} ${isUnread}" data-index="${idx}" data-id="${m.id}" data-has-tag="${Boolean(tagColor)}" style="${rowColorStyle}">
            <td class="summary-unread-cell" data-unread-toggle="${m.id}" title="${unreadTitle}">${unreadDot}${repliedMark}</td>
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
        const toggleBtn = e.target.closest("[data-thread-toggle]");
        if (toggleBtn) {
          e.stopPropagation();
          const tid = parseInt(toggleBtn.getAttribute("data-thread-toggle"), 10);
          store.toggleThreadCollapse(tid);
          return;
        }

        const unreadCell = e.target.closest(".summary-unread-cell, [data-unread-toggle]");
        if (unreadCell) {
          e.stopPropagation();
          const row = unreadCell.closest(".summary-row");
          if (!row) return;
          const msgId = parseInt(row.getAttribute("data-id"), 10);
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

        const row = e.target.closest(".summary-row");
        if (!row) return;
        const idx = parseInt(row.getAttribute("data-index"), 10);
        store.selectMessageIndex(idx);
      });
    }

    // Scroll active row into view
    const activeEl = tableContainer.querySelector(".summary-row.active");
    if (activeEl) {
      activeEl.scrollIntoView({ block: "nearest" });
    }
  }

  // Close hamburger dropdown on outside click, window resize, or tableContainer scroll
  document.addEventListener("click", (e) => {
    const dropdown = tableContainer.querySelector("#summary-dropdown-menu");
    const btn = tableContainer.querySelector("#btn-summary-hamburger");
    if (dropdown && dropdown.style.display !== "none" && !dropdown.contains(e.target) && !btn?.contains(e.target)) {
      dropdown.style.display = "none";
    }
  });

  window.addEventListener("resize", () => {
    const dropdown = tableContainer.querySelector("#summary-dropdown-menu");
    if (dropdown && dropdown.style.display !== "none") {
      dropdown.style.display = "none";
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

    if (effectiveRead) {
      row.classList.remove("unread");
      const dotCell = row.cells ? row.cells[0] : row.querySelector(".summary-unread-cell");
      if (dotCell) {
        dotCell.innerHTML = repliedHtml;
        dotCell.setAttribute("title", `Click to mark as unread${repliedTitle}`);
      }
    } else {
      row.classList.add("unread");
      const dotCell = row.cells ? row.cells[0] : row.querySelector(".summary-unread-cell");
      if (dotCell) {
        dotCell.innerHTML = `<span class="unread-dot">●</span>${repliedHtml}`;
        dotCell.setAttribute("title", `Click to mark as read${repliedTitle}`);
      }
    }
  }

  store.subscribe((event, payload) => {
    if (
      event === EVENT_MESSAGES_UPDATED ||
      event === EVENT_FOLDER_SELECTED ||
      event === EVENT_FILTER_UPDATED
    ) {
      renderTable();
      renderSyncBanner();
      renderFilterBanner();
    } else if (event === EVENT_MESSAGE_SELECTED) {
      updateSelection(payload?.id, true);
    } else if (event === EVENT_MESSAGE_READ_UPDATED) {
      updateRowReadState(payload?.id, Boolean(payload?.is_read));
    } else if (event === EVENT_MESSAGE_REPLIED_UPDATED) {
      const rawMsg = store.getState().rawMessages.find((item) => item.id === payload?.id);
      const isRead = rawMsg ? Boolean(rawMsg.is_read) : true;
      updateRowReadState(payload?.id, isRead);
    } else if (
      event === EVENT_FOLDER_SYNC_STARTED ||
      event === EVENT_FOLDER_SYNC_FINISHED ||
      event === EVENT_FOLDER_SYNC_PROGRESS
    ) {
      renderSyncBanner();
    }
  });

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
