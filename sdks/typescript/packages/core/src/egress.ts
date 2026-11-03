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
  return env.ATTEST_EGRESS === "block";
}

type Connect = (this: net.Socket, ...args: unknown[]) => net.Socket;
let original: Connect | null = null;

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

/** Install the guard. Idempotent; returns the uninstall function. */
export function installEgressGuard(): () => void {
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
  return uninstallEgressGuard;
}

export function uninstallEgressGuard(): void {
  if (original === null) return;
  (net.Socket.prototype as unknown as { connect: Connect }).connect = original;
  original = null;
}
