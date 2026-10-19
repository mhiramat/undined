/**
 * AddressCompletionProvider: Provides auto-completion for "To:" and "Cc:" header lines
 * in email draft documents based on UndineD's Known Addresses database.
 */

import * as vscode from "vscode";
import { UndineRestClient } from "../client/restClient";

export class AddressCompletionProvider implements vscode.CompletionItemProvider {
  constructor(private readonly client: UndineRestClient) {}

  public async provideCompletionItems(
    document: vscode.TextDocument,
    position: vscode.Position,
    _token: vscode.CancellationToken,
    _context: vscode.CompletionContext
  ): Promise<vscode.CompletionItem[] | undefined> {
    const lineText = document.lineAt(position.line).text;
    const prefix = lineText.substring(0, position.character);

    // Only trigger on lines starting with "To:" or "Cc:" (case-insensitive)
    const headerMatch = /^(To|Cc):\s*(.*)$/i.exec(prefix);
    if (!headerMatch) {
      return undefined;
    }

    const afterHeader = headerMatch[2];
    // Find the current token after the last comma
    const lastCommaIndex = afterHeader.lastIndexOf(",");
    const currentToken = (lastCommaIndex === -1
      ? afterHeader
      : afterHeader.substring(lastCommaIndex + 1)
    ).trim();

    try {
      const cleanQuery = currentToken.replace(/^[<"']+|[>"']+$/g, "").trim();
      const addresses = await this.client.getKnownAddresses(cleanQuery, 15);
      if (!addresses || addresses.length === 0) {
        return undefined;
      }

      // Calculate replacement range for the current address token
      const rawAfterComma = lastCommaIndex === -1 ? afterHeader : afterHeader.substring(lastCommaIndex + 1);
      const leadingSpaces = rawAfterComma.length - rawAfterComma.trimStart().length;
      const tokenStart = (lastCommaIndex === -1 ? prefix.length - afterHeader.length : prefix.lastIndexOf(",") + 1) + leadingSpaces;
      const range = new vscode.Range(
        new vscode.Position(position.line, Math.max(0, tokenStart)),
        position
      );

      return addresses.map((addr, index) => {
        const formatted = addr.name ? `"${addr.name}" <${addr.address}>` : addr.address;
        const item = new vscode.CompletionItem(
          addr.name ? `${addr.name} <${addr.address}>` : addr.address,
          vscode.CompletionItemKind.User
        );
        item.insertText = formatted;
        item.range = range;
        item.detail = addr.name ? `<${addr.address}>` : addr.address;
        item.documentation = new vscode.MarkdownString(
          `**${addr.name || addr.address}**\n\n` +
          `*Email*: \`${addr.address}\`\n\n` +
          `*Frequency*: ${addr.frequency} email(s)\n\n` +
          `*Last seen*: ${addr.last_seen}`
        );
        // Prioritize by query match rank (database returns prefix matches and highest frequency first)
        item.sortText = String(index).padStart(4, "0");
        return item;
      });
    } catch {
      return undefined;
    }
  }
}
