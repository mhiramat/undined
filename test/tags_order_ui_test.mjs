import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";

import { setCachedTags, getCachedTags, sortTagEntries } from "../frontend/src/tags.js";

test("sortTagEntries and setCachedTags order $cl_1..$cl_10 numerically instead of lexicographically", () => {
  // Simulate Go encoding/json lexicographic map key ordering ($cl_1, $cl_10, $cl_2, ...)
  const goJsonTags = {
    "$cl_1": { tag: "P1-Urgent", color: "#FF7B72" },
    "$cl_10": { tag: "Yellow", color: "#FFFF00" },
    "$cl_2": { tag: "P2-High", color: "#FFA657" },
    "$cl_3": { tag: "P3-Normal", color: "#3FB950" },
    "$cl_4": { tag: "Needs-Ack", color: "#79C0FF" },
    "$cl_5": { tag: "Purple", color: "#6600CC" },
    "$cl_9": { tag: "Light blue", color: "#33CCFF" },
    "$label1": { tag: "Important", color: "#FF2600" },
    "$label2": { tag: "Work", color: "#FF9900" },
    "custom-b": { tag: "Custom B", color: "#112233" },
    "custom-a": { tag: "Custom A", color: "#445566" },
  };

  const expectedOrder = [
    "$cl_1",
    "$cl_2",
    "$cl_3",
    "$cl_4",
    "$cl_5",
    "$cl_9",
    "$cl_10",
    "$label1",
    "$label2",
    "custom-a",
    "custom-b",
  ];

  const sortedKeys = sortTagEntries(goJsonTags).map(([k]) => k);
  assert.deepEqual(sortedKeys, expectedOrder);

  setCachedTags(goJsonTags);
  assert.deepEqual(Object.keys(getCachedTags()), expectedOrder);
});

test("WebUI tag dropdowns and filter dialog use sortTagEntries", () => {
  for (const relPath of [
    "frontend/src/components/ViewerPane.js",
    "frontend/src/components/SummaryPane.js",
    "frontend/src/components/FilterDialog.js",
  ]) {
    const src = fs.readFileSync(path.resolve(relPath), "utf-8");
    assert.equal(src.includes("sortTagEntries("), true, `${relPath} must use sortTagEntries`);
  }
});
