import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { ENGINE_VERSION } from "../../packages/core/src/version.js";

/**
 * Write an executable shell script that behaves like attest-engine: `version` prints
 * `attest-engine <version>` (an empty version prints nothing), `help` prints a commands
 * section, and any other command echoes its arguments, appends them to `<target>.calls`
 * and exits with `exitCode`.
 */
export function writeScriptedEngine(
  target: string,
  options: { version?: string; exitCode?: number } = {},
): string {
  const version = options.version ?? ENGINE_VERSION;
  const versionCmd = version === "" ? "true" : `echo 'attest-engine ${version}'`;
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.writeFileSync(
    target,
    [
      "#!/bin/sh",
      'case "$1" in',
      `  version) ${versionCmd} ;;`,
      "  help)",
      "    echo 'usage: attest-engine <command> [args]'",
      "    echo 'commands:'",
      "    echo '  doctor  report engine configuration'",
      "    echo '  policy  evaluate a report against a policy' ;;",
      `  *) echo "forwarded: $*"; echo "$*" >> "$0.calls"; exit ${String(options.exitCode ?? 0)} ;;`,
      "esac",
      "",
    ].join("\n"),
    { mode: 0o755 },
  );
  return target;
}

const ENV_KEYS = ["HOME", "PATH", "ATTEST_ENGINE_PATH", "ATTEST_ENGINE_NO_DOWNLOAD"] as const;

/** Isolated HOME, empty PATH directory and download disabled; `restore` undoes it. */
export interface IsolatedEngineEnv {
  root: string;
  home: string;
  pathDir: string;
  restore: () => void;
}

export function isolateEngineEnv(): IsolatedEngineEnv {
  const saved = Object.fromEntries(ENV_KEYS.map((k) => [k, process.env[k]]));
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "attest-engine-env-"));
  const home = path.join(root, "home");
  const pathDir = path.join(root, "path-bin");
  fs.mkdirSync(home);
  fs.mkdirSync(pathDir);
  process.env["HOME"] = home;
  process.env["PATH"] = pathDir;
  process.env["ATTEST_ENGINE_NO_DOWNLOAD"] = "1";
  delete process.env["ATTEST_ENGINE_PATH"];
  return {
    root,
    home,
    pathDir,
    restore: () => {
      for (const key of ENV_KEYS) {
        const value = saved[key];
        if (value === undefined) delete process.env[key];
        else process.env[key] = value;
      }
      fs.rmSync(root, { recursive: true, force: true });
    },
  };
}

export function versionedCachePath(home: string): string {
  return path.join(home, ".attest", "bin", ENGINE_VERSION, "attest-engine");
}

export function legacyCachePath(home: string): string {
  return path.join(home, ".attest", "bin", "attest-engine");
}

/** Argument lines the scripted engine at `target` received for forwarded commands. */
export function forwardedCalls(target: string): string[] {
  const log = `${target}.calls`;
  return fs.existsSync(log) ? fs.readFileSync(log, "utf-8").trim().split("\n") : [];
}
