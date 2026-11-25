import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import {
  escapeAttr,
  sanitizeHttpUrl,
  sanitizeHexColor,
  sanitizeCssSlug,
} from "../frontend/src/linkify.js";

test("escapeAttr: escapes all HTML metacharacters", () => {
  assert.equal(
    escapeAttr(`a&b<c>d"e'f`),
    "a&amp;b&lt;c&gt;d&quot;e&#39;f"
  );
  assert.equal(escapeAttr(""), "");
  assert.equal(escapeAttr(null), "");
});

test("sanitizeHttpUrl: permits only http and https URLs", () => {
  assert.equal(
    sanitizeHttpUrl("https://patchwork.kernel.org/patch/123/"),
    "https://patchwork.kernel.org/patch/123/"
  );
  assert.equal(
    sanitizeHttpUrl("http://lore.kernel.org/r/msg@example.com"),
    "http://lore.kernel.org/r/msg@example.com"
  );
  assert.equal(sanitizeHttpUrl("javascript:alert(1)"), "");
  assert.equal(sanitizeHttpUrl("JAVASCRIPT:alert(1)", "#"), "#");
  assert.equal(sanitizeHttpUrl("data:text/html,<script>alert(1)</script>"), "");
  assert.equal(sanitizeHttpUrl("//evil.example/x"), "");
  assert.equal(sanitizeHttpUrl("not a url"), "");
});

test("sanitizeHexColor: permits only #RGB and #RRGGBB hex values", () => {
  assert.equal(sanitizeHexColor("#cc0000"), "#cc0000");
  assert.equal(sanitizeHexColor("#ABC"), "#ABC");
  assert.equal(sanitizeHexColor("red; background:url(javascript:alert(1))"), "");
  assert.equal(sanitizeHexColor("#123456; position:fixed", "#94a3b8"), "#94a3b8");
  assert.equal(sanitizeHexColor("rgb(1,2,3)"), "");
});

test("sanitizeCssSlug: strips characters that can break out of class attributes", () => {
  assert.equal(sanitizeCssSlug("under-review"), "under-review");
  assert.equal(sanitizeCssSlug('new" onclick="alert(1)', "new"), "newonclickalert1");
  assert.equal(sanitizeCssSlug("!!!", "pending"), "pending");
});

test("SummaryPane.js escapes formatDate(m.date) in innerHTML", () => {
  const src = fs.readFileSync(
    path.resolve("frontend/src/components/SummaryPane.js"),
    "utf-8"
  );
  assert.equal(src.includes("${escapeHtml(formatDate(m.date))}"), true);
  assert.equal(src.includes(">${formatDate(m.date)}<"), false);
});

test("ViewerPane.js uses sanitizeHttpUrl for Patchwork links and avoids javascript:void(0)", () => {
  const src = fs.readFileSync(
    path.resolve("frontend/src/components/ViewerPane.js"),
    "utf-8"
  );
  assert.equal(src.includes("sanitizeHttpUrl(pw.web_url || pw.url)"), true);
  assert.equal(src.includes("sanitizeHttpUrl(c.target_url)"), true);
  assert.equal(src.includes("sanitizeHttpUrl(sp.web_url)"), true);
  assert.equal(src.includes('href="javascript:void(0)"'), false);
});

test("InternalEditor.js cleans up localStorage entry when window.open is blocked", () => {
  const src = fs.readFileSync(
    path.resolve("frontend/src/components/InternalEditor.js"),
    "utf-8"
  );
  assert.equal(src.includes("localStorage.removeItem(sessionId)"), true);
});
