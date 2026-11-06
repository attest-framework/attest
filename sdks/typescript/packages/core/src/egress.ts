/**
 * Egress guard: fail in-process connections to known model-provider hosts.
 *
 * Replay serves model responses from a fixture, so a connection to a provider means the
 * agent bypassed the proxy. `installEgressGuard()` patches `net.Socket.prototype.connect`
 * (which every client reaches: undici `fetch`, `http`, `https`, `tls`), refuses provider
 * hostnames and always allows loopback. Calls tunnelled through `HTTP(S)_PROXY` connect only
 * to the proxy, so the guard also reads the `CONNECT host:port` / absolute-URI request line
 * written to a socket and refuses a provider target. The first write of every socket is held
 * until that request line is complete, so splitting it across writes cannot hide the target. Child processes (the engine, anything
 * the agent spawns) are not covered, nor are SOCKS proxies or a TLS-wrapped proxy connection.
 */

import * as net from "node:net";

export const PROVIDER_HOSTS: readonly (string | RegExp)[] = [
  "api.openai.com",
  "api.anthropic.com",
  "generativelanguage.googleapis.com",
  "aiplatform.googleapis.com",
  "api.mistral.ai",
  "api.groq.com",
  "openrouter.ai",
  "api.together.xyz",
  "api.together.ai",
  /\.openai\.azure\.com$/,
  /-aiplatform\.googleapis\.com$/,
  /^bedrock-runtime\.[a-z0-9-]+\.amazonaws\.com$/,
];

// First line a client sends a forward proxy: `CONNECT host:port` or `METHOD http://host/path`.
const PROXY_REQUEST_LINE = /^([A-Z]{3,7}) (\S{1,8192}) HTTP\/1\.[01]\r\n/;

/** The origin host a forward-proxy request line names, or null for any other bytes. */
export function proxiedTargetHost(chunk: unknown): string | null {
  let head: string;
  if (typeof chunk === "string") head = chunk.slice(0, 8200);
  else if (chunk instanceof Uint8Array)
    head = Buffer.from(chunk.subarray(0, 8200)).toString("latin1");
  else return null;
  const m = PROXY_REQUEST_LINE.exec(head);
  if (m === null) return null;
  const target = m[2] as string;
  try {
    if (m[1] === "CONNECT")
      return new URL(`http://${target}`).hostname.replace(/^\[|\]$/g, "");
    return target.includes("://")
      ? new URL(target).hostname.replace(/^\[|\]$/g, "")
      : null;
  } catch {
    return null;
  }
}

export class EgressBlockedError extends Error {
  constructor(
    readonly host: string,
    detail?: string,
  ) {
    super(
      detail ??
        `attest egress guard: connection to model provider ${host} blocked; ` +
          `the agent must reach models through the replay proxy (OPENAI_BASE_URL / ANTHROPIC_BASE_URL)`,
    );
    this.name = "EgressBlockedError";
  }
}

export function isProviderHost(host: string): boolean {
  const h = host.toLowerCase().replace(/\.$/, "");
  return PROVIDER_HOSTS.some((p) =>
    typeof p === "string" ? p === h : p.test(h),
  );
}

/** True when `ATTEST_EGRESS=block`. */
export function egressBlockRequested(
  env: NodeJS.ProcessEnv = process.env,
): boolean {
  const v = (env.ATTEST_EGRESS ?? "").trim().toLowerCase();
  if (v !== "" && v !== "block" && v !== "allow") {
    throw new Error(
      `ATTEST_EGRESS must be block or allow, got ${JSON.stringify(env.ATTEST_EGRESS)}`,
    );
  }
  return v === "block";
}

type Connect = (this: net.Socket, ...args: unknown[]) => net.Socket;
type SinkWrite = (
  this: net.Socket,
  data: unknown,
  encoding: BufferEncoding | "buffer",
  cb: Done,
) => void;
type SinkWritev = (
  this: net.Socket,
  chunks: { chunk: unknown; encoding: BufferEncoding | "buffer" }[],
  cb: Done,
) => void;
type Done = (error?: Error | null) => void;
let original: Connect | null = null;
let originalSinks: { write: SinkWrite; writev: SinkWritev } | null = null;
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
  if (
    typeof first === "number" ||
    (typeof first === "string" && /^\d+$/.test(first))
  ) {
    return typeof args[1] === "string" ? args[1] : null;
  }
  return null;
}

/** Longest first request line a socket may hold back before the guard refuses it. */
const MAX_REQUEST_LINE = 8192;
// Bytes written to a socket before its first request line is complete.
const held = new WeakMap<net.Socket, Buffer>();
// Sockets whose first request line was inspected and allowed; their writes pass untouched.
const cleared = new WeakSet<net.Socket>();
const refused = new WeakMap<net.Socket, EgressBlockedError>();

function toBuffer(data: unknown, encoding: BufferEncoding | "buffer"): Buffer {
  if (typeof data === "string")
    return Buffer.from(data, encoding === "buffer" ? "utf8" : encoding);
  if (data instanceof Uint8Array)
    return Buffer.from(data.buffer, data.byteOffset, data.byteLength);
  throw new TypeError(
    "net.Socket write received a chunk that is neither a string nor bytes",
  );
}

/** true: could still become a request line; false: cannot; null: more bytes needed. */
function couldBeRequestLine(buf: Buffer): boolean | null {
  let end = 0;
  while (
    end < buf.length &&
    (buf[end] as number) >= 0x41 &&
    (buf[end] as number) <= 0x5a
  )
    end++;
  if (end === 0 || end > 7) return false;
  if (end === buf.length) return null;
  return buf[end] === 0x20;
}

/**
 * Decide a socket write. Returns the bytes to forward (everything held so far plus `data`),
 * null while the first request line is incomplete (the bytes are held and reported written),
 * or an EgressBlockedError when the line names a provider or outgrows the bound.
 */
function screen(sock: net.Socket, data: Buffer): Buffer | Error | null {
  const prior = refused.get(sock);
  if (prior !== undefined) return prior;
  if (cleared.has(sock)) return data;
  const previous = held.get(sock);
  const buf = previous === undefined ? data : Buffer.concat([previous, data]);
  const lineEnd = buf.indexOf("\r\n");
  const oversized =
    lineEnd === -1
      ? buf.length > MAX_REQUEST_LINE
      : lineEnd + 2 > MAX_REQUEST_LINE;
  if (oversized) {
    held.delete(sock);
    const err = new EgressBlockedError(
      "request line",
      `attest egress guard: first request line exceeds ${MAX_REQUEST_LINE} bytes, so its target host cannot be checked`,
    );
    refused.set(sock, err);
    return err;
  }
  if (lineEnd === -1 && couldBeRequestLine(buf) !== false) {
    held.set(sock, buf);
    return null;
  }
  held.delete(sock);
  const host = proxiedTargetHost(buf);
  if (host !== null && isProviderHost(host)) {
    const err = new EgressBlockedError(host);
    refused.set(sock, err);
    return err;
  }
  cleared.add(sock);
  return buf;
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
    proto.connect = function (
      this: net.Socket,
      ...args: unknown[]
    ): net.Socket {
      const host = hostOf(args);
      if (host !== null && isProviderHost(host)) {
        const err = new EgressBlockedError(host);
        process.nextTick(() => this.destroy(err));
        return this;
      }
      return orig.apply(this, args);
    };
    const sock = net.Socket.prototype as unknown as {
      _write: SinkWrite;
      _writev: SinkWritev;
    };
    const origSink = sock._write;
    const origSinkV = sock._writev;
    originalSinks = { write: origSink, writev: origSinkV };
    // Every write path (write, end(chunk), cork/uncork, writev) ends in _write or _writev.
    sock._write = function (this: net.Socket, data, encoding, cb): void {
      if (cleared.has(this)) return origSink.call(this, data, encoding, cb);
      const outcome = screen(this, toBuffer(data, encoding));
      if (outcome instanceof Error) return cb(outcome);
      if (outcome === null) return cb();
      return origSink.call(this, outcome, "buffer", cb);
    };
    sock._writev = function (this: net.Socket, chunks, cb): void {
      if (cleared.has(this)) return origSinkV.call(this, chunks, cb);
      const outcome = screen(
        this,
        Buffer.concat(chunks.map((c) => toBuffer(c.chunk, c.encoding))),
      );
      if (outcome instanceof Error) return cb(outcome);
      if (outcome === null) return cb();
      return origSink.call(this, outcome, "buffer", cb);
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
  if (originalSinks !== null) {
    const sock = net.Socket.prototype as unknown as {
      _write: SinkWrite;
      _writev: SinkWritev;
    };
    sock._write = originalSinks.write;
    sock._writev = originalSinks.writev;
    originalSinks = null;
  }
}
