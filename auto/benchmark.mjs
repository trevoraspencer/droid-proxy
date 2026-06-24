#!/usr/bin/env node
import { spawn } from "node:child_process";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const json = process.argv.includes("--json");

mkdirSync(path.join(root, ".cache", "go-build"), { recursive: true });

const result = await run("go", ["test", "-run", "^$", "-bench", ".", "./...", "-count=1"], {
  GOCACHE: path.join(root, ".cache", "go-build"),
});
const benchmarks = result.stdout
  .split("\n")
  .filter((line) => /^Benchmark\S+\s+\d+/.test(line))
  .map((line) => line.trim());
const summary = {
  command: "go test -run ^$ -bench . ./... -count=1",
  exitCode: result.exitCode,
  benchmarkCount: benchmarks.length,
  benchmarks,
  skipped: result.exitCode === 0 && benchmarks.length === 0,
  stdout: result.stdout,
  stderr: result.stderr,
};

if (json) {
  console.log(JSON.stringify(summary, null, 2));
} else if (summary.skipped) {
  console.log("No Go benchmarks are currently defined.");
} else {
  console.log(benchmarks.join("\n"));
  if (result.stderr.trim()) console.error(result.stderr.trim());
}

process.exit(result.exitCode === 0 ? 0 : result.exitCode);

function run(command, args, extraEnv = {}) {
  return new Promise((resolve) => {
    const child = spawn(command, args, {
      cwd: root,
      env: { ...process.env, ...extraEnv },
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => {
      stdout += String(chunk);
    });
    child.stderr.on("data", (chunk) => {
      stderr += String(chunk);
    });
    child.on("error", (error) => {
      resolve({ exitCode: 127, stdout, stderr: `${stderr}${error.message}` });
    });
    child.on("close", (exitCode) => {
      resolve({ exitCode, stdout, stderr });
    });
  });
}
