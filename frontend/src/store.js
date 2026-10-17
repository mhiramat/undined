import { threadMessages, flatSortMessages, cleanMsgId, findParentId, findRootMessage } from "./threading.js";
import { parseFilterQuery, matchMessageFilter } from "./filter.js";
import { saveMessageComment, deleteMessageComment, discardDraftComments, markMessageRead } from "./api.js";
import { setCachedTags } from "./tags.js";
import {
  EVENT_AI_REVIEW_STATE_CHANGED,
  EVENT_CONFIG_LOADED,
  EVENT_FILTER_UPDATED,
  EVENT_FOLDERS_UPDATED,
  EVENT_FOLDER_SELECTED,
  EVENT_FOLDER_SYNC_FINISHED,
  EVENT_FOLDER_SYNC_PROGRESS,
  EVENT_FOLDER_SYNC_STARTED,
  EVENT_MESSAGES_UPDATED,
  EVENT_MESSAGE_DETAIL_LOADED,
  EVENT_MESSAGE_READ_UPDATED,
  EVENT_MESSAGE_REPLIED_UPDATED,
  EVENT_MESSAGE_SELECTED,
  EVENT_MESSAGE_TAG_UPDATED,
  EVENT_NOTES_UPDATED,
  EVENT_WIDTH_RESIZED,
  FOLDER_B4_IMPORTS,
} from "./constants.js";

class UndineDStore {
  constructor() {
    this.state = {
      folders: [],
      selectedFolderId: null,
      rawMessages: [],
      messages: [],
      selectedMessageId: null,
      selectedMessageIndex: -1,
      sortKey: "date",
      sortOrder: "desc",
      threaded: true,
      collapsedThreadIds: this.loadCollapsedThreadIds(),
      filterQuery: "",
      parsedFilter: null,
      activeMessageDetail: null,
      activeNotes: {}, // key: `${file}:${lineNo}` -> { file, lineNo, text }
      containerWidth: 1200,
      syncingFolderIds: new Set(),
      folderSyncProgress: {},
      config: null,
      activeReview: {
        isRunning: false,
        messageId: null,
        activeReviewDbId: null,
        status: "idle",
        error: null,
        output: null,
        queue: [],
      },
    };
    this.listeners = new Set();
  }

  setConfig(config) {
    this.state.config = config;
    if (config?.tags) {
      setCachedTags(config.tags);
    }
    this.notify(EVENT_CONFIG_LOADED, config);
  }

  getConfig() {
    return this.state.config;
  }

  isFolderSyncing(folderId) {
    return this.state.syncingFolderIds.has(folderId);
  }

  startFolderSync(folderId) {
    this.state.syncingFolderIds.add(folderId);
    this.state.folderSyncProgress[folderId] = { is_syncing: true, synced: 0, total: 0, status: "connecting" };
    this.notify(EVENT_FOLDER_SYNC_STARTED, folderId);
  }

  finishFolderSync(folderId) {
    this.state.syncingFolderIds.delete(folderId);
    delete this.state.folderSyncProgress[folderId];
    this.notify(EVENT_FOLDER_SYNC_FINISHED, folderId);
  }

  setFolderSyncProgress(folderId, progress) {
    this.state.folderSyncProgress[folderId] = progress;
    this.notify(EVENT_FOLDER_SYNC_PROGRESS, { folderId, progress });
  }

  getFolderSyncProgress(folderId) {
    return this.state.folderSyncProgress[folderId] || null;
  }

  isAiReviewActive() {
    return Boolean(
      this.state.activeReview.isRunning ||
      (this.state.activeReview.queue && this.state.activeReview.queue.length > 0)
    );
  }

  isAiReviewTarget(id) {
    return Boolean(this.getAiReviewStatusForTarget(id));
  }

  getAiReviewStatusForTarget(id) {
    if (!id) return null;
    const current = String(id).replace(/^<|>$/g, "").trim();
    if (this.state.activeReview.messageId) {
      const target = String(this.state.activeReview.messageId).replace(/^<|>$/g, "").trim();
      if (target === current) {
        return this.state.activeReview.isRunning ? "in-progress" : (this.state.activeReview.status || "idle");
      }
    }
    if (this.state.activeReview.activeReviewDbId != null && String(this.state.activeReview.activeReviewDbId) === current) {
      return this.state.activeReview.isRunning ? "in-progress" : (this.state.activeReview.status || "idle");
    }
    if (this.state.activeReview.queue && Array.isArray(this.state.activeReview.queue)) {
      for (const q of this.state.activeReview.queue) {
        if (q.message_id && String(q.message_id).replace(/^<|>$/g, "").trim() === current) {
          return q.status || "queued";
        }
        if (q.review_db_id != null && String(q.review_db_id) === current) {
          return q.status || "queued";
        }
      }
    }
    return null;
  }

  setAiReviewState(state) {
    this.state.activeReview = {
      ...this.state.activeReview,
      ...state,
    };
    this.notify(EVENT_AI_REVIEW_STATE_CHANGED, this.state.activeReview);
  }

  getState() {
    return this.state;
  }

  subscribe(listener) {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  notify(event, payload) {
    for (const listener of this.listeners) {
      listener(event, payload, this.state);
    }
  }

  setFolders(folders) {
    this.state.folders = folders;
    if (this.state.selectedFolderId === null && folders.length > 0) {
      // Default to first sync_enabled folder or b4-imports or first folder
      const defFolder = folders.find((f) => f.sync_enabled) || folders.find((f) => f.name === FOLDER_B4_IMPORTS) || folders[0];
      this.state.selectedFolderId = defFolder.id;
    }
    this.notify(EVENT_FOLDERS_UPDATED, folders);
  }

  loadCollapsedThreadIds() {
    try {
      if (typeof localStorage !== "undefined") {
        const saved = localStorage.getItem("undine_collapsed_threads");
        if (saved) {
          const arr = JSON.parse(saved);
          if (Array.isArray(arr)) {
            return new Set(arr);
          }
        }
      }
    } catch (e) {
      console.warn("Failed to load collapsed threads from localStorage:", e);
    }
    return new Set();
  }

  saveCollapsedThreadIds() {
    try {
      if (typeof localStorage !== "undefined") {
        const arr = Array.from(this.state.collapsedThreadIds).slice(-1000);
        localStorage.setItem("undine_collapsed_threads", JSON.stringify(arr));
      }
    } catch (e) {
      console.warn("Failed to save collapsed threads to localStorage:", e);
    }
  }

  isThreadCollapsed(msg) {
    if (!msg) return false;
    const cleanMid = cleanMsgId(msg.message_id);
    return Boolean(
      (msg.id != null && this.state.collapsedThreadIds.has(msg.id)) ||
      (msg.id != null && this.state.collapsedThreadIds.has(String(msg.id))) ||
      (msg.id != null && this.state.collapsedThreadIds.has(`id-${msg.id}`)) ||
      (cleanMid && this.state.collapsedThreadIds.has(cleanMid))
    );
  }

  collapseThread(msg) {
    if (!msg) return;
    if (msg.id != null) {
      this.state.collapsedThreadIds.add(msg.id);
    }
    const cleanMid = cleanMsgId(msg.message_id);
    if (cleanMid) {
      this.state.collapsedThreadIds.add(cleanMid);
    }
    this.saveCollapsedThreadIds();
  }

  uncollapseThread(msg) {
    if (!msg) return;
    if (msg.id != null) {
      this.state.collapsedThreadIds.delete(msg.id);
      this.state.collapsedThreadIds.delete(String(msg.id));
      this.state.collapsedThreadIds.delete(`id-${msg.id}`);
    }
    const cleanMid = cleanMsgId(msg.message_id);
    if (cleanMid) {
      this.state.collapsedThreadIds.delete(cleanMid);
    }
    this.saveCollapsedThreadIds();
  }

  selectFolder(folderId, targetMessageId = null) {
    if (this.state.selectedFolderId === folderId) {
      if (targetMessageId != null) {
        let idx = this.state.messages.findIndex(
          (m) => m.id === targetMessageId || String(m.id) === String(targetMessageId) || (m.message_id && cleanMsgId(m.message_id) === cleanMsgId(targetMessageId))
        );
        if (idx < 0 && this.state.rawMessages) {
          const rawIdx = this.state.rawMessages.findIndex(
            (m) => m.id === targetMessageId || String(m.id) === String(targetMessageId) || (m.message_id && cleanMsgId(m.message_id) === cleanMsgId(targetMessageId))
          );
          if (rawIdx >= 0) {
            const targetMsg = this.state.rawMessages[rawIdx];
            const msgMap = new Map();
            for (const m of this.state.rawMessages) {
              const mid = cleanMsgId(m.message_id) || `id-${m.id}`;
              msgMap.set(mid, m);
            }
            const rootMsg = findRootMessage(targetMsg, msgMap);
            if (rootMsg) {
              this.uncollapseThread(rootMsg);
              this.recomputeMessages();
              idx = this.state.messages.findIndex(
                (m) => m.id === targetMessageId || String(m.id) === String(targetMessageId) || (m.message_id && cleanMsgId(m.message_id) === cleanMsgId(targetMessageId))
              );
            }
          }
        }
        if (idx >= 0) {
          this.selectMessageIndex(idx);
        }
      }
      return;
    }
    this.state.selectedFolderId = folderId;
    this.state.pendingTargetMessageId = targetMessageId;
    this.state.rawMessages = [];
    this.state.messages = [];
    this.state.selectedMessageId = null;
    this.state.selectedMessageIndex = -1;
    this.state.activeMessageDetail = null;
    this.state.activeNotes = {};
    this.notify(EVENT_FOLDER_SELECTED, folderId);
  }

  recomputeMessages() {
    let filtered = this.state.rawMessages;
    if (this.state.parsedFilter) {
      filtered = filtered.filter((m) => matchMessageFilter(m, this.state.parsedFilter));
    }

    if (this.state.threaded) {
      this.state.messages = threadMessages(
        filtered,
        this.state.sortKey,
        this.state.sortOrder,
        this.state.collapsedThreadIds
      );
    } else {
      this.state.messages = flatSortMessages(
        filtered,
        this.state.sortKey,
        this.state.sortOrder
      );
    }

    // Keep active selection index synchronized
    if (this.state.pendingTargetMessageId != null) {
      const target = this.state.pendingTargetMessageId;
      let idx = this.state.messages.findIndex(
        (m) => m.id === target || String(m.id) === String(target) || (target && m.message_id && cleanMsgId(m.message_id) === cleanMsgId(target))
      );
      if (idx < 0 && this.state.rawMessages) {
        const rawIdx = this.state.rawMessages.findIndex(
          (m) => m.id === target || String(m.id) === String(target) || (target && m.message_id && cleanMsgId(m.message_id) === cleanMsgId(target))
        );
        if (rawIdx >= 0 && this.state.collapsedThreadIds.size > 0) {
          const targetMsg = this.state.rawMessages[rawIdx];
          const msgMap = new Map();
          for (const m of this.state.rawMessages) {
            const mid = cleanMsgId(m.message_id) || `id-${m.id}`;
            msgMap.set(mid, m);
          }
          const rootMsg = findRootMessage(targetMsg, msgMap);
          if (rootMsg) {
            this.uncollapseThread(rootMsg);
            if (this.state.threaded) {
              this.state.messages = threadMessages(
                filtered,
                this.state.sortKey,
                this.state.sortOrder,
                this.state.collapsedThreadIds
              );
            }
            idx = this.state.messages.findIndex(
              (m) => m.id === target || String(m.id) === String(target) || (target && m.message_id && cleanMsgId(m.message_id) === cleanMsgId(target))
            );
          }
        }
      }
      if (idx >= 0) {
        this.state.selectedMessageIndex = idx;
        this.state.selectedMessageId = this.state.messages[idx].id;
      }
      this.state.pendingTargetMessageId = null;
    } else if (this.state.selectedMessageId !== null) {
      const idx = this.state.messages.findIndex((m) => m.id === this.state.selectedMessageId);
      this.state.selectedMessageIndex = idx;
    } else if (this.state.messages.length > 0) {
      this.state.selectedMessageIndex = 0;
      this.state.selectedMessageId = this.state.messages[0].id;
    } else {
      this.state.selectedMessageIndex = -1;
      this.state.selectedMessageId = null;
    }
  }

  setFilter(queryStr) {
    const q = (queryStr || "").trim();
    this.state.filterQuery = q;
    this.state.parsedFilter = parseFilterQuery(q);
    this.recomputeMessages();
    this.notify(EVENT_FILTER_UPDATED, this.state.filterQuery);
    this.notify(EVENT_MESSAGES_UPDATED, this.state.messages);
  }

  clearFilter() {
    if (!this.state.filterQuery && !this.state.parsedFilter) return;
    this.state.filterQuery = "";
    this.state.parsedFilter = null;
    this.recomputeMessages();
    this.notify(EVENT_FILTER_UPDATED, "");
    this.notify(EVENT_MESSAGES_UPDATED, this.state.messages);
  }

  toggleThreadCollapse(messageId) {
    let msg = null;
    if (this.state.rawMessages && this.state.rawMessages.length > 0) {
      msg = this.state.rawMessages.find(
        (m) => m.id === messageId || String(m.id) === String(messageId) || (m.message_id && cleanMsgId(m.message_id) === cleanMsgId(messageId))
      );
      if (msg) {
        const msgMap = new Map();
        for (const m of this.state.rawMessages) {
          const mid = cleanMsgId(m.message_id) || `id-${m.id}`;
          msgMap.set(mid, m);
        }
        if (findParentId(msg, msgMap)) {
          // Child messages cannot be collapsed
          return;
        }
      }
    }

    const isCollapsed = msg ? this.isThreadCollapsed(msg) : (
      this.state.collapsedThreadIds.has(messageId) ||
      this.state.collapsedThreadIds.has(String(messageId)) ||
      this.state.collapsedThreadIds.has(cleanMsgId(messageId))
    );

    if (isCollapsed) {
      if (msg) {
        this.uncollapseThread(msg);
      } else {
        this.state.collapsedThreadIds.delete(messageId);
        this.state.collapsedThreadIds.delete(String(messageId));
        this.state.collapsedThreadIds.delete(cleanMsgId(messageId));
        this.saveCollapsedThreadIds();
      }
    } else {
      if (msg) {
        this.collapseThread(msg);
      } else {
        this.state.collapsedThreadIds.add(messageId);
        this.saveCollapsedThreadIds();
      }
    }
    this.recomputeMessages();
    this.notify(EVENT_MESSAGES_UPDATED, this.state.messages);
  }

  expandAllThreads() {
    if (this.state.rawMessages && this.state.rawMessages.length > 0) {
      for (const m of this.state.rawMessages) {
        this.state.collapsedThreadIds.delete(m.id);
        this.state.collapsedThreadIds.delete(String(m.id));
        this.state.collapsedThreadIds.delete(`id-${m.id}`);
        const cleanMid = cleanMsgId(m.message_id);
        if (cleanMid) {
          this.state.collapsedThreadIds.delete(cleanMid);
        }
      }
    } else {
      this.state.collapsedThreadIds.clear();
    }
    this.saveCollapsedThreadIds();
    this.recomputeMessages();
    this.notify(EVENT_MESSAGES_UPDATED, this.state.messages);
  }

  collapseAllThreads() {
    if (this.state.rawMessages && this.state.rawMessages.length > 0) {
      const msgMap = new Map();
      for (const m of this.state.rawMessages) {
        const mid = cleanMsgId(m.message_id) || `id-${m.id}`;
        msgMap.set(mid, m);
      }
      const parentIds = new Set();
      for (const m of this.state.rawMessages) {
        const pid = findParentId(m, msgMap);
        if (pid) {
          parentIds.add(pid);
        }
      }
      for (const m of this.state.rawMessages) {
        const mid = cleanMsgId(m.message_id) || `id-${m.id}`;
        const isRoot = !findParentId(m, msgMap);
        const hasChildren = parentIds.has(mid);
        if (isRoot && hasChildren) {
          this.state.collapsedThreadIds.add(m.id);
          const cleanMid = cleanMsgId(m.message_id);
          if (cleanMid) {
            this.state.collapsedThreadIds.add(cleanMid);
          }
        }
      }
      this.saveCollapsedThreadIds();
    }
    this.recomputeMessages();
    this.notify(EVENT_MESSAGES_UPDATED, this.state.messages);
  }

  markThreadRead(rootMessageId, isRead) {
    if (!this.state.rawMessages || this.state.rawMessages.length === 0) return;
    const msgMap = new Map();
    for (const m of this.state.rawMessages) {
      const mid = cleanMsgId(m.message_id) || `id-${m.id}`;
      msgMap.set(mid, m);
    }

    const rootMsg = this.state.rawMessages.find(
      (m) => m.id === rootMessageId || String(m.id) === String(rootMessageId) || (m.message_id && cleanMsgId(m.message_id) === cleanMsgId(rootMessageId))
    );
    if (!rootMsg) return;
    const rootMid = cleanMsgId(rootMsg.message_id) || `id-${rootMsg.id}`;

    // Collect root and all descendant messages
    const threadMsgIds = [rootMsg.id];
    for (const m of this.state.rawMessages) {
      if (m.id === rootMsg.id) continue;
      let curr = m;
      const visited = new Set();
      while (curr) {
        const cid = cleanMsgId(curr.message_id) || `id-${curr.id}`;
        if (visited.has(cid)) break;
        visited.add(cid);
        const pid = findParentId(curr, msgMap);
        if (!pid) break;
        if (pid === rootMid) {
          threadMsgIds.push(m.id);
          break;
        }
        curr = msgMap.get(pid);
      }
    }

    const val = isRead ? 1 : 0;
    for (const id of threadMsgIds) {
      const m = this.state.rawMessages.find((item) => item.id === id);
      if (m) m.is_read = val;
      if (this.state.activeMessageDetail?.message?.id === id) {
        this.state.activeMessageDetail.message.is_read = val;
      }
      markMessageRead(id, isRead).catch((err) => {
        console.error(`Failed to mark message ${id} read:`, err);
      });
    }

    this.syncSelectedFolderUnreadCount();
    this.recomputeMessages();
    this.notify(EVENT_MESSAGES_UPDATED, this.state.messages);
    if (this.state.folders) {
      this.notify(EVENT_FOLDERS_UPDATED, this.state.folders);
    }
  }

  syncSelectedFolderUnreadCount() {
    if (this.state.selectedFolderId != null && Array.isArray(this.state.folders)) {
      const folder = this.state.folders.find((f) => f.id === this.state.selectedFolderId);
      if (folder) {
        folder.unread_count = this.state.rawMessages.filter((m) => !m.is_read).length;
        this.notify(EVENT_FOLDERS_UPDATED, this.state.folders);
      }
    }
  }

  setMessages(messages) {
    const prevSelectedId = this.state.selectedMessageId;
    this.state.rawMessages = messages || [];
    this.syncSelectedFolderUnreadCount();
    this.recomputeMessages();
    if (this.state.messages.length > 0 && this.state.selectedMessageIndex >= 0) {
      const current = this.state.messages[this.state.selectedMessageIndex];
      if (current.id !== prevSelectedId || !this.state.activeMessageDetail) {
        this.notify(EVENT_MESSAGE_SELECTED, current);
      }
    }
    this.notify(EVENT_MESSAGES_UPDATED, this.state.messages);
  }

  toggleSort(column) {
    if (this.state.sortKey === column) {
      this.state.sortOrder = this.state.sortOrder === "desc" ? "asc" : "desc";
    } else {
      this.state.sortKey = column;
      this.state.sortOrder = column === "date" ? "desc" : "asc";
    }
    this.recomputeMessages();
    this.notify(EVENT_MESSAGES_UPDATED, this.state.messages);
  }

  setSort(sortKey, sortOrder) {
    this.state.sortKey = sortKey;
    this.state.sortOrder = sortOrder;
    this.recomputeMessages();
    this.notify(EVENT_MESSAGES_UPDATED, this.state.messages);
  }

  toggleThreaded() {
    this.state.threaded = !this.state.threaded;
    this.recomputeMessages();
    this.notify(EVENT_MESSAGES_UPDATED, this.state.messages);
  }

  setThreaded(isThreaded) {
    if (this.state.threaded === isThreaded) return;
    this.state.threaded = isThreaded;
    this.recomputeMessages();
    this.notify(EVENT_MESSAGES_UPDATED, this.state.messages);
  }

  selectMessageIndex(index) {
    if (index < 0 || index >= this.state.messages.length) return;
    this.state.selectedMessageIndex = index;
    const msg = this.state.messages[index];
    this.state.selectedMessageId = msg.id;
    this.state.activeNotes = {}; // Reset notes for new message
    this.notify(EVENT_MESSAGE_SELECTED, msg);
  }

  removeMessage(messageId) {
    this.state.rawMessages = this.state.rawMessages.filter((m) => m.id !== messageId);
    this.recomputeMessages();
    if (this.state.selectedMessageId === messageId) {
      if (this.state.messages.length > 0) {
        const nextIdx = Math.min(
          this.state.selectedMessageIndex >= 0 ? this.state.selectedMessageIndex : 0,
          this.state.messages.length - 1
        );
        this.selectMessageIndex(Math.max(0, nextIdx));
      } else {
        this.state.selectedMessageId = null;
        this.state.selectedMessageIndex = -1;
        this.state.activeMessageDetail = null;
        this.state.activeNotes = {};
        this.notify(EVENT_MESSAGE_SELECTED, null);
      }
    } else {
      this.state.selectedMessageIndex = this.state.messages.findIndex(
        (m) => m.id === this.state.selectedMessageId
      );
    }
    this.notify(EVENT_MESSAGES_UPDATED, this.state.messages);
  }

  updateMessageRead(messageId, isRead) {
    const rawMsg = this.state.rawMessages.find((m) => m.id === messageId);
    const prevRead = rawMsg ? Boolean(rawMsg.is_read) : null;
    if (rawMsg) {
      rawMsg.is_read = isRead ? 1 : 0;
    }
    const computedMsg = this.state.messages.find((m) => m.id === messageId);
    if (computedMsg) {
      computedMsg.is_read = isRead ? 1 : 0;
    }
    if (
      this.state.activeMessageDetail &&
      this.state.activeMessageDetail.message &&
      this.state.activeMessageDetail.message.id === messageId
    ) {
      this.state.activeMessageDetail.message.is_read = isRead ? 1 : 0;
    }

    // Immediately update folder unread count
    const folderId = (rawMsg && rawMsg.folder_id) || this.state.selectedFolderId;
    if (folderId != null && Array.isArray(this.state.folders)) {
      const folder = this.state.folders.find((f) => f.id === folderId);
      if (folder) {
        if (this.state.selectedFolderId === folderId && this.state.rawMessages.length > 0) {
          folder.unread_count = this.state.rawMessages.filter((m) => !m.is_read).length;
        } else if (prevRead !== null && prevRead !== Boolean(isRead)) {
          if (isRead) {
            folder.unread_count = Math.max(0, (folder.unread_count || 0) - 1);
          } else {
            folder.unread_count = (folder.unread_count || 0) + 1;
          }
        }
        this.notify(EVENT_FOLDERS_UPDATED, this.state.folders);
      }
    }

    this.recomputeMessages();
    this.notify(EVENT_MESSAGE_READ_UPDATED, { id: messageId, is_read: isRead ? 1 : 0 });
    this.notify(EVENT_MESSAGES_UPDATED, this.state.messages);
  }

  updateMessageReplied(messageId, isReplied = true) {
    const rawMsg = this.state.rawMessages.find((m) => m.id === messageId || m.message_id === messageId);
    if (rawMsg) {
      rawMsg.is_replied = isReplied ? 1 : 0;
    }
    const computedMsg = this.state.messages.find((m) => m.id === messageId || m.message_id === messageId);
    if (computedMsg) {
      computedMsg.is_replied = isReplied ? 1 : 0;
    }
    if (
      this.state.activeMessageDetail &&
      this.state.activeMessageDetail.message &&
      (this.state.activeMessageDetail.message.id === messageId || this.state.activeMessageDetail.message.message_id === messageId)
    ) {
      this.state.activeMessageDetail.message.is_replied = isReplied ? 1 : 0;
    }
    this.notify(EVENT_MESSAGE_REPLIED_UPDATED, { id: rawMsg ? rawMsg.id : messageId, is_replied: isReplied ? 1 : 0 });
    this.notify(EVENT_MESSAGES_UPDATED, this.state.messages);
  }

  updateMessageTag(messageId, tagKey, tagName, tagColor) {
    const rawMsg = this.state.rawMessages.find((m) => m.id === messageId);
    if (rawMsg) {
      rawMsg.tag_key = tagKey;
      rawMsg.tag_name = tagName;
      rawMsg.tag_color = tagColor;
    }
    const computedMsg = this.state.messages.find((m) => m.id === messageId);
    if (computedMsg) {
      computedMsg.tag_key = tagKey;
      computedMsg.tag_name = tagName;
      computedMsg.tag_color = tagColor;
    }
    if (
      this.state.activeMessageDetail &&
      this.state.activeMessageDetail.message &&
      this.state.activeMessageDetail.message.id === messageId
    ) {
      this.state.activeMessageDetail.message.tag_key = tagKey;
      this.state.activeMessageDetail.message.tag_name = tagName;
      this.state.activeMessageDetail.message.tag_color = tagColor;
    }
    if (this.state.parsedFilter) {
      this.recomputeMessages();
    }
    this.notify(EVENT_MESSAGE_TAG_UPDATED, { messageId, tagKey, tagName, tagColor });
    this.notify(EVENT_MESSAGES_UPDATED, this.state.messages);
  }

  setActiveMessageDetail(detail) {
    this.state.activeMessageDetail = detail;
    this.state.activeNotes = {};
    if (detail && detail.comments && Array.isArray(detail.comments)) {
      for (const c of detail.comments) {
        const file = c.file_path || c.file;
        const lineNo = c.line_no != null ? c.line_no : c.lineNo;
        if (file != null && lineNo != null) {
          const key = `${file}:${lineNo}`;
          this.state.activeNotes[key] = {
            id: c.id,
            file: file,
            line_no: Number(lineNo),
            lineNo: Number(lineNo),
            side: c.side || "new",
            text: c.text,
            state: c.state || "draft",
            author: c.author || "user",
          };
        }
      }
    }
    this.notify(EVENT_NOTES_UPDATED, this.state.activeNotes);
    this.notify(EVENT_MESSAGE_DETAIL_LOADED, detail);
  }

  setNote(file, lineNo, text, side = "new") {
    const key = `${file}:${lineNo}`;
    const trimmed = text ? text.trim() : "";
    const activeMsg = this.state.activeMessageDetail?.message;
    const msgId = activeMsg?.id || this.state.selectedMessageId;

    if (!trimmed) {
      const existing = this.state.activeNotes[key];
      delete this.state.activeNotes[key];
      if (existing && existing.id && msgId != null) {
        deleteMessageComment(msgId, existing.id).catch((err) => {
          console.error("Failed to delete comment:", err);
        });
      }
    } else {
      const existing = this.state.activeNotes[key] || {};
      const noteObj = {
        ...existing,
        file,
        line_no: Number(lineNo),
        lineNo: Number(lineNo),
        side: side,
        text: trimmed,
        state: existing.state || "draft",
        author: existing.author || "user",
      };
      this.state.activeNotes[key] = noteObj;
      if (msgId != null) {
        saveMessageComment(msgId, {
          file,
          line_no: Number(lineNo),
          side,
          text: trimmed,
          state: noteObj.state,
          author: noteObj.author,
          comment_id: existing.id || undefined,
        }).then((res) => {
          if (res?.comment?.id && this.state.activeNotes[key]) {
            this.state.activeNotes[key].id = res.comment.id;
          }
        }).catch((err) => {
          console.error("Failed to save comment:", err);
        });
      }
    }
    this.notify(EVENT_NOTES_UPDATED, this.state.activeNotes);
  }

  async discardDraftNotes(messageId) {
    const msgId = messageId || this.state.activeMessageDetail?.message?.id || this.state.selectedMessageId;
    if (msgId != null) {
      try {
        await discardDraftComments(msgId);
      } catch (err) {
        console.error("Failed to discard draft comments:", err);
      }
    }
    for (const [k, n] of Object.entries(this.state.activeNotes)) {
      if (!n.state || n.state === "draft") {
        delete this.state.activeNotes[k];
      }
    }
    this.notify(EVENT_NOTES_UPDATED, this.state.activeNotes);
  }

  getNotesList() {
    return Object.values(this.state.activeNotes);
  }

  setContainerWidth(width) {
    if (this.state.containerWidth !== width) {
      this.state.containerWidth = width;
      this.notify(EVENT_WIDTH_RESIZED, width);
    }
  }

  // Keyboard Navigation: j = next, k = prev
  selectNextMessage() {
    const nextIdx = this.state.selectedMessageIndex + 1;
    if (nextIdx < this.state.messages.length) {
      this.selectMessageIndex(nextIdx);
    }
  }

  selectPrevMessage() {
    const prevIdx = this.state.selectedMessageIndex - 1;
    if (prevIdx >= 0) {
      this.selectMessageIndex(prevIdx);
    }
  }
}

export const store = new UndineDStore();
