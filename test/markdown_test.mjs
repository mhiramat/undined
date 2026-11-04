import test from "node:test";
import assert from "node:assert/strict";
import { isMarkdown, renderMarkdown, cleanBodyText } from "../frontend/src/markdown.js";

test("cleanBodyText: collapses invisible preheader padding and figure spaces", () => {
  const raw = "The desktop app picks up where the browser leaves off.  \u2007 \u034F \u2007 \u034F\n" +
    "\u2007 \u034F \u2007 \u034F \u2007 \u034F\n".repeat(20) +
    "\n\n\nClaude, closer to your work\n";
  const cleaned = cleanBodyText(raw);
  assert.equal(cleaned.includes("\u2007"), false);
  assert.equal(cleaned.includes("\u034F"), false);
  assert.equal(cleaned.includes("The desktop app picks up where the browser leaves off."), true);
  assert.equal(cleaned.includes("Claude, closer to your work"), true);
  // Verify that excessive newlines are collapsed to a single blank line
  assert.equal(cleaned.includes("\n\n\n"), false);
});

test("isMarkdown: detects fenced code blocks", () => {
  const md = "Here is some code:\n```c\nint x = 42;\n```\n";
  assert.equal(isMarkdown(md), true);
});

test("isMarkdown: detects markdown headings and lists", () => {
  const md = "## Summary\n- item 1\n- item 2\n";
  assert.equal(isMarkdown(md), true);
});

test("isMarkdown: detects markdown tables", () => {
  const md = "| Name | Status |\n| --- | --- |\n| Test | OK |\n";
  assert.equal(isMarkdown(md), true);
});

test("isMarkdown: detects task lists", () => {
  const md = "- [ ] Task 1\n- [x] Task 2\n";
  assert.equal(isMarkdown(md), true);
});

test("isMarkdown: rejects plain text emails", () => {
  const plain1 = "Hi dev,\n\nThanks for sending this patch.\nI will test it on my box.\n\nBest regards,\nReviewer";
  assert.equal(isMarkdown(plain1), false);

  const plain2 = "On 2026-11-01, developer wrote:\n> This is a quote from an email.\n> Another line of quote.\n\nSounds good to me.";
  assert.equal(isMarkdown(plain2), false);
});

test("renderMarkdown: escapes HTML and blocks malicious scripts (XSS)", () => {
  const malicious = '<script>alert("pwned")</script> <img src=x onerror=alert(1)> [evil](javascript:alert(1))';
  const html = renderMarkdown(malicious);
  assert.equal(html.includes("<script>"), false);
  assert.equal(html.includes("javascript:alert"), false);
  assert.equal(html.includes("&lt;script&gt;"), true);
  assert.equal(html.includes("&lt;img"), true);
});

test("renderMarkdown: preserves URL query parameters with underscores and autolinks", () => {
  const text = "Download the desktop app:\n<https://example.com/download?utm_content=hero_cta&opt_in=true#ctaButton>";
  const html = renderMarkdown(text);
  assert.equal(html.includes("<em>content=hero</em>"), false);
  assert.equal(html.includes("<em>in=true#ctaButton</em>"), false);
  assert.match(html, /<a href="https:\/\/example\.com\/download\?utm_content=hero_cta&amp;opt_in=true#ctaButton"/);
});

test("renderMarkdown: renders headings and formatting", () => {
  const md = "# Title\n\n**bold text** and *italic text* and `inline code`";
  const html = renderMarkdown(md);
  assert.match(html, /<h1 class="md-h md-h1">Title<\/h1>/);
  assert.match(html, /<strong>bold text<\/strong>/);
  assert.match(html, /<em>italic text<\/em>/);
  assert.match(html, /<code class="md-inline-code">inline code<\/code>/);
});

test("renderMarkdown: renders fenced code blocks with language", () => {
  const md = "```go\nfunc main() {\n\tprintln(\"hello\")\n}\n```";
  const html = renderMarkdown(md);
  assert.match(html, /<div class="md-code-card">/);
  assert.match(html, /<span class="md-code-lang">go<\/span>/);
  assert.match(html, /func main\(\)/);
});

test("renderMarkdown: renders tables with alignments", () => {
  const md = "| Left | Center | Right |\n| :--- | :---: | ---: |\n| 1 | 2 | 3 |";
  const html = renderMarkdown(md);
  assert.match(html, /<table class="md-table">/);
  assert.match(html, /<th style="text-align: left">Left<\/th>/);
  assert.match(html, /<th style="text-align: center">Center<\/th>/);
  assert.match(html, /<th style="text-align: right">Right<\/th>/);
  assert.match(html, /<td style="text-align: left">1<\/td>/);
});

test("renderMarkdown: renders blockquotes and task lists", () => {
  const md = "> quoted note\n\n- [x] Done\n- [ ] Pending";
  const html = renderMarkdown(md);
  assert.match(html, /<blockquote class="md-blockquote">/);
  assert.match(html, /<li class="md-task-item"><input type="checkbox" disabled checked class="md-task-checkbox"> Done<\/li>/);
  assert.match(html, /<li class="md-task-item"><input type="checkbox" disabled  class="md-task-checkbox"> Pending<\/li>/);
});
