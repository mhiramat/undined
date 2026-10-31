/**
 * FolderPane: Left sidebar displaying synchronized Maildir folders, unread counts,
 * and a Mailboxes hamburger menu for account-level actions.
 */

import { store } from "../store.js";
import { syncFullFolderSummaries, syncTodoFromPatchwork, fetchMessages, fetchFolders, fetchSyncStatus, cancelFolderSync, fetchAiReviewStatus } from "../api.js";
import {
  EVENT_FOLDERS_UPDATED,
  EVENT_FOLDER_SELECTED,
  EVENT_MESSAGE_READ_UPDATED,
  EVENT_FOLDER_SYNC_STARTED,
  EVENT_FOLDER_SYNC_FINISHED,
  EVENT_FOLDER_SYNC_PROGRESS,
  EVENT_AI_REVIEW_STATE_CHANGED,
  EVENT_MESSAGE_SELECTED,
  EVENT_MESSAGE_DETAIL_LOADED,
  FOLDER_AI_REVIEWS,
} from "../constants.js";

function showToast(message, type = "success") {
  const container = document.getElementById("toast-container");
  if (!container) return;
  const toast = document.createElement("div");
  toast.className = `toast ${type}`;
  toast.innerText = message;
  container.appendChild(toast);
  setTimeout(() => toast.remove(), 4000);
}

export function renderFolderPane(container) {
  let isMenuOpen = false;
  let isSyncingTodo = false;

  function update() {
    const { folders, selectedFolderId } = store.getState();
    const selectedFolder = folders.find((f) => f.id === selectedFolderId);
    const isImap = Boolean(selectedFolder && selectedFolder.is_imap);
    const isTodo = Boolean(selectedFolder && selectedFolder.name === "TODO");
    const isSyncing = selectedFolder ? store.isFolderSyncing(selectedFolder.id) : false;

    let dropdownHtml = "";
    if (isMenuOpen) {
      let itemsHtml = "";

      // Include option to download full email summaries only when selected mailbox is IMAP
      if (isImap) {
        if (isSyncing) {
          const progress = store.getFolderSyncProgress(selectedFolder.id);
          let syncBtnLabel = "Downloading summaries...";
          if (progress && progress.total > 0) {
            const pct = Math.round((progress.synced / progress.total) * 100);
            syncBtnLabel = `Downloading: ${progress.synced}/${progress.total} (${pct}%)`;
          } else if (progress && progress.synced > 0) {
            syncBtnLabel = `Downloaded ${progress.synced} summaries...`;
          }
          itemsHtml += `
            <button type="button" class="mailbox-dropdown-item disabled" id="btn-download-full-summaries" disabled>
              <span class="spinner" style="font-size: 13px;">🔄</span>
              <span>${escapeHtml(syncBtnLabel)}</span>
            </button>
            <button type="button" class="mailbox-dropdown-item" id="btn-cancel-sync-menu">
              <span style="font-size: 13px; color: var(--accent-red);">✕</span>
              <span style="color: var(--accent-red);">Cancel summary download</span>
            </button>
            <div class="mailbox-dropdown-divider"></div>
          `;
        } else {
          itemsHtml += `
            <button type="button" class="mailbox-dropdown-item" id="btn-download-full-summaries">
              <span style="font-size: 13px;">📥</span>
              <span>Download full email summaries</span>
            </button>
            <div class="mailbox-dropdown-divider"></div>
          `;
        }
      }

      // Include option to sync delegated patches from Patchwork when selected mailbox is TODO
      if (isTodo) {
        if (isSyncingTodo) {
          itemsHtml += `
            <button type="button" class="mailbox-dropdown-item disabled" id="btn-sync-todo-patchwork" disabled>
              <span class="spinner" style="font-size: 13px;">🔄</span>
              <span>Syncing TODO from patchwork...</span>
            </button>
            <div class="mailbox-dropdown-divider"></div>
          `;
        } else {
          itemsHtml += `
            <button type="button" class="mailbox-dropdown-item" id="btn-sync-todo-patchwork">
              <span style="font-size: 13px;">🧩</span>
              <span>Sync TODO from patchwork</span>
            </button>
            <div class="mailbox-dropdown-divider"></div>
          `;
        }
      }

      itemsHtml += `
        <button type="button" class="mailbox-dropdown-item" id="btn-refresh-mailboxes">
          <span style="font-size: 13px;">🔄</span>
          <span>Refresh mailboxes</span>
        </button>
      `;

      dropdownHtml = `
        <div class="mailbox-dropdown" id="mailbox-dropdown">
          ${itemsHtml}
        </div>
      `;
    }

    let html = `
      <div class="folder-header">
        <span>Mailboxes</span>
        <div class="folder-header-menu">
          <button type="button" class="mailbox-menu-btn ${isMenuOpen ? "active" : ""}" id="mailbox-menu-toggle" title="Mailbox options" aria-label="Mailbox options">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
              <line x1="3" y1="6" x2="21" y2="6"></line>
              <line x1="3" y1="12" x2="21" y2="12"></line>
              <line x1="3" y1="18" x2="21" y2="18"></line>
            </svg>
          </button>
          ${dropdownHtml}
        </div>
      </div>
      <ul class="folder-list">
    `;

    for (const f of folders) {
      const isActive = f.id === selectedFolderId ? "active" : "";
      const isFolderSyncing = store.isFolderSyncing(f.id);
      const progress = store.getFolderSyncProgress(f.id);
      const unreadBadge = f.unread_count > 0 ? `<span class="unread-badge">${f.unread_count}</span>` : "";
      const imapBadge = f.is_imap ? `<span style="font-size: 10px; color: var(--text-muted); margin-left: 4px;" title="IMAP Mailbox">☁</span>` : "";
      let syncingBadge = "";
      if (isFolderSyncing) {
        if (progress && progress.total > 0) {
          const pct = Math.round((progress.synced / progress.total) * 100);
          syncingBadge = `<span class="folder-sync-badge" title="Downloading summaries: ${progress.synced} of ${progress.total} (${pct}%)"><span class="spinner" style="font-size: 10px;">🔄</span> ${progress.synced}/${progress.total}</span>`;
        } else if (progress && progress.synced > 0) {
          syncingBadge = `<span class="folder-sync-badge" title="Downloaded ${progress.synced} summaries"><span class="spinner" style="font-size: 10px;">🔄</span> ${progress.synced}</span>`;
        } else {
          syncingBadge = `<span class="folder-syncing-icon spinner" title="Downloading summaries..." style="margin-left: 5px; font-size: 11px;">🔄</span>`;
        }
      } else if (f.name === "TODO" && isSyncingTodo) {
        syncingBadge = `<span class="folder-syncing-icon spinner" title="Syncing TODO from Patchwork..." style="margin-left: 5px; font-size: 11px;">🔄</span>`;
      } else if ((f.name === FOLDER_AI_REVIEWS || f.name === "AI-reviews") && store.isAiReviewActive()) {
        syncingBadge = `<span class="folder-syncing-icon spinner" title="AI Review in progress..." style="margin-left: 5px; font-size: 11px;">🔄</span>`;
      }
      html += `
        <li class="folder-item ${isActive}" data-id="${f.id}">
          <span class="folder-name">${escapeHtml(f.name)}${imapBadge}${syncingBadge}</span>
          ${unreadBadge}
        </li>
      `;
    }

    html += `</ul>`;
    container.innerHTML = html;

    // Attach hamburger toggle
    const toggleBtn = container.querySelector("#mailbox-menu-toggle");
    if (toggleBtn) {
      toggleBtn.addEventListener("click", (e) => {
        e.stopPropagation();
        isMenuOpen = !isMenuOpen;
        update();
      });
    }

    // Position fixed mailbox dropdown relative to toggle button, clamped inside viewport
    const dropdownEl = container.querySelector("#mailbox-dropdown");
    if (toggleBtn && dropdownEl) {
      const rect = toggleBtn.getBoundingClientRect();
      const menuWidth = dropdownEl.offsetWidth || 240;
      const menuHeight = dropdownEl.offsetHeight || 160;
      let top = rect.bottom + 6;
      let left = rect.left;

      if (left + menuWidth > window.innerWidth - 8) {
        left = window.innerWidth - menuWidth - 8;
      }
      if (left < 8) {
        left = 8;
      }
      if (top + menuHeight > window.innerHeight - 8) {
        top = Math.max(8, rect.top - menuHeight - 6);
      }

      dropdownEl.style.top = `${top}px`;
      dropdownEl.style.left = `${left}px`;
    }

    // Attach Download Full Summaries button (only rendered if isImap)
    const btnDownload = container.querySelector("#btn-download-full-summaries");
    if (btnDownload && selectedFolder) {
      btnDownload.addEventListener("click", async (e) => {
        e.stopPropagation();
        if (store.isFolderSyncing(selectedFolder.id)) {
          return;
        }
        isMenuOpen = false;
        const targetFolderId = selectedFolder.id;
        const targetFolderName = selectedFolder.name;
        store.startFolderSync(targetFolderId);
        update();
        showToast(`Downloading full email summaries for "${targetFolderName}"...`, "info");

        // Periodic progress check during full summary download
        let pollTimer = null;
        let isPolling = false;
        const refreshProgress = async () => {
          if (isPolling) return;
          isPolling = true;
          try {
            const status = await fetchSyncStatus(targetFolderId);
            store.setFolderSyncProgress(targetFolderId, status);
          } catch (e) {
            // Ignore background sync status error
          } finally {
            isPolling = false;
          }
        };

        // Poll progress during long sync
        pollTimer = setInterval(refreshProgress, 2000);

        try {
          const res = await syncFullFolderSummaries(targetFolderId);
          if (pollTimer) {
            clearInterval(pollTimer);
            pollTimer = null;
          }
          if (res.status === "cancelled") {
            showToast(`Summary download cancelled: ${res.synced_count ?? 0} messages synced.`, "info");
          } else {
            const synced = res.synced_count ?? 0;
            const removed = res.removed_count ?? 0;
            if (synced > 0 && removed > 0) {
              showToast(`Full summaries updated: ${synced} new added, ${removed} removed.`, "success");
            } else if (synced > 0) {
              showToast(`Full summaries downloaded: ${synced} new messages synced.`, "success");
            } else if (removed > 0) {
              showToast(`Full summaries updated: ${removed} removed message(s) purged.`, "success");
            } else {
              showToast("Full summaries checked: Mailbox is already up to date.", "success");
            }
          }
        } catch (err) {
          if (pollTimer) {
            clearInterval(pollTimer);
            pollTimer = null;
          }
          showToast(err.detail || err.message || "Failed to download full summaries", "error");
        } finally {
          if (pollTimer) {
            clearInterval(pollTimer);
            pollTimer = null;
          }
          store.finishFolderSync(targetFolderId);
          update();
        }
      });
    }

    // Attach Cancel Full Summaries button in menu
    const btnCancelMenu = container.querySelector("#btn-cancel-sync-menu");
    if (btnCancelMenu && selectedFolder) {
      btnCancelMenu.addEventListener("click", async (e) => {
        e.stopPropagation();
        isMenuOpen = false;
        update();
        showToast("Cancelling summary download...", "info");
        try {
          await cancelFolderSync(selectedFolder.id);
        } catch (err) {
          showToast(err.detail || err.message || "Failed to cancel summary sync", "error");
        }
      });
    }

    // Attach Sync TODO from patchwork button
    const btnSyncTodo = container.querySelector("#btn-sync-todo-patchwork");
    if (btnSyncTodo) {
      btnSyncTodo.addEventListener("click", async (e) => {
        e.stopPropagation();
        isSyncingTodo = true;
        update();
        try {
          showToast("Syncing delegated patches from Patchwork...", "info");
          const res = await syncTodoFromPatchwork();
          const count = res.synced_count ?? 0;
          const removed = res.removed_count ?? 0;
          const total = res.total_delegated ?? count;
          if (count > 0 && removed > 0) {
            showToast(`TODO synced: ${count} new added, ${removed} removed patch(es) purged (${total} active).`, "success");
          } else if (count > 0) {
            showToast(`Synced ${count} new delegated patch(es) (${total} active) from Patchwork!`, "success");
          } else if (removed > 0) {
            showToast(`TODO updated: ${removed} removed patch(es) purged (${total} active).`, "success");
          } else {
            showToast(`All ${total} delegated TODO patches are up to date!`, "success");
          }
          const updatedFolders = await fetchFolders();
          store.setFolders(updatedFolders);
          const currentFolderId = store.getState().selectedFolderId;
          if (currentFolderId) {
            const msgs = await fetchMessages(currentFolderId);
            store.setMessages(msgs);
          }
        } catch (err) {
          showToast(`Failed to sync TODO: ${err.message}`, "error");
        } finally {
          isSyncingTodo = false;
          isMenuOpen = false;
          update();
        }
      });
    }

    // Attach Refresh Mailboxes button
    const btnRefresh = container.querySelector("#btn-refresh-mailboxes");
    if (btnRefresh) {
      btnRefresh.addEventListener("click", async (e) => {
        e.stopPropagation();
        isMenuOpen = false;
        update();
        try {
          const updatedFolders = await fetchFolders();
          store.setFolders(updatedFolders);
          const currentFolderId = store.getState().selectedFolderId;
          if (currentFolderId) {
            const msgs = await fetchMessages(currentFolderId);
            store.setMessages(msgs);
          }
          showToast("Mailboxes refreshed.", "success");
        } catch (err) {
          showToast("Failed to refresh mailboxes", "error");
        }
      });
    }

    // Attach folder click handlers
    container.querySelectorAll(".folder-item").forEach((el) => {
      el.addEventListener("click", () => {
        const id = parseInt(el.getAttribute("data-id"), 10);
        isMenuOpen = false;
        store.selectFolder(id);
      });
    });
  }

  // Close dropdown on click outside
  document.addEventListener("click", (e) => {
    if (isMenuOpen && !container.querySelector(".folder-header-menu")?.contains(e.target)) {
      isMenuOpen = false;
      update();
    }
  });

  window.addEventListener("resize", () => {
    if (isMenuOpen) {
      isMenuOpen = false;
      update();
    }
  });

  container.addEventListener("scroll", () => {
    if (isMenuOpen) {
      isMenuOpen = false;
      update();
    }
  }, { passive: true });

  store.subscribe((event) => {
    if (
      event === EVENT_FOLDERS_UPDATED ||
      event === EVENT_FOLDER_SELECTED ||
      event === EVENT_MESSAGE_READ_UPDATED ||
      event === EVENT_FOLDER_SYNC_STARTED ||
      event === EVENT_FOLDER_SYNC_FINISHED ||
      event === EVENT_FOLDER_SYNC_PROGRESS ||
      event === EVENT_AI_REVIEW_STATE_CHANGED
    ) {
      if (event === EVENT_FOLDERS_UPDATED || event === EVENT_FOLDER_SELECTED) {
        isMenuOpen = false;
      }
      update();
    }
  });

  // On mount, check if there is an in-flight review to hydrate initial store state
  fetchAiReviewStatus()
    .then((status) => {
      if (status && (status.is_running || (status.queue_length && status.queue_length > 0))) {
        store.setAiReviewState({
          isRunning: status.is_running,
          messageId: status.active_message_id,
          activeReviewDbId: status.active_review_db_id || null,
          status: status.status,
          error: status.error || null,
          output: status.output || null,
          queue: status.queue || [],
        });
      }
    })
    .catch(() => {});

  update();
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
