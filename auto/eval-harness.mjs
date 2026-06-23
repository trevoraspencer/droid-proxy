#!/usr/bin/env node
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const args = parseArgs(process.argv.slice(2));
const profile = String(args.profile || process.env.DROID_PROXY_EVAL_PROFILE || "default");
const defaultTimeoutMs = Number(args["timeout-ms"] || process.env.DROID_PROXY_EVAL_TIMEOUT_MS || 180000);
const outputJson = Boolean(args.json);
const writeLast = !args["no-write-last"];
const allowedProfiles = new Set(["fast", "default", "full"]);

if (!allowedProfiles.has(profile)) {
  console.error(`unknown profile ${profile}; expected fast, default, or full`);
  process.exit(2);
}

mkdirSync(path.join(root, ".cache", "go-build"), { recursive: true });

const env = {
  ...process.env,
  GOCACHE: path.join(root, ".cache", "go-build"),
};

const checks = [
  nodeCheck({
    id: "vision-file",
    title: "VISION.md is canonical and present",
    category: "vision",
    weight: 16,
    required: true,
    run: () => {
      const file = path.join(root, "VISION.md");
      if (!existsSync(file)) return fail("VISION.md is missing");
      const text = readFileSync(file, "utf8");
      const required = ["Single Source of Truth", "Droid", "Mandatory Instructions"];
      const missing = required.filter((needle) => !text.includes(needle));
      return missing.length ? fail(`VISION.md missing expected markers: ${missing.join(", ")}`) : pass();
    },
  }),
  nodeCheck({
    id: "vision-immutable",
    title: "VISION.md is not modified by the loop",
    category: "vision",
    weight: 14,
    required: true,
    run: async () => {
      const diff = await capture("git", ["diff", "--name-only", "--", "VISION.md"]);
      return diff.stdout.trim() ? fail("VISION.md has uncommitted modifications") : pass();
    },
  }),
  nodeCheck({
    id: "vision-doc-surface",
    title: "Canonical vision is surfaced in contributor docs",
    category: "vision",
    weight: 10,
    required: false,
    run: () => {
      const docs = ["README.md", "docs/README.md", "CONTRIBUTING.md"];
      const misses = docs.filter((file) => {
        const target = path.join(root, file);
        return !existsSync(target) || !readFileSync(target, "utf8").includes("VISION.md");
      });
      return misses.length ? fail(`VISION.md is not referenced in: ${misses.join(", ")}`) : pass();
    },
  }),
  nodeCheck({
    id: "required-docs-present",
    title: "VISION §14.1 required reading files exist",
    category: "vision",
    weight: 12,
    required: true,
    run: () => {
      const files = [
        "README.md",
        "docs/CONFIG.md",
        "docs/PROVIDERS.md",
        "CONTRIBUTING.md",
        "SECURITY.md",
        "Makefile",
      ];
      const missing = files.filter((file) => !existsSync(path.join(root, file)));
      return missing.length ? fail(`missing required files: ${missing.join(", ")}`) : pass();
    },
  }),
  nodeCheck({
    id: "no-dependency-creep",
    title: "No unapproved dependency creep",
    category: "vision",
    weight: 14,
    required: true,
    run: async () => {
      const pkgPath = path.join(root, "package.json");
      if (existsSync(pkgPath)) {
        const pkg = JSON.parse(readFileSync(pkgPath, "utf8"));
        const depKeys = ["dependencies", "devDependencies", "optionalDependencies", "peerDependencies"];
        const populated = depKeys.filter((key) => pkg[key] && Object.keys(pkg[key]).length > 0);
        if (populated.length) return fail(`package.json has dependency sections: ${populated.join(", ")}`);
      }
      const diff = await capture("git", ["diff", "--name-only", "--", "go.mod", "go.sum"]);
      return diff.stdout.trim() ? fail("go.mod/go.sum changed; dependency changes require explicit approval") : pass();
    },
  }),
  nodeCheck({
    id: "changelog-additive",
    title: "CHANGELOG changes are additive if touched",
    category: "vision",
    weight: 10,
    required: true,
    run: async () => {
      const stat = await capture("git", ["diff", "--numstat", "--", "CHANGELOG.md"]);
      const line = stat.stdout.trim();
      if (!line) return pass("CHANGELOG.md not touched");
      const [added, deleted] = line.split(/\s+/).map((value) => Number(value));
      if (Number.isFinite(deleted) && deleted > 0) return fail("CHANGELOG.md has deletions");
      return Number.isFinite(added) && added > 0 ? pass(`CHANGELOG.md has ${added} additive line(s)`) : pass();
    },
  }),
  nodeCheck({
    id: "scope-creep-diff",
    title: "Dirty diff does not add obvious VISION non-goals",
    category: "vision",
    weight: 14,
    required: true,
    run: async () => {
      const diff = await capture("git", [
        "diff",
        "--",
        ".",
        ":(exclude)auto/**",
        ":(exclude)package.json",
        ":(exclude).gitignore",
      ]);
      const forbidden = /\b(telemetry|web dashboard|multi-user|hosted gateway|plugin system|non-Droid client|0\.0\.0\.0)\b/i;
      const added = diff.stdout.split("\n").filter((line) => line.startsWith("+") && !line.startsWith("+++"));
      const hit = added.find((line) => forbidden.test(line));
      return hit ? fail(`potential VISION non-goal in added diff: ${hit.slice(0, 180)}`) : pass();
    },
  }),
  commandCheck({
    id: "npm-test",
    title: "npm test (go test ./...)",
    category: "tests",
    weight: 58,
    required: true,
    profiles: ["fast", "default", "full"],
    command: "npm",
    args: ["test", "--", "-count=1"],
  }),
  commandCheck({
    id: "build",
    title: "make build",
    category: "robustness",
    weight: 12,
    required: true,
    profiles: ["fast", "default", "full"],
    command: "make",
    args: ["build"],
  }),
  commandCheck({
    id: "lint",
    title: "npm run lint",
    category: "robustness",
    weight: 18,
    required: true,
    profiles: ["default", "full"],
    command: "npm",
    args: ["run", "lint"],
  }),
  commandCheck({
    id: "cli-version",
    title: "CLI smoke: --version",
    category: "tests",
    weight: 10,
    required: true,
    profiles: ["fast", "default", "full"],
    command: "./droid-proxy",
    args: ["--version"],
  }),
  commandCheck({
    id: "cli-auth-help",
    title: "CLI smoke: auth --help",
    category: "tests",
    weight: 8,
    required: true,
    profiles: ["default", "full"],
    command: "./droid-proxy",
    args: ["auth", "--help"],
  }),
  commandCheck({
    id: "cli-service-help",
    title: "CLI smoke: service --help",
    category: "tests",
    weight: 8,
    required: true,
    profiles: ["default", "full"],
    command: "./droid-proxy",
    args: ["service", "--help"],
  }),
  commandCheck({
    id: "docs-audit",
    title: "make docs-audit",
    category: "robustness",
    weight: 16,
    required: true,
    profiles: ["default", "full"],
    command: "make",
    args: ["docs-audit"],
  }),
  commandCheck({
    id: "legal-audit",
    title: "make legal-audit",
    category: "robustness",
    weight: 16,
    required: true,
    profiles: ["default", "full"],
    command: "make",
    args: ["legal-audit"],
  }),
  commandCheck({
    id: "ci-audit",
    title: "make ci-audit",
    category: "robustness",
    weight: 18,
    required: true,
    profiles: ["default", "full"],
    command: "make",
    args: ["ci-audit"],
  }),
  commandCheck({
    id: "test-race",
    title: "make test-race",
    category: "tests",
    weight: 12,
    required: true,
    profiles: ["full"],
    command: "make",
    args: ["test-race"],
    timeoutMs: 300000,
  }),
  commandCheck({
    id: "coverage",
    title: "go test -cover ./...",
    category: "tests",
    weight: 4,
    required: false,
    profiles: ["full"],
    command: "go",
    args: ["test", "-cover", "./..."],
    timeoutMs: 300000,
  }),
  commandCheck({
    id: "benchmark",
    title: "benchmark helper",
    category: "robustness",
    weight: 8,
    required: false,
    profiles: ["full"],
    command: "node",
    args: ["auto/benchmark.mjs", "--json"],
    timeoutMs: 300000,
  }),
  commandCheck({
    id: "audit-secrets",
    title: "make audit-secrets",
    category: "robustness",
    weight: 12,
    required: false,
    profiles: ["full"],
    command: "make",
    args: ["audit-secrets"],
    skipIfMissing: "gitleaks",
  }),
];

const startedAt = new Date();
const results = [];

for (const check of checks) {
  if (!check.profiles.includes(profile)) {
    results.push({ ...baseResult(check), status: "skipped", reason: `not in ${profile} profile`, durationMs: 0 });
    continue;
  }
  if (check.skipIfMissing && !(await hasCommand(check.skipIfMissing))) {
    results.push({ ...baseResult(check), status: "skipped", reason: `${check.skipIfMissing} not installed`, durationMs: 0 });
    continue;
  }
  const started = Date.now();
  try {
    const result = await check.run();
    results.push({ ...baseResult(check), ...result, durationMs: Date.now() - started });
  } catch (error) {
    results.push({
      ...baseResult(check),
      status: "fail",
      reason: error?.stack || String(error),
      durationMs: Date.now() - started,
    });
  }
}

const summary = summarize(results, startedAt);
if (writeLast) {
  writeFileSync(path.join(root, "auto", "eval-last.json"), `${JSON.stringify(summary, null, 2)}\n`);
}

if (outputJson) {
  console.log(JSON.stringify(summary, null, 2));
} else {
  printHuman(summary);
}

process.exit(summary.requiredPass ? 0 : 1);

function commandCheck(def) {
  return {
    ...def,
    kind: "command",
    profiles: def.profiles || ["default", "full"],
    timeoutMs: def.timeoutMs || defaultTimeoutMs,
    run: async () => {
      const result = await runCommand(def.command, def.args || [], {
        timeoutMs: def.timeoutMs || defaultTimeoutMs,
      });
      const output = `${result.stdout}\n${result.stderr}`;
      if (result.timedOut) {
        return { status: "fail", reason: `timed out after ${def.timeoutMs || defaultTimeoutMs}ms`, ...result };
      }
      if (result.exitCode === 0) return { status: "pass", ...result };
      if (looksSandboxBlocked(output)) {
        return { status: "blocked", reason: "environment appears to block sockets/cache/process access", ...result };
      }
      return { status: "fail", reason: `exit ${result.exitCode}`, ...result };
    },
  };
}

function nodeCheck(def) {
  return {
    ...def,
    kind: "node",
    profiles: def.profiles || ["fast", "default", "full"],
  };
}

function pass(reason = "") {
  return { status: "pass", reason };
}

function fail(reason) {
  return { status: "fail", reason };
}

async function capture(command, commandArgs) {
  return runCommand(command, commandArgs, { timeoutMs: 30000, maxOutputBytes: 1_000_000 });
}

async function hasCommand(command) {
  const result = await runCommand("command", ["-v", command], { timeoutMs: 5000, shell: true });
  return result.exitCode === 0;
}

function runCommand(command, commandArgs, options = {}) {
  const timeoutMs = options.timeoutMs || defaultTimeoutMs;
  const maxOutputBytes = options.maxOutputBytes || 200000;
  return new Promise((resolve) => {
    const started = Date.now();
    const child = spawn(command, commandArgs, {
      cwd: root,
      env,
      shell: Boolean(options.shell),
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGTERM");
      setTimeout(() => child.kill("SIGKILL"), 2000).unref();
    }, timeoutMs);
    child.stdout.on("data", (chunk) => {
      stdout = appendCapped(stdout, chunk, maxOutputBytes);
    });
    child.stderr.on("data", (chunk) => {
      stderr = appendCapped(stderr, chunk, maxOutputBytes);
    });
    child.on("error", (error) => {
      clearTimeout(timer);
      resolve({
        command,
        args: commandArgs,
        exitCode: 127,
        stdout,
        stderr: appendCapped(stderr, String(error), maxOutputBytes),
        timedOut,
        elapsedMs: Date.now() - started,
      });
    });
    child.on("close", (exitCode, signal) => {
      clearTimeout(timer);
      resolve({
        command,
        args: commandArgs,
        exitCode,
        signal,
        stdout,
        stderr,
        timedOut,
        elapsedMs: Date.now() - started,
      });
    });
  });
}

function appendCapped(current, chunk, maxBytes) {
  const next = current + String(chunk);
  if (Buffer.byteLength(next) <= maxBytes) return next;
  return `${next.slice(0, Math.floor(maxBytes / 2))}\n[...output truncated...]\n${next.slice(-Math.floor(maxBytes / 2))}`;
}

function looksSandboxBlocked(output) {
  return /operation not permitted|failed to listen on a port|bind: operation not permitted|permission denied|sandbox/i.test(output);
}

function baseResult(check) {
  return {
    id: check.id,
    title: check.title,
    category: check.category,
    weight: check.weight,
    required: Boolean(check.required),
    kind: check.kind,
  };
}

function summarize(results, startedAt) {
  const categories = {};
  for (const category of ["tests", "vision", "robustness"]) {
    const scored = results.filter((result) => result.category === category && result.status !== "skipped");
    const possible = scored.reduce((sum, result) => sum + result.weight, 0);
    const earned = scored.reduce((sum, result) => sum + (result.status === "pass" ? result.weight : 0), 0);
    categories[category] = {
      earned,
      possible,
      score: possible ? round((earned / possible) * 100) : 100,
    };
  }
  const composite = round(
    categories.tests.score * 0.4 +
      categories.vision.score * 0.3 +
      categories.robustness.score * 0.3,
  );
  const required = results.filter((result) => result.required && result.status !== "skipped");
  const requiredPass = required.every((result) => result.status === "pass");
  return {
    schemaVersion: 1,
    repo: root,
    profile,
    startedAt: startedAt.toISOString(),
    finishedAt: new Date().toISOString(),
    composite,
    weights: { tests: 40, vision: 30, robustness: 30 },
    requiredPass,
    categories,
    counts: {
      pass: results.filter((result) => result.status === "pass").length,
      fail: results.filter((result) => result.status === "fail").length,
      blocked: results.filter((result) => result.status === "blocked").length,
      skipped: results.filter((result) => result.status === "skipped").length,
    },
    results,
  };
}

function printHuman(summary) {
  console.log(`autoresearch eval (${summary.profile})`);
  console.log(`composite=${summary.composite} requiredPass=${summary.requiredPass}`);
  for (const [category, value] of Object.entries(summary.categories)) {
    console.log(`${category}: ${value.score} (${value.earned}/${value.possible})`);
  }
  for (const result of summary.results) {
    const mark = result.status === "pass" ? "PASS" : result.status.toUpperCase();
    const suffix = result.reason ? ` - ${result.reason}` : "";
    console.log(`${mark} ${result.id}${suffix}`);
  }
}

function round(value) {
  return Math.round(value * 10) / 10;
}

function parseArgs(argv) {
  const parsed = {};
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (!arg.startsWith("--")) continue;
    const key = arg.slice(2);
    const next = argv[i + 1];
    if (!next || next.startsWith("--")) {
      parsed[key] = true;
    } else {
      parsed[key] = next;
      i += 1;
    }
  }
  return parsed;
}
