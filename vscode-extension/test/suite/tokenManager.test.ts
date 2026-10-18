import * as assert from "assert";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { TokenManager } from "../../src/client/tokenManager";

describe("TokenManager", () => {
  let tmpDir: string;

  beforeEach(() => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "undine-token-test-"));
  });

  afterEach(() => {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  });

  it("should read token from custom path with 0600 permissions", () => {
    const tokenFile = path.join(tmpDir, "auth_token");
    fs.writeFileSync(tokenFile, "test-secret-token-12345\n", { encoding: "utf-8" });
    if (process.platform !== "win32") {
      fs.chmodSync(tokenFile, 0o600);
    }

    const tm = new TokenManager({ customTokenPath: tokenFile, strictPermissions: true });
    assert.strictEqual(tm.getToken(), "test-secret-token-12345");
  });

  it("should reject token file with insecure permissions (e.g. 0644) on POSIX", () => {
    if (process.platform === "win32") {
      return; // Skip on Windows
    }

    const tokenFile = path.join(tmpDir, "auth_token_insecure");
    fs.writeFileSync(tokenFile, "insecure-token\n", { encoding: "utf-8" });
    fs.chmodSync(tokenFile, 0o644);

    const tm = new TokenManager({ customTokenPath: tokenFile, strictPermissions: true });
    assert.throws(
      () => tm.getToken(),
      /Insecure token file permissions/
    );
  });

  it("should throw error if token file does not exist", () => {
    const nonExistent = path.join(tmpDir, "non_existent");
    const tm = new TokenManager({ customTokenPath: nonExistent });
    assert.throws(() => tm.getToken(), /auth token file not found/);
  });

  it("should reload token when reload=true or clearCache() is called", () => {
    const tokenFile = path.join(tmpDir, "auth_token");
    fs.writeFileSync(tokenFile, "token-v1\n", { encoding: "utf-8" });
    if (process.platform !== "win32") {
      fs.chmodSync(tokenFile, 0o600);
    }

    const tm = new TokenManager({ customTokenPath: tokenFile });
    assert.strictEqual(tm.getToken(), "token-v1");

    // Overwrite with v2
    fs.writeFileSync(tokenFile, "token-v2\n", { encoding: "utf-8" });
    // Still cached
    assert.strictEqual(tm.getToken(), "token-v1");

    // Reloaded
    assert.strictEqual(tm.getToken(true), "token-v2");
  });
});
