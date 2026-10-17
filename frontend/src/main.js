/**
 * Main application bootstrap for Project Undine.
 */

import { fetchConfig, fetchFolders, fetchMessages, setMessageTag } from "./api.js";
import { renderFolderPane } from "./components/FolderPane.js";
import { renderSummaryPane } from "./components/SummaryPane.js";
import { renderViewerPane, triggerReply, triggerForward, triggerEdit, triggerNewMessage, triggerOpenReviewed, isDraftFolder, isDraftMessage, isAiReviewMessage } from "./components/ViewerPane.js";
import { initPaneResizer } from "./components/PaneResizer.js";
import { openFilterDialog } from "./components/FilterDialog.js";
import { store } from "./store.js";
import { EVENT_FOLDER_SELECTED } from "./constants.js";

async function init() {
  const folderContainer = document.getElementById("folder-pane");
  const summaryContainer = document.getElementById("summary-pane");
  const viewerContainer = document.getElementById("viewer-pane");

  // Mount components
  renderFolderPane(folderContainer);
  renderSummaryPane(summaryContainer);
  renderViewerPane(viewerContainer);

  // Initialize draggable pane resizer
  initPaneResizer();

  // Load configuration
  try {
    const config = await fetchConfig();
    store.setConfig(config);
  } catch (err) {
    console.warn("Could not load config:", err);
  }

  // Global Keyboard Navigation (j / k / r / e / F / / / f / 0-9 tags)
  window.addEventListener("keydown", (e) => {
    // Ignore if typing inside input or textarea
    const activeTag = document.activeElement ? document.activeElement.tagName.toLowerCase() : "";
    if (activeTag === "input" || activeTag === "textarea") {
      return;
    }

    if (e.key === "j" || e.key === "ArrowDown") {
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
      }
    } else if (e.key === "/" || e.key === "f" || ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "f")) {
      e.preventDefault();
      openFilterDialog();
    } else if (e.key >= "0" && e.key <= "9" && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      const current = store.getState().activeMessageDetail?.message;
      if (current && !isAiReviewMessage(current)) {
        const num = parseInt(e.key, 10);
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
