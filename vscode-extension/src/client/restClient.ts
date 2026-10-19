/**
 * UndineRestClient: Typed HTTP client for interacting with UndineD REST endpoints.
 */

import { TokenManager } from "./tokenManager";
import {
  Folder,
  FolderSyncStatus,
  MessageSummary,
  MessageDetail,
  PatchComment,
  B4ImportResult,
  PatchApplyResult,
  QuotePreviewResult,
  SaveDraftPayload,
  SendEmailPayload,
  PatchworkPatch,
  KnownAddress,
} from "./types";

export class UndineRestClient {
  private baseUrl: string;
  private tokenManager: TokenManager;

  constructor(baseUrl: string, tokenManager: TokenManager) {
    this.baseUrl = baseUrl.replace(/\/+$/, "");
    this.tokenManager = tokenManager;
  }

  public setBaseUrl(url: string): void {
    this.baseUrl = url.replace(/\/+$/, "");
  }

  private async request<T>(endpoint: string, options: RequestInit = {}, isRetry = false): Promise<T> {
    const token = this.tokenManager.getToken(isRetry);
    const url = `${this.baseUrl}${endpoint.startsWith("/") ? endpoint : `/${endpoint}`}`;

    const headers: Record<string, string> = {
      Accept: "application/json",
      Authorization: `Bearer ${token}`,
      ...(options.headers as Record<string, string>),
    };

    if (options.body && !headers["Content-Type"]) {
      headers["Content-Type"] = "application/json";
    }

    const res = await fetch(url, {
      ...options,
      headers,
    });

    if (res.status === 401 && !isRetry) {
      // Token may have been regenerated; retry once with a refreshed token from disk
      this.tokenManager.clearCache();
      return this.request<T>(endpoint, options, true);
    }

    if (!res.ok) {
      let errMessage = `HTTP ${res.status} ${res.statusText}`;
      try {
        const errorJson = await res.json() as { detail?: string; error?: string };
        errMessage = errorJson.detail || errorJson.error || errMessage;
      } catch {
        const text = await res.text().catch(() => "");
        if (text) errMessage = `${errMessage}: ${text}`;
      }
      throw new Error(`UndineD API error [${endpoint}]: ${errMessage}`);
    }

    return (await res.json()) as T;
  }

  // --- Folders ---

  public async getFolders(): Promise<Folder[]> {
    return this.request<Folder[]>("/api/folders");
  }

  public async syncFolderFull(folderId: number): Promise<{ success: boolean; synced: number }> {
    return this.request<{ success: boolean; synced: number }>(`/api/folders/${folderId}/sync-full`, {
      method: "POST",
    });
  }

  public async getFolderSyncStatus(folderId: number): Promise<FolderSyncStatus> {
    return this.request<FolderSyncStatus>(`/api/folders/${folderId}/sync-status`);
  }

  public async cancelFolderSync(folderId: number): Promise<{ success: boolean }> {
    return this.request<{ success: boolean }>(`/api/folders/${folderId}/sync-cancel`, {
      method: "POST",
    });
  }

  // --- Messages ---

  public async getMessages(folderId?: number, limit?: number, offset = 0): Promise<MessageSummary[]> {
    const params = new URLSearchParams();
    if (folderId != null) params.append("folder_id", String(folderId));
    if (limit != null && limit > 0) params.append("limit", String(limit));
    if (offset > 0) params.append("offset", String(offset));
    const qs = params.toString() ? `?${params.toString()}` : "";
    return this.request<MessageSummary[]>(`/api/messages${qs}`);
  }

  public async getMessageDetail(idOrIndex: string | number): Promise<MessageDetail> {
    return this.request<MessageDetail>(`/api/messages/${encodeURIComponent(String(idOrIndex))}`);
  }

  public async getMessageByMsgId(msgId: string): Promise<MessageSummary | null> {
    const cleanId = msgId.replace(/^<|>$/g, "");
    try {
      const res = await this.request<MessageSummary[]>(`/api/messages?msgid=${encodeURIComponent(cleanId)}`);
      if (res && res.length > 0) return res[0];
    } catch {}
    try {
      const searchRes = await this.search(cleanId);
      const match = searchRes.find(
        (r) => r.message_id.replace(/^<|>$/g, "") === cleanId
      );
      if (match) return match;
    } catch {}
    return null;
  }

  public async setMessageRead(id: number, isRead = true): Promise<{ success: boolean }> {
    return this.request<{ success: boolean }>(`/api/messages/${id}/read`, {
      method: "POST",
      body: JSON.stringify({ is_read: isRead }),
    });
  }

  public async setMessageReplied(id: number, isReplied = true): Promise<{ success: boolean }> {
    return this.request<{ success: boolean }>(`/api/messages/${id}/replied`, {
      method: "POST",
      body: JSON.stringify({ is_replied: isReplied }),
    });
  }

  public async setMessageTag(id: number, tagKey: string | null): Promise<{ success: boolean }> {
    return this.request<{ success: boolean }>(`/api/messages/${id}/tag`, {
      method: "POST",
      body: JSON.stringify({ tag: tagKey }),
    });
  }

  public async deleteMessage(idOrMsgId: string | number): Promise<{ success: boolean }> {
    return this.request<{ success: boolean }>(`/api/messages/${encodeURIComponent(String(idOrMsgId))}`, {
      method: "DELETE",
    });
  }

  // --- Comments ---

  public async getComments(idOrMsgId: string | number, state?: string): Promise<{ message_id: string; comments: PatchComment[] }> {
    const qs = state ? `?state=${encodeURIComponent(state)}` : "";
    return this.request<{ message_id: string; comments: PatchComment[] }>(
      `/api/messages/${encodeURIComponent(String(idOrMsgId))}/comments${qs}`
    );
  }

  public async saveComment(idOrMsgId: string | number, comment: Partial<PatchComment>): Promise<{ success: boolean; comment: PatchComment }> {
    return this.request<{ success: boolean; comment: PatchComment }>(
      `/api/messages/${encodeURIComponent(String(idOrMsgId))}/comments`,
      {
        method: "POST",
        body: JSON.stringify(comment),
      }
    );
  }

  public async deleteComment(idOrMsgId: string | number, commentId: number): Promise<{ success: boolean }> {
    return this.request<{ success: boolean }>(
      `/api/messages/${encodeURIComponent(String(idOrMsgId))}/comments/${commentId}`,
      {
        method: "DELETE",
      }
    );
  }

  public async discardDraftComments(idOrMsgId: string | number): Promise<{ success: boolean; discarded: number }> {
    return this.request<{ success: boolean; discarded: number }>(
      `/api/messages/${encodeURIComponent(String(idOrMsgId))}/comments/discard`,
      {
        method: "POST",
      }
    );
  }

  // --- Search ---

  public async search(query: string): Promise<MessageSummary[]> {
    return this.request<MessageSummary[]>(`/api/search?q=${encodeURIComponent(query)}`);
  }

  // --- b4 & Patch Application ---

  public async importB4(messageId: string): Promise<B4ImportResult> {
    return this.request<B4ImportResult>("/api/threads/import-b4", {
      method: "POST",
      body: JSON.stringify({ message_id: messageId }),
    });
  }

  public async applyPatch(
    messageId: string,
    targetGitRepo?: string,
    branchName?: string,
    baseCommit?: string
  ): Promise<PatchApplyResult> {
    return this.request<PatchApplyResult>("/api/patch/apply", {
      method: "POST",
      body: JSON.stringify({
        message_id: messageId,
        target_git_repo: targetGitRepo,
        branch_name: branchName,
        base_commit: baseCommit,
      }),
    });
  }

  public async abortPatchApply(params: {
    target_git_repo?: string;
    branch_name?: string;
    base_commit?: string;
    original_branch?: string;
    head_before?: string;
  } = {}): Promise<PatchApplyResult> {
    return this.request<PatchApplyResult>("/api/patch/apply/abort", {
      method: "POST",
      body: JSON.stringify(params),
    });
  }

  // --- LKML Quote & Composer ---

  public async previewQuote(
    messageId: string,
    notes: PatchComment[] = [],
    generalComment = ""
  ): Promise<QuotePreviewResult> {
    return this.request<QuotePreviewResult>("/api/composer/preview-quote", {
      method: "POST",
      body: JSON.stringify({
        message_id: messageId,
        notes,
        general_comment: generalComment,
      }),
    });
  }

  public async saveDraft(payload: SaveDraftPayload): Promise<{ success: boolean; message_id: string; db_id: number; file_path: string }> {
    return this.request<{ success: boolean; message_id: string; db_id: number; file_path: string }>(
      "/api/composer/save-draft",
      {
        method: "POST",
        body: JSON.stringify(payload),
      }
    );
  }

  public async sendEmail(payload: SendEmailPayload): Promise<{ success: boolean; message: string }> {
    return this.request<{ success: boolean; message: string }>("/api/smtp/send", {
      method: "POST",
      body: JSON.stringify(payload),
    });
  }

  // --- Patchwork ---

  public async getPatchworkInfo(msgid: string, refresh = false): Promise<any> {
    const clean = msgid.trim().replace(/^<|>$/g, "");
    return this.request<any>(`/api/patchwork/info?msgid=${encodeURIComponent(clean)}${refresh ? "&refresh=true" : ""}`);
  }

  public async getMyPatchworkPatches(params: { state?: string; project?: string; limit?: number } = {}): Promise<PatchworkPatch[]> {
    const sp = new URLSearchParams();
    if (params.state) sp.append("state", params.state);
    if (params.project) sp.append("project", params.project);
    if (params.limit) sp.append("limit", String(params.limit));
    const qs = sp.toString() ? `?${sp.toString()}` : "";
    return this.request<PatchworkPatch[]>(`/api/patchwork/my-patches${qs}`);
  }

  public async updatePatchworkPatch(patchId: number, updateData: any): Promise<any> {
    return this.request<any>(`/api/patchwork/patches/${patchId}`, {
      method: "PATCH",
      body: JSON.stringify(updateData),
    });
  }

  public async delegatePatchworkPatchToMe(patchId: number): Promise<any> {
    return this.request<any>(`/api/patchwork/patches/${patchId}/delegate-to-me`, {
      method: "POST",
    });
  }

  // --- System Configuration & Tags ---

  public async getConfig(): Promise<any> {
    return this.request<any>("/api/config");
  }

  public async getTags(): Promise<any> {
    return this.request<any>("/api/tags");
  }

  // --- Known Addresses ---

  public async getKnownAddresses(query = "", limit = 20): Promise<KnownAddress[]> {
    const params = new URLSearchParams();
    if (query) params.append("query", query);
    if (limit) params.append("limit", String(limit));
    const qs = params.toString() ? `?${params.toString()}` : "";
    return this.request<KnownAddress[]>(`/api/addresses${qs}`);
  }
}
