/**
 * Egress guard: fail in-process connections to known model-provider hosts.
 *
 * Replay serves model responses from a fixture, so a connection to a provider means the
 * agent bypassed the proxy. `installEgressGuard()` patches `net.Socket.prototype.connect`
 * (which every client reaches: undici `fetch`, `http`, `https`, `tls`), refuses provider
 * hostnames and always allows loopback.
 */

import * as net from "node:net";

export const PROVIDER_HOSTS: readonly (string | RegExp)[] = [
  "api.openai.com",
  "api.anthropic.com",
  "generativelanguage.googleapis.com",
  /\.openai\.azure\.com$/,
];

export class EgressBlockedError extends Error {
  constructor(readonly host: string) {
    super(
      `attest egress guard: connection to model provider ${host} blocked; ` +
        `the agent must reach models through the replay proxy (OPENAI_BASE_URL / ANTHROPIC_BASE_URL)`,
    );
    this.name = "EgressBlockedError";
  }
}

export function isProviderHost(host: string): boolean {
  const h = host.toLowerCase().replace(/\.$/, "");
  return PROVIDER_HOSTS.some((p) => (typeof p === "string" ? p === h : p.test(h)));
}

/** True when `ATTEST_EGRESS=block`. */
export function egressBlockRequested(env: NodeJS.ProcessEnv = process.env): boolean {
  const v = (env.ATTEST_EGRESS ?? "").trim().toLowerCase();
  if (v !== "" && v !== "block" && v !== "allow") {
    throw new Error(`ATTEST_EGRESS must be block or allow, got ${JSON.stringify(env.ATTEST_EGRESS)}`);
  }
  return v === "block";
}

type Connect = (this: net.Socket, ...args: unknown[]) => net.Socket;
let original: Connect | null = null;
// Installs outstanding; the guard comes off when the last one is released.
let installs = 0;

function hostOf(args: unknown[]): string | null {
  // net.createConnection hands connect() its arguments already normalized: [options, cb].
  const first = Array.isArray(args[0]) ? (args[0] as unknown[])[0] : args[0];
  if (typeof first === "object" && first !== null) {
    const o = first as { host?: unknown; path?: unknown };
    return typeof o.host === "string" ? o.host : null;
  }
  // connect(port, host?) or connect(path)
  if (typeof first === "number" || (typeof first === "string" && /^\d+$/.test(first))) {
    return typeof args[1] === "string" ? args[1] : null;
  }
  return null;
}

/**
 * Install the guard. Installs are counted: the returned function releases only this
 * install (once), and the guard comes off when every install has been released.
 */
export function installEgressGuard(): () => void {
  installs++;
  if (original === null) {
    const proto = net.Socket.prototype as unknown as { connect: Connect };
    const orig = proto.connect;
    original = orig;
    proto.connect = function (this: net.Socket, ...args: unknown[]): net.Socket {
      const host = hostOf(args);
      if (host !== null && isProviderHost(host)) {
        const err = new EgressBlockedError(host);
        process.nextTick(() => this.destroy(err));
        return this;
      }
      return orig.apply(this, args);
    };
  }
  let released = false;
  return () => {
    if (released) return;
    released = true;
    installs--;
    if (installs === 0) uninstallEgressGuard();
  };
}

/** True while an egress guard is installed in this process. */
export function egressGuardActive(): boolean {
  return installs > 0;
}

/** Remove the guard now, whatever the install count. */
export function uninstallEgressGuard(): void {
  installs = 0;
  if (original === null) return;
  (net.Socket.prototype as unknown as { connect: Connect }).connect = original;
  original = null;
}
