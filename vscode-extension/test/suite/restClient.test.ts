import * as assert from "assert";
import * as http from "http";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { UndineRestClient } from "../../src/client/restClient";
import { TokenManager } from "../../src/client/tokenManager";

describe("UndineRestClient", () => {
  let server: http.Server;
  let serverUrl: string;
  let tmpDir: string;
  let tokenFile: string;
  let tokenManager: TokenManager;
  let receivedAuthHeaders: string[] = [];

  before((done) => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "undine-rest-test-"));
    tokenFile = path.join(tmpDir, "auth_token");
    fs.writeFileSync(tokenFile, "initial-token-12345\n", { encoding: "utf-8" });
    if (process.platform !== "win32") {
      fs.chmodSync(tokenFile, 0o600);
    }
    tokenManager = new TokenManager({ customTokenPath: tokenFile });

    server = http.createServer((req, res) => {
      const auth = req.headers.authorization || "";
      receivedAuthHeaders.push(auth);

      res.setHeader("Content-Type", "application/json");

      // Simulate 401 if token is expired
      if (auth === "Bearer expired-token") {
        // Simulate daemon having regenerated token
        fs.writeFileSync(tokenFile, "valid-reloaded-token\n", { encoding: "utf-8" });
        res.statusCode = 401;
        res.end(JSON.stringify({ error: "Unauthorized" }));
        return;
      }

      if (req.url === "/api/folders" && req.method === "GET") {
        res.statusCode = 200;
        res.end(JSON.stringify([{ id: 1, name: "INBOX", is_system: true, unread_count: 5 }]));
        return;
      }

      if (req.url === "/api/folders/1/sync-full" && req.method === "POST") {
        res.statusCode = 200;
        res.end(
          JSON.stringify({
            status: "completed",
            folder_id: 1,
            folder_name: "INBOX",
            synced_count: 7,
            removed_count: 0,
          })
        );
        return;
      }

      if (req.url === "/api/folders/1/sync-status" && req.method === "GET") {
        res.statusCode = 200;
        res.end(
          JSON.stringify({
            folder_id: 1,
            folder_name: "INBOX",
            is_syncing: false,
            synced: 7,
            total: 7,
            status: "completed",
          })
        );
        return;
      }

      if (req.url === "/api/folders/1/sync-cancel" && req.method === "POST") {
        res.statusCode = 200;
        res.end(JSON.stringify({ success: true }));
        return;
      }

      if (req.url?.startsWith("/api/patchwork/sync-todo") && req.method === "POST") {
        res.statusCode = 200;
        res.end(
          JSON.stringify({
            synced_count: 3,
            removed_count: 1,
            total_delegated: 5,
            patches: [],
          })
        );
        return;
      }

      if (req.url?.startsWith("/api/messages/101/comments") && req.method === "POST") {
        let body = "";
        req.on("data", (chunk) => (body += chunk));
        req.on("end", () => {
          const parsed = JSON.parse(body);
          res.statusCode = 200;
          res.end(JSON.stringify({ success: true, comment: { id: 42, ...parsed } }));
        });
        return;
      }

      if (req.url?.startsWith("/api/messages/101") && req.method === "GET") {
        res.statusCode = 200;
        res.end(
          JSON.stringify({
            message: { id: 101, message_id: "test@kernel.org", subject: "[PATCH] test patch" },
            comments: [],
            parsed_diff: { is_patch: true, preamble: "Description line 1", files: [] },
          })
        );
        return;
      }

      res.statusCode = 404;
      res.end(JSON.stringify({ error: "Not Found" }));
    });

    server.listen(0, "127.0.0.1", () => {
      const addr = server.address() as any;
      serverUrl = `http://127.0.0.1:${addr.port}`;
      done();
    });
  });

  after((done) => {
    server.close(() => {
      fs.rmSync(tmpDir, { recursive: true, force: true });
      done();
    });
  });

  beforeEach(() => {
    receivedAuthHeaders = [];
  });

  it("should send Authorization: Bearer token and fetch folders", async () => {
    const client = new UndineRestClient(serverUrl, tokenManager);
    const folders = await client.getFolders();
    assert.strictEqual(folders.length, 1);
    assert.strictEqual(folders[0].name, "INBOX");
    assert.strictEqual(receivedAuthHeaders[0], "Bearer initial-token-12345");
  });

  it("should fetch message details", async () => {
    const client = new UndineRestClient(serverUrl, tokenManager);
    const detail = await client.getMessageDetail(101);
    assert.strictEqual(detail.message.id, 101);
    assert.strictEqual(detail.message.subject, "[PATCH] test patch");
  });

  it("should post inline comments with DESCRIPTION target", async () => {
    const client = new UndineRestClient(serverUrl, tokenManager);
    const res = await client.saveComment(101, {
      file_path: "DESCRIPTION",
      line_no: 1,
      side: "new",
      text: "Clarify commit title",
    });
    assert.strictEqual(res.success, true);
    assert.strictEqual(res.comment.file_path, "DESCRIPTION");
    assert.strictEqual(res.comment.line_no, 1);
  });

  it("should trigger full folder summary sync and parse result", async () => {
    const client = new UndineRestClient(serverUrl, tokenManager);
    const res = await client.syncFolderFull(1);
    assert.strictEqual(res.status, "completed");
    assert.strictEqual(res.folder_id, 1);
    assert.strictEqual(res.folder_name, "INBOX");
    assert.strictEqual(res.synced_count, 7);
    assert.strictEqual(res.removed_count, 0);
  });

  it("should trigger Patchwork TODO sync and parse result", async () => {
    const client = new UndineRestClient(serverUrl, tokenManager);
    const res = await client.syncTodoFromPatchwork(["new", "under-review"]);
    assert.strictEqual(res.synced_count, 3);
    assert.strictEqual(res.removed_count, 1);
    assert.strictEqual(res.total_delegated, 5);
  });

  it("should retry with new token on 401 Unauthorized", async () => {
    // Put expired token in file
    fs.writeFileSync(tokenFile, "expired-token\n", { encoding: "utf-8" });
    tokenManager.clearCache();

    const client = new UndineRestClient(serverUrl, tokenManager);
    const folders = await client.getFolders();
    assert.strictEqual(folders.length, 1);
    // Should have sent expired-token first, then retried with valid-reloaded-token
    assert.strictEqual(receivedAuthHeaders.length, 2);
    assert.strictEqual(receivedAuthHeaders[0], "Bearer expired-token");
    assert.strictEqual(receivedAuthHeaders[1], "Bearer valid-reloaded-token");
  });
});
