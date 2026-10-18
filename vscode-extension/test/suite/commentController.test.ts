import * as assert from "assert";
import * as http from "http";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { UndineCommentController } from "../../src/providers/commentController";
import { UndineRestClient } from "../../src/client/restClient";
import { TokenManager } from "../../src/client/tokenManager";
import { mockVscode } from "../vscodeMock";

describe("UndineCommentController", () => {
  let server: http.Server;
  let serverUrl: string;
  let tmpDir: string;
  let savedComments: any[] = [];
  let deletedCommentIds: number[] = [];

  before((done) => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "undine-comment-test-"));
    const tokenFile = path.join(tmpDir, "auth_token");
    fs.writeFileSync(tokenFile, "token-for-comments\n", { encoding: "utf-8" });
    if (process.platform !== "win32") {
      fs.chmodSync(tokenFile, 0o600);
    }

    server = http.createServer((req, res) => {
      res.setHeader("Content-Type", "application/json");

      if (req.url?.startsWith("/api/messages/patch-1/comments") && req.method === "GET") {
        res.statusCode = 200;
        res.end(
          JSON.stringify({
            message_id: "patch-1",
            comments: [
              {
                id: 10,
                file_path: "DESCRIPTION",
                line_no: 2,
                side: "new",
                text: "Existing description comment",
                author: "agent",
              },
              {
                id: 11,
                file_path: "kernel/trace/ftrace.c",
                line_no: 50,
                side: "new",
                text: "Existing diff comment",
                author: "user",
              },
            ],
          })
        );
        return;
      }

      if (req.url?.startsWith("/api/messages/patch-1/comments") && req.method === "POST") {
        let body = "";
        req.on("data", (chunk) => (body += chunk));
        req.on("end", () => {
          const parsed = JSON.parse(body);
          savedComments.push(parsed);
          res.statusCode = 200;
          res.end(JSON.stringify({ success: true, comment: { id: 99, ...parsed } }));
        });
        return;
      }

      if (req.url?.startsWith("/api/messages/patch-1/comments/10") && req.method === "DELETE") {
        deletedCommentIds.push(10);
        res.statusCode = 200;
        res.end(JSON.stringify({ success: true }));
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
    savedComments = [];
    deletedCommentIds = [];
  });

  it("should load existing review comments and populate threadMap", async () => {
    const tokenFile = path.join(tmpDir, "auth_token");
    const tm = new TokenManager({ customTokenPath: tokenFile });
    const client = new UndineRestClient(serverUrl, tm);

    const mockContext: any = { subscriptions: [] };
    const cc = new UndineCommentController(mockContext, client);

    await cc.loadCommentsForMessage("patch-1");

    const threadMap = (cc as any).threadMap as Map<string, any>;
    assert.strictEqual(threadMap.size, 2);

    assert.ok(threadMap.has("patch-1:DESCRIPTION:2"));
    const descThread = threadMap.get("patch-1:DESCRIPTION:2");
    assert.strictEqual(descThread.comments.length, 1);
    assert.strictEqual(descThread.comments[0].text, "Existing description comment");

    assert.ok(threadMap.has("patch-1:kernel/trace/ftrace.c:50"));
    const diffThread = threadMap.get("patch-1:kernel/trace/ftrace.c:50");
    assert.strictEqual(diffThread.comments.length, 1);
    assert.strictEqual(diffThread.comments[0].text, "Existing diff comment");
  });

  it("should post new comment on DESCRIPTION via handleCreateComment", async () => {
    const tokenFile = path.join(tmpDir, "auth_token");
    const tm = new TokenManager({ customTokenPath: tokenFile });
    const client = new UndineRestClient(serverUrl, tm);

    const mockContext: any = { subscriptions: [] };
    const cc = new UndineCommentController(mockContext, client);

    const mockReply: any = {
      text: "New note on description",
      thread: {
        uri: mockVscode.Uri.parse("undine-patch://patch-1/DESCRIPTION"),
        range: new mockVscode.Range(2, 0, 2, 0), // line 3 (0-indexed 2)
        comments: [],
      },
    };

    await (cc as any).handleCreateComment(mockReply);

    assert.strictEqual(savedComments.length, 1);
    assert.strictEqual(savedComments[0].file_path, "DESCRIPTION");
    assert.strictEqual(savedComments[0].line_no, 3);
    assert.strictEqual(savedComments[0].text, "New note on description");
    assert.strictEqual(mockReply.thread.comments.length, 1);
  });
});
