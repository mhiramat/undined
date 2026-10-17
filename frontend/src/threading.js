/**
 * Threading and hierarchy sorting module for Project UndineD.
 *
 * Implements thread tree reconstruction and thread-prioritized sorting:
 * 1. Checks In-Reply-To header to identify parent message.
 * 2. If In-Reply-To is missing or parent not in current set, checks References header
 *    in reverse order (nearest ancestor).
 * 3. Ensures that child messages ALWAYS appear beneath their parent message,
 *    even when the sort order is reversed (e.g. ASC vs DESC).
 * 4. Cycle detection prevents loops if messages cross-reference each other.
 * 5. Generates visual thread branch guides (├─, └─, │  ).
 */

export function cleanMsgId(msgid) {
  if (!msgid) return "";
  return String(msgid).trim().replace(/^<+|>+$/g, "").trim();
}

export function extractMsgIds(headerVal) {
  if (!headerVal) return [];
  const val = String(headerVal);
  const bracketed = [];
  const rx = /<([^>]+)>/g;
  let match;
  while ((match = rx.exec(val)) !== null) {
    const cleaned = cleanMsgId(match[1]);
    if (cleaned) bracketed.push(cleaned);
  }
  if (bracketed.length > 0) return bracketed;
  return val.split(/\s+/).map(cleanMsgId).filter(Boolean);
}

export function findParentId(msg, msgMap) {
  const selfId = cleanMsgId(msg.message_id);

  // 1. In-Reply-To header
  if (msg.in_reply_to) {
    const candidates = extractMsgIds(msg.in_reply_to);
    for (let i = candidates.length - 1; i >= 0; i--) {
      const cand = candidates[i];
      if (cand !== selfId && msgMap.has(cand)) {
        return cand;
      }
    }
  }

  // 2. References header (when In-Reply-To is missing, empty, or parent not in current set)
  if (msg.references_header) {
    const candidates = extractMsgIds(msg.references_header);
    // In RFC 5322, References lists ancestors from root to immediate parent.
    // Searching in reverse finds the nearest ancestor present in msgMap.
    for (let i = candidates.length - 1; i >= 0; i--) {
      const cand = candidates[i];
      if (cand !== selfId && msgMap.has(cand)) {
        return cand;
      }
    }
  }

  return null;
}

export function findRootMessage(msg, msgMap) {
  if (!msg || !msgMap) return msg;
  let curr = msg;
  const visited = new Set();
  while (curr) {
    const cid = cleanMsgId(curr.message_id) || `id-${curr.id}`;
    if (visited.has(cid)) break;
    visited.add(cid);
    const parentId = findParentId(curr, msgMap);
    if (!parentId || !msgMap.has(parentId)) {
      return curr;
    }
    curr = msgMap.get(parentId);
  }
  return curr;
}

class ThreadNode {
  constructor(message) {
    this.message = message;
    this.children = [];
    this.parent = null;
    this.depth = 0;
  }
}

function getSortValue(msg, sortKey) {
  if (sortKey === "date") {
    if (typeof msg.epoch_timestamp === "number" && msg.epoch_timestamp > 0) {
      return msg.epoch_timestamp;
    }
    if (msg.date) {
      const t = new Date(msg.date).getTime();
      if (!isNaN(t)) return t;
    }
    return 0;
  } else if (sortKey === "subject") {
    const subj = String(msg.subject || "").toLowerCase();
    return subj.replace(/^(re:\s*|fwd:\s*)+/i, "").trim();
  } else if (sortKey === "from") {
    return String(msg.from_name || msg.from_addr || "").toLowerCase();
  } else if (sortKey === "tag") {
    return String(msg.tag_name || msg.tag_key || "").toLowerCase();
  }
  return msg.epoch_timestamp || 0;
}

function getThreadLatestTimestamp(node) {
  let latest = getSortValue(node.message, "date");
  if (node.children && node.children.length > 0) {
    for (const child of node.children) {
      const childLatest = getThreadLatestTimestamp(child);
      if (childLatest > latest) {
        latest = childLatest;
      }
    }
  }
  return latest;
}

function createNodeComparator(sortKey, sortOrder, isRoot = false) {
  const isDesc = String(sortOrder).toLowerCase() === "desc";

  return function compareNodes(a, b) {
    const valA = isRoot && sortKey === "date" ? getThreadLatestTimestamp(a) : getSortValue(a.message, sortKey);
    const valB = isRoot && sortKey === "date" ? getThreadLatestTimestamp(b) : getSortValue(b.message, sortKey);

    let cmp = 0;
    if (typeof valA === "number" && typeof valB === "number") {
      cmp = valA - valB;
    } else {
      cmp = String(valA).localeCompare(String(valB));
    }
    return isDesc ? -cmp : cmp;
  };
}

/**
 * Organize messages into thread trees and sort with thread priority.
 * Supports thread expansion and collapsing via collapsedThreadIds.
 */
export function threadMessages(
  messages,
  sortKey = "date",
  sortOrder = "desc",
  collapsedThreadIds = new Set()
) {
  if (!messages || messages.length === 0) return [];

  const msgMap = new Map();
  const nodes = new Map();

  for (const m of messages) {
    const mid = cleanMsgId(m.message_id) || `id-${m.id}`;
    msgMap.set(mid, m);
    nodes.set(mid, new ThreadNode(m));
  }

  const rootNodes = [];

  for (const [mid, node] of nodes.entries()) {
    const parentId = findParentId(node.message, msgMap);
    if (parentId && nodes.has(parentId)) {
      const parentNode = nodes.get(parentId);
      // Prevent cycles
      let curr = parentNode;
      let isCycle = false;
      while (curr !== null) {
        if (curr === node) {
          isCycle = true;
          break;
        }
        curr = curr.parent;
      }

      if (!isCycle) {
        node.parent = parentNode;
        parentNode.children.push(node);
      } else {
        rootNodes.push(node);
      }
    } else {
      rootNodes.push(node);
    }
  }

  const compareRoots = createNodeComparator(sortKey, sortOrder, true);
  const childComparator = createNodeComparator(sortKey, sortOrder, false);

  // Sort root threads according to chosen sort key and order
  rootNodes.sort(compareRoots);

  // Helper to count all descendants in subtree
  function countDescendants(node) {
    let count = 0;
    for (const child of node.children) {
      count += 1 + countDescendants(child);
    }
    return count;
  }

  // Helper to count unread descendants in subtree
  function countUnreadDescendants(node) {
    let count = 0;
    for (const child of node.children) {
      if (!child.message.is_read) {
        count++;
      }
      count += countUnreadDescendants(child);
    }
    return count;
  }

  const flattened = [];

  // Depth-first preorder traversal guarantees that parent is always visited
  // before any of its children.
  function dfs(node, depth, hasMoreAncestors = []) {
    node.depth = depth;

    let prefix = "";
    for (let k = 0; k < depth - 1; k++) {
      prefix += hasMoreAncestors[k] ? "│  " : "   ";
    }
    if (depth > 0) {
      const isLastChild = hasMoreAncestors[depth - 1] === false;
      prefix += isLastChild ? "└─ " : "├─ ";
    }

    const hasChildren = node.children.length > 0;
    const isTopLevel = depth === 0;
    const childCount = (isTopLevel && hasChildren) ? countDescendants(node) : 0;
    const unreadChildCount = (isTopLevel && hasChildren) ? countUnreadDescendants(node) : 0;
    const msgId = node.message.id;
    const cleanMid = cleanMsgId(node.message.message_id);
    const isCollapsed = isTopLevel && hasChildren && (
      (collapsedThreadIds && (
        collapsedThreadIds.has(msgId) ||
        collapsedThreadIds.has(String(msgId)) ||
        (cleanMid && collapsedThreadIds.has(cleanMid)) ||
        collapsedThreadIds.has(`id-${msgId}`)
      ))
    );

    const hasUnreadChildren = unreadChildCount > 0;
    // If a thread is collapsed, check whether any message in the thread is unread.
    // If there is an unread child, show the collapsed top message as unread.
    const isEffectivelyUnread = !node.message.is_read || (isCollapsed && hasUnreadChildren);

    flattened.push({
      ...node.message,
      thread_depth: depth,
      thread_prefix: prefix,
      has_children: Boolean(isTopLevel && hasChildren),
      is_collapsed: Boolean(isCollapsed),
      child_count: childCount,
      unread_child_count: unreadChildCount,
      has_unread_children: Boolean(hasUnreadChildren),
      is_read: isEffectivelyUnread ? 0 : 1,
      orig_is_read: node.message.is_read,
    });

    // If collapsed, do not visit children
    if (!isCollapsed) {
      // Sibling replies under this parent node are sorted according to sortOrder
      node.children.sort(childComparator);

      for (let i = 0; i < node.children.length; i++) {
        const child = node.children[i];
        const childHasMore = i < node.children.length - 1;
        dfs(child, depth + 1, [...hasMoreAncestors, childHasMore]);
      }
    }
  }

  for (const root of rootNodes) {
    dfs(root, 0, []);
  }

  return flattened;
}

/**
 * Flat sort without thread hierarchy.
 */
export function flatSortMessages(messages, sortKey = "date", sortOrder = "desc") {
  if (!messages || messages.length === 0) return [];
  const isDesc = String(sortOrder).toLowerCase() === "desc";

  return [...messages].sort((a, b) => {
    const valA = getSortValue(a, sortKey);
    const valB = getSortValue(b, sortKey);

    let cmp = 0;
    if (typeof valA === "number" && typeof valB === "number") {
      cmp = valA - valB;
    } else {
      cmp = String(valA).localeCompare(String(valB));
    }
    return isDesc ? -cmp : cmp;
  }).map((m) => ({
    ...m,
    thread_depth: 0,
    thread_prefix: "",
    has_children: false,
  }));
}
