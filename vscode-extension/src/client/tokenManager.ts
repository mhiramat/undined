/**
 * TokenManager: Discovers and verifies the UndineD authentication token.
 */

import * as fs from "fs";
import * as path from "path";
import * as os from "os";

export interface TokenManagerOptions {
  customTokenPath?: string;
  strictPermissions?: boolean;
}

export class TokenManager {
  private cachedToken: string | null = null;
  private customPath?: string;
  private strictPermissions: boolean;

  constructor(options: TokenManagerOptions = {}) {
    this.customPath = options.customTokenPath;
    this.strictPermissions = options.strictPermissions ?? true;
  }

  public setCustomPath(p?: string): void {
    if (this.customPath !== p) {
      this.customPath = p;
      this.cachedToken = null;
    }
  }

  /**
   * Resolves the canonical token file path on the local filesystem.
   */
  public resolveTokenPath(): string {
    if (this.customPath && this.customPath.trim()) {
      return this.expandTilde(this.customPath.trim());
    }

    // Follow XDG Base Directory specification
    const xdgDataHome = process.env.XDG_DATA_HOME;
    if (xdgDataHome && xdgDataHome.trim()) {
      return path.join(xdgDataHome.trim(), "undined", "auth_token");
    }

    return path.join(os.homedir(), ".local", "share", "undined", "auth_token");
  }

  /**
   * Reads and verifies the auth token.
   * If reload is true, bypasses in-memory cache.
   */
  public getToken(reload = false): string {
    if (!reload && this.cachedToken) {
      return this.cachedToken;
    }

    const tokenPath = this.resolveTokenPath();

    if (!fs.existsSync(tokenPath)) {
      throw new Error(`UndineD auth token file not found at: ${tokenPath}. Ensure undined is running.`);
    }

    // POSIX Permission Verification: Token file should only be accessible by current user (0600 or 0400)
    if (this.strictPermissions && process.platform !== "win32") {
      const stat = fs.statSync(tokenPath);
      const perm = stat.mode & 0o777;
      const groupOrOther = perm & 0o077;
      if (groupOrOther !== 0) {
        throw new Error(
          `Insecure token file permissions (${(perm).toString(8)}) on ${tokenPath}. Required: 0600 (owner only).`
        );
      }
    }

    const raw = fs.readFileSync(tokenPath, "utf-8").trim();
    if (!raw) {
      throw new Error(`Token file at ${tokenPath} is empty.`);
    }

    this.cachedToken = raw;
    return this.cachedToken;
  }

  public clearCache(): void {
    this.cachedToken = null;
  }

  private expandTilde(filepath: string): string {
    if (filepath.startsWith("~/") || filepath === "~") {
      return path.join(os.homedir(), filepath.slice(1));
    }
    return filepath;
  }
}
