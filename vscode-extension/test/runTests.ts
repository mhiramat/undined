import Mocha from "mocha";
import * as path from "path";
import * as fs from "fs";
import * as Module from "module";
import { mockVscode } from "./vscodeMock";

// Intercept require('vscode') for unit tests running outside Electron
const originalRequire = (Module.prototype as any).require;
(Module.prototype as any).require = function (id: string) {
  if (id === "vscode") {
    return mockVscode;
  }
  return originalRequire.apply(this, arguments);
};

function run(): Promise<void> {
  const mocha = new Mocha({
    ui: "bdd",
    color: true,
    timeout: 10000,
  });

  const testsRoot = path.resolve(__dirname, "suite");

  const files = fs.readdirSync(testsRoot).filter((f) => f.endsWith(".test.js"));
  for (const f of files) {
    mocha.addFile(path.resolve(testsRoot, f));
  }

  return new Promise((resolve, reject) => {
    try {
      mocha.run((failures) => {
        if (failures > 0) {
          reject(new Error(`${failures} tests failed.`));
        } else {
          resolve();
        }
      });
    } catch (err) {
      reject(err);
    }
  });
}

run()
  .then(() => {
    console.log("All unit tests passed successfully.");
    process.exit(0);
  })
  .catch((err) => {
    console.error("Test execution failed:", err);
    process.exit(1);
  });
