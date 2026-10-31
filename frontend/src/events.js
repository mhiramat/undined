/**
 * Server-Sent Events (SSE) notification channel for Project Undine.
 * Receives real-time invalidation events and triggers debounced REST re-fetching.
 */

import { fetchFolders, fetchMessages, fetchMessageDetail, fetchMessageComments, invalidateMessageDetailCache, clearMessageDetailCache, isEventSuppressed } from "./api.js";

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
        if (rootMsgId && isEventSuppressed(rootMsgId)) return;
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
            if (isEventSuppressed(curId) || isEventSuppressed(curMsgId)) return;
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
        if (msgId && isEventSuppressed(msgId)) return;
        const activeDetail = this.store.getState().activeMessageDetail;
        if (!activeDetail?.message) return;
        const curDbId = activeDetail.message.id;
        const curMsgId = (activeDetail.message.message_id || "").replace(/^<|>$/g, "").trim();
        const cleanMsgId = String(msgId || "").replace(/^<|>$/g, "").trim();
        if (isEventSuppressed(curDbId) || (curMsgId && isEventSuppressed(curMsgId))) return;
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
        if (!data) return;

        const msgIdStr = data.message_id || "";
        const msgDbId = data.message_db_id;
        const cleanMsgId = msgIdStr.replace(/^<|>$/g, "").trim();

        // 1. Check if suppressed by recent explicit user action
        if (
          isEventSuppressed(cleanMsgId) ||
          isEventSuppressed(msgDbId) ||
          isEventSuppressed(msgIdStr)
        ) {
          return;
        }

        // 2. Find message in local frontend state
        const rawMsgs = this.store.getState().rawMessages || [];
        const localMsg = rawMsgs.find(
          (m) =>
            (msgDbId != null && m.id === msgDbId) ||
            (cleanMsgId && (m.message_id || "").replace(/^<|>$/g, "").trim() === cleanMsgId)
        );

        if (!localMsg) {
          // If not in current folder, and read status changed, update folder counters
          if (data.is_read !== undefined) {
            this.debouncedSyncFolders();
          }
          return;
        }

        // 3. Check for actual state differences and patch in-place
        let hasChanges = false;
        if (data.is_read !== undefined) {
          const targetRead = Boolean(data.is_read);
          const currentRead = Boolean(localMsg.is_read);
          if (targetRead !== currentRead) {
            hasChanges = true;
            this.store.updateMessageRead(localMsg.id, targetRead);
          }
        }

        if (data.is_replied !== undefined) {
          const targetReplied = Boolean(data.is_replied);
          const currentReplied = Boolean(localMsg.is_replied);
          if (targetReplied !== currentReplied) {
            hasChanges = true;
            this.store.updateMessageReplied(localMsg.id, targetReplied);
          }
        }

        if (data.tag !== undefined) {
          const targetTag = data.tag || null;
          const currentTag = localMsg.tag_key || null;
          if (targetTag !== currentTag) {
            hasChanges = true;
            this.store.updateMessageTag(
              localMsg.id,
              targetTag,
              data.tag_name || null,
              data.tag_color || null
            );
          }
        }

        if (!hasChanges) {
          return; // No-op, UI already represents this state
        }
      } catch (err) {
        console.warn("[UndineD SSE] Error handling message.updated event:", err);
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
      clearMessageDetailCache();
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
