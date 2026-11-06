/**
 * Main application bootstrap for Project Undine.
 */

import { fetchConfig, fetchFolders, fetchMessages, setMessageTag, setMessagesTagBatch } from "./api.js";
import { getCachedTags } from "./tags.js";
import { renderFolderPane } from "./components/FolderPane.js";
import { renderSummaryPane } from "./components/SummaryPane.js";
import { renderViewerPane, triggerReply, triggerForward, triggerEdit, triggerNewMessage, triggerOpenReviewed, triggerOpenAiReview, isDraftFolder, isDraftMessage, isAiReviewMessage } from "./components/ViewerPane.js";
import { initPaneResizer } from "./components/PaneResizer.js";
import { openFilterDialog } from "./components/FilterDialog.js";
import { openInternalEditor } from "./components/InternalEditor.js";
import { store } from "./store.js";
import { NotificationChannel } from "./events.js";
import { EVENT_FOLDER_SELECTED } from "./constants.js";

async function init() {
  const urlParams = new URLSearchParams(window.location.search);
  const composerSessionId = urlParams.get("composer");

  if (composerSessionId) {
    // Standalone composer window mode
    document.body.classList.add("composer-standalone-body");
    const appEl = document.getElementById("app");
    if (appEl) {
      appEl.style.display = "none";
    }

    try {
      const config = await fetchConfig();
      store.setConfig(config);
    } catch (err) {
      console.warn("Could not load config:", err);
    }

    let composerParams = {};
    try {
      const raw = localStorage.getItem(composerSessionId);
      if (raw) {
        composerParams = JSON.parse(raw);
        sessionStorage.setItem(composerSessionId, raw);
        localStorage.removeItem(composerSessionId);
      } else {
        const sessionRaw = sessionStorage.getItem(composerSessionId);
        if (sessionRaw) {
          composerParams = JSON.parse(sessionRaw);
        }
      }
    } catch (e) {
      console.warn("Could not retrieve composer session:", e);
    }

    document.title = (composerParams.title || "Composer") + " - UndineD";
    openInternalEditor({
      ...composerParams,
      isStandalone: true,
    });
    return;
  }

  const folderContainer = document.getElementById("folder-pane");
  const summaryContainer = document.getElementById("summary-pane");
  const viewerContainer = document.getElementById("viewer-pane");

  // Mount components
  renderFolderPane(folderContainer);
  renderSummaryPane(summaryContainer);
  renderViewerPane(viewerContainer);

  // Initialize draggable pane resizer
  initPaneResizer();

  // Initialize real-time server-side event stream
  const notificationChannel = new NotificationChannel(store);
  notificationChannel.connect();
  window.addEventListener("beforeunload", () => {
    notificationChannel.disconnect();
  });

  // Load configuration
  try {
    const config = await fetchConfig();
    store.setConfig(config);
  } catch (err) {
    console.warn("Could not load config:", err);
  }

  // Listen for cross-window messages from standalone composer windows
  window.addEventListener("message", async (event) => {
    if (event.origin !== window.location.origin) return;
    const data = event.data;
    if (!data || typeof data !== "object") return;

    if (data.type === "UNDINE_MAIL_SENT") {
      if (data.origMsgId) {
        store.updateMessageReplied(data.origMsgId, true);
      }
      if (data.draftId) {
        store.removeMessage(data.draftId);
      }
    }

    if (data.type === "UNDINE_DRAFT_SAVED" || data.type === "UNDINE_MAIL_SENT") {
      try {
        const folders = await fetchFolders();
        store.setFolders(folders);
        const currentFolderId = store.getState().selectedFolderId;
        if (currentFolderId) {
          const msgs = await fetchMessages(currentFolderId);
          store.setMessages(msgs);
        }
      } catch (err) {
        console.warn("Failed to refresh folders/messages after composer action:", err);
      }
    }
  });

  // Global Keyboard Navigation (j / k / r / e / F / / / f / 0-9 tags)
  window.addEventListener("keydown", (e) => {
    // Ignore if typing inside input or textarea
    const activeTag = document.activeElement ? document.activeElement.tagName.toLowerCase() : "";
    if (activeTag === "input" || activeTag === "textarea") {
      return;
    }

    if (e.key === "Escape" && store.getState().isMultiSelectMode) {
      e.preventDefault();
      store.clearMultiSelection();
    } else if (e.key === "j" || e.key === "ArrowDown") {
      e.preventDefault();
      store.selectNextMessage();
    } else if (e.key === "k" || e.key === "ArrowUp") {
      e.preventDefault();
      store.selectPrevMessage();
    } else if (e.key === "r" && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      const current = store.getState().activeMessageDetail?.message;
      if (current && !isAiReviewMessage(current)) {
        if (isDraftMessage(current)) {
          triggerEdit(current);
        } else {
          triggerReply(current);
        }
      }
    } else if (e.key === "e" && !e.ctrlKey && !e.metaKey && !e.altKey) {
      const current = store.getState().activeMessageDetail?.message;
      if (current && !isAiReviewMessage(current) && isDraftMessage(current)) {
        e.preventDefault();
        triggerEdit(current);
      } else if (isDraftFolder(store.getState().selectedFolderId)) {
        e.preventDefault();
        triggerNewMessage();
      }
    } else if ((e.key === "c" || e.key === "C") && !e.ctrlKey && !e.metaKey && !e.altKey) {
      if (isDraftFolder(store.getState().selectedFolderId)) {
        e.preventDefault();
        triggerNewMessage();
      }
    } else if (e.key === "F" && !e.ctrlKey && !e.metaKey && !e.altKey) {
      const current = store.getState().activeMessageDetail?.message;
      if (current && !isAiReviewMessage(current) && !isDraftMessage(current)) {
        e.preventDefault();
        triggerForward(current);
      }
    } else if (e.key === "o" && !e.ctrlKey && !e.metaKey && !e.altKey) {
      const current = store.getState().activeMessageDetail?.message;
      const currentDetail = store.getState().activeMessageDetail;
      if (current && isAiReviewMessage(current)) {
        const reviewedMsg = currentDetail?.reviewed_message || current.reviewed_message;
        if (reviewedMsg) {
          e.preventDefault();
          triggerOpenReviewed(reviewedMsg);
        }
      } else if (current && !isAiReviewMessage(current)) {
        const aiReviewMsg = currentDetail?.ai_review_message || current.ai_review_message;
        if (aiReviewMsg) {
          e.preventDefault();
          triggerOpenAiReview(aiReviewMsg);
        }
      }
    } else if (e.key === "/" || e.key === "f" || ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "f")) {
      e.preventDefault();
      openFilterDialog();
    } else if (e.key >= "0" && e.key <= "9" && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      const { isMultiSelectMode, selectedMessageIds, activeMessageDetail } = store.getState();
      const num = parseInt(e.key, 10);
      if (isMultiSelectMode && selectedMessageIds && selectedMessageIds.size > 0) {
        const ids = Array.from(selectedMessageIds);
        const tagTarget = num > 0 ? `$cl_${num}` : null;
        const availableTags = getCachedTags();
        const tagDef = tagTarget && availableTags[tagTarget] ? availableTags[tagTarget] : null;
        setMessagesTagBatch(ids, tagTarget)
          .then((res) => {
            store.updateMessagesTag(
              ids,
              res?.tag_key !== undefined ? res.tag_key : (tagTarget || ""),
              res?.tag_name !== undefined ? res.tag_name : (tagDef ? tagDef.tag : ""),
              res?.tag_color !== undefined ? res.tag_color : (tagDef ? tagDef.color : "")
            );
          })
          .catch((err) => {
            console.error("Failed to update bulk message tags:", err);
          });
        return;
      }
      const current = activeMessageDetail?.message;
      if (current && !isAiReviewMessage(current)) {
        let tagTarget = null;
        if (num > 0) {
          const targetCl = `$cl_${num}`;
          if (current.tag_key?.toLowerCase() === targetCl.toLowerCase() || current.tag_key?.toLowerCase() === `$label${num}`) {
            tagTarget = null;
          } else {
            tagTarget = targetCl;
          }
        }
        setMessageTag(current.id, tagTarget)
          .then((res) => {
            store.updateMessageTag(current.id, res.tag_key, res.tag_name, res.tag_color);
          })
          .catch((err) => {
            console.error("Failed to update message tag:", err);
          });
      }
    }
  });

  // Listen for folder changes to fetch folder messages
  store.subscribe(async (event, folderId) => {
    if (event === EVENT_FOLDER_SELECTED) {
      try {
        const msgs = await fetchMessages(folderId);
        store.setMessages(msgs);
      } catch (err) {
        console.error("Failed to load folder messages:", err);
      }
    }
  });

  // Initial load
  try {
    const folders = await fetchFolders();
    store.setFolders(folders);

    const defaultFolderId = store.getState().selectedFolderId;
    if (defaultFolderId) {
      const msgs = await fetchMessages(defaultFolderId);
      store.setMessages(msgs);
    }
  } catch (err) {
    console.error("Failed to initialize UndineD:", err);
  }
}

document.addEventListener("DOMContentLoaded", init);
