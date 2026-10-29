/**
 * Server-Sent Events (SSE) notification channel for Project Undine.
 * Receives real-time invalidation events and triggers debounced REST re-fetching.
 */

import { fetchFolders, fetchMessages, fetchMessageDetail, fetchMessageComments, invalidateMessageDetailCache } from "./api.js";

// Debounce utility to coalesce burst notifications (e.g., multiple emails or comments)
export function debounce(fn, delayMs = 300) {
  let timer = null;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), delayMs);
  };
}

export class NotificationChannel {
  constructor(appStore) {
    this.store = appStore;
    this.es = null;

    this.debouncedSyncFolders = debounce(async (targetFolder) => {
      try {
        const folders = await fetchFolders();
        this.store.setFolders(folders);
        const currentFolderId = this.store.getState().selectedFolderId;
        const currentFolder = this.store.getState().folders.find((f) => f.id === currentFolderId);
        if (!targetFolder || (currentFolder && currentFolder.name === targetFolder)) {
          if (currentFolderId) {
            const msgs = await fetchMessages(currentFolderId);
            this.store.setMessages(msgs);
          }
        }
      } catch (err) {
        console.warn("[UndineD SSE] Error refreshing folders/messages:", err);
      }
    }, 300);

    this.debouncedSyncThread = debounce(async (rootMsgId) => {
      try {
        const folders = await fetchFolders();
        this.store.setFolders(folders);
        const currentFolderId = this.store.getState().selectedFolderId;
        if (currentFolderId) {
          const msgs = await fetchMessages(currentFolderId);
          this.store.setMessages(msgs);
        }
        const activeDetail = this.store.getState().activeMessageDetail;
        if (activeDetail?.message) {
          const curId = activeDetail.message.id;
          const curMsgId = (activeDetail.message.message_id || "").replace(/^<|>$/g, "").trim();
          const cleanRoot = (rootMsgId || "").replace(/^<|>$/g, "").trim();
          if (cleanRoot && (curMsgId === cleanRoot || activeDetail.message.in_reply_to?.includes(cleanRoot))) {
            invalidateMessageDetailCache(curId);
            const updated = await fetchMessageDetail(curId, { bypassCache: true });
            this.store.setActiveMessageDetail(updated);
          }
        }
      } catch (err) {
        console.warn("[UndineD SSE] Error refreshing thread:", err);
      }
    }, 300);

    this.debouncedSyncComments = debounce(async (msgId) => {
      try {
        const activeDetail = this.store.getState().activeMessageDetail;
        if (!activeDetail?.message) return;
        const curDbId = activeDetail.message.id;
        const curMsgId = (activeDetail.message.message_id || "").replace(/^<|>$/g, "").trim();
        const cleanMsgId = String(msgId || "").replace(/^<|>$/g, "").trim();
        if (!cleanMsgId || curMsgId === cleanMsgId || String(curDbId) === cleanMsgId) {
          const comments = await fetchMessageComments(curDbId);
          this.store.setActiveMessageDetail({
            ...activeDetail,
            comments: comments || [],
          });
        }
      } catch (err) {
        console.warn("[UndineD SSE] Error refreshing comments:", err);
      }
    }, 200);
  }

  connect() {
    if (this.es) {
      this.es.close();
    }

    // Cookies (HttpOnly session) are sent automatically by browser EventSource
    this.es = new EventSource("/api/events");

    this.es.addEventListener("mail.sync_completed", (e) => {
      try {
        const data = JSON.parse(e.data);
        this.debouncedSyncFolders(data?.folder);
      } catch (_) {
        this.debouncedSyncFolders();
      }
    });

    this.es.addEventListener("message.updated", (e) => {
      try {
        const data = JSON.parse(e.data);
        this.debouncedSyncFolders(data?.folder);
      } catch (_) {
        this.debouncedSyncFolders();
      }
    });

    this.es.addEventListener("thread.hydrated", (e) => {
      try {
        const data = JSON.parse(e.data);
        this.debouncedSyncThread(data?.root_message_id);
      } catch (_) {
        this.debouncedSyncThread();
      }
    });

    this.es.addEventListener("review.comment_added", (e) => {
      try {
        const data = JSON.parse(e.data);
        this.debouncedSyncComments(data?.message_id);
      } catch (_) {
        this.debouncedSyncComments();
      }
    });

    this.es.addEventListener("draft.created", () => {
      this.debouncedSyncFolders("Drafts");
    });

    this.es.addEventListener("draft.updated", () => {
      this.debouncedSyncFolders("Drafts");
    });

    this.es.addEventListener("patch.applied", () => {
      this.debouncedSyncFolders();
    });

    this.es.addEventListener("system.resync_required", () => {
      this.debouncedSyncFolders();
      const activeDetail = this.store.getState().activeMessageDetail;
      if (activeDetail?.message) {
        this.debouncedSyncComments(activeDetail.message.id);
      }
    });

    this.es.onerror = () => {
      console.warn("[UndineD SSE] Connection interrupted; browser will auto-reconnect.");
    };
  }

  disconnect() {
    if (this.es) {
      this.es.close();
      this.es = null;
    }
  }
}
