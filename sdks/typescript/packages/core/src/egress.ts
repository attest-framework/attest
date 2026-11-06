/**
 * Egress guard: fail in-process connections to known model-provider hosts.
 *
 * Replay serves model responses from a fixture, so a connection to a provider means the
 * agent bypassed the proxy. `installEgressGuard()` patches `net.Socket.prototype.connect`
 * (which every client reaches: undici `fetch`, `http`, `https`, `tls`), refuses provider
 * hostnames and always allows loopback. Calls tunnelled through `HTTP(S)_PROXY` connect only
 * to the proxy, so a socket whose peer is a configured plain-HTTP proxy (host:port from
 * `HTTPS_PROXY`, `HTTP_PROXY`, `ALL_PROXY`, either case, read at connect time) is screened:
 * its `_write`/`_writev` sinks (write, end, cork, writev) run every HTTP/1.1 request through a
 * minimal framer that checks the request-line target and `Host` header, skips bodies by
 * `Content-Length` or chunked framing. Ambiguous framing is refused. No other socket is
 * touched (stdout/stderr pipes, sockets to any other peer).
 *
 * A socket is a proxy when its connected `remoteAddress`/`remotePort` match the proxy host's
 * `dns.lookup(all)` addresses (IPv4-mapped IPv6 normalized, cached per proxy URL; a peer
 * address missing from the cache triggers a fresh lookup that replaces it), so
 * `localhost.` or a multi-address name match a pre-resolved connect. The decision is taken on
 * `connect` and the socket's writes are held until then. A failed lookup on a port a proxy is
 * configured on counts as a proxy (fail closed).
 *
 * After an allowed `CONNECT` the guard goes opaque on a three-byte TLS record prefix (0x16
 * 0x03 0x00-0x04); the tunnel itself is not verified. Anything else (the proxy refused the
 * CONNECT, e.g. 407, and the client sent another request, or the tunnel carries plain HTTP)
 * returns to request framing with full screening. A client that forges that prefix and then
 * writes plaintext is not inspected. The proxy's response is not read, so a non-HTTP,
 * non-TLS protocol inside a tunnel is refused. This is a best-effort guard for trusted test
 * code, not an isolation boundary.
 *
 * Not covered: child processes (the engine, anything the agent spawns), SOCKS proxies, an
 * `https://` proxy (TLS hides the request line), a proxy named only through a client option
 * rather than the environment, and the contents of a tunnel after the TLS record prefix.
 */

import * as dns from "node:dns";
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
type Done = (error?: Error | null) => void;
type Encoding = BufferEncoding | "buffer";
type SinkWrite = (
  this: net.Socket,
  data: unknown,
  encoding: Encoding,
  cb: Done,
) => void;
type SinkWritev = (
  this: net.Socket,
  chunks: { chunk: unknown; encoding: Encoding }[],
  cb: Done,
) => void;
type SocketSinks = { _write: SinkWrite; _writev: SinkWritev };

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
  if (
    typeof first === "number" ||
    (typeof first === "string" && /^\d+$/.test(first))
  ) {
    return typeof args[1] === "string" ? args[1] : null;
  }
  return null;
}

function portOf(args: unknown[]): number | null {
  const first = Array.isArray(args[0]) ? (args[0] as unknown[])[0] : args[0];
  const raw =
    typeof first === "object" && first !== null
      ? (first as { port?: unknown }).port
      : first;
  const port = typeof raw === "string" ? Number(raw) : raw;
  return typeof port === "number" && Number.isInteger(port) ? port : null;
}

/** Plain-HTTP proxies named by the environment, read at call time. */
function configuredProxies(
  env: NodeJS.ProcessEnv = process.env,
): { host: string; port: number }[] {
  const out: { host: string; port: number }[] = [];
  for (const name of [
    "HTTPS_PROXY",
    "https_proxy",
    "HTTP_PROXY",
    "http_proxy",
    "ALL_PROXY",
    "all_proxy",
  ]) {
    const value = env[name]?.trim();
    if (!value) continue;
    let url: URL;
    try {
      url = new URL(value.includes("://") ? value : `http://${value}`);
    } catch {
      continue;
    }
    // https:// proxies are TLS-wrapped (request line hidden) and socks:// is not HTTP.
    if (url.protocol !== "http:") continue;
    out.push({
      host: url.hostname.replace(/^\[|\]$/g, "").toLowerCase(),
      port: url.port === "" ? 80 : Number(url.port),
    });
  }
  return out;
}

/** Ports at least one configured proxy listens on. */
function isProxyPortCandidate(args: unknown[]): boolean {
  const port = portOf(args);
  return port !== null && configuredProxies().some((p) => p.port === port);
}

function normalizeAddress(address: string): string {
  const bare = address.replace(/^\[|\]$/g, "").split("%")[0]!.toLowerCase();
  const mapped = /^::ffff:(\d+\.\d+\.\d+\.\d+)$/.exec(bare);
  return mapped === null ? bare : (mapped[1] as string);
}

// Resolved addresses per proxy URL; failures are not cached.
const proxyAddresses = new Map<string, Set<string>>();

/** Fresh `dns.lookup(all)` answer for a proxy host, or null when the lookup fails. */
async function lookupProxy(host: string): Promise<Set<string> | null> {
  try {
    const infos = await dns.promises.lookup(host, { all: true });
    return new Set(infos.map((i) => normalizeAddress(i.address)));
  } catch {
    return null;
  }
}

/**
 * Whether `peer` is an address of the proxy. A cached hit answers at once; a miss bypasses
 * the cache, replaces it with the fresh answer and decides on that (a rotated proxy address
 * must not be classified as a non-proxy). A failed lookup counts as a proxy (fail closed).
 */
async function isProxyAddress(p: { host: string; port: number }, peer: string): Promise<boolean> {
  const key = `http://${p.host}:${p.port}`;
  if (proxyAddresses.get(key)?.has(peer) === true) return true;
  const fresh = await lookupProxy(p.host);
  if (fresh === null) return true;
  proxyAddresses.set(key, fresh);
  return fresh.has(peer);
}

/** Whether a connected socket's peer is a configured proxy (fail closed on a lookup error). */
async function isProxyPeer(sock: net.Socket): Promise<boolean> {
  const { remoteAddress, remotePort } = sock;
  if (remoteAddress === undefined || remotePort === undefined) return false;
  const peer = normalizeAddress(remoteAddress);
  for (const p of configuredProxies().filter((c) => c.port === remotePort)) {
    if (await isProxyAddress(p, peer)) return true;
  }
  return false;
}

/** Settles on `connect` with the proxy verdict; false if the socket closes first. */
function decideProxy(sock: net.Socket): Promise<boolean> {
  return new Promise<boolean>((resolve) => {
    sock.once("connect", () => {
      isProxyPeer(sock).then(resolve, () => resolve(true));
    });
    sock.once("close", () => resolve(false));
  });
}

/** Longest request head, chunk-size line or trailer line the framer will hold. */
const MAX_HEAD = 64 * 1024;
const TOKEN_HEADER = /^([!#$%&'*+.^_`|~0-9A-Za-z-]+):[ \t]*(.*?)[ \t]*$/;
const REQUEST_LINE = /^([A-Z]{3,7}) (\S+) HTTP\/1\.[01]$/;

function refuse(detail: string): never {
  throw new EgressBlockedError("proxy request", `attest egress guard: ${detail}`);
}

function hostnameOf(authority: string): string | null {
  try {
    return new URL(`http://${authority}`).hostname.replace(/^\[|\]$/g, "");
  } catch {
    return null;
  }
}

type FramerState =
  | "head"
  | "body"
  | "chunk-size"
  | "chunk-data"
  | "chunk-end"
  | "trailer"
  | "await-tunnel"
  | "tunnel";

/** Follows the HTTP/1.1 requests written to one proxy connection and screens each head. */
class RequestFramer {
  private state: FramerState = "head";
  private remaining = 0;
  private held: Buffer = Buffer.alloc(0);

  /** Bytes safe to forward for `data`; throws EgressBlockedError for a blocked request. */
  feed(data: Buffer): Buffer {
    const buf = this.held.length === 0 ? data : Buffer.concat([this.held, data]);
    this.held = Buffer.alloc(0);
    const out: Buffer[] = [];
    let pos = 0;
    while (pos < buf.length) {
      if (this.state === "tunnel") {
        out.push(buf.subarray(pos));
        pos = buf.length;
      } else if (this.state === "await-tunnel") {
        const startsTls = tlsRecordStart(buf.subarray(pos));
        if (startsTls === null) break;
        this.state = startsTls ? "tunnel" : "head";
      } else if (this.state === "body" || this.state === "chunk-data") {
        const n = Math.min(this.remaining, buf.length - pos);
        out.push(buf.subarray(pos, pos + n));
        pos += n;
        this.remaining -= n;
        if (this.remaining === 0)
          this.state = this.state === "body" ? "head" : "chunk-end";
      } else if (this.state === "chunk-end") {
        if (buf.length - pos < 2) break;
        if (buf[pos] !== 0x0d || buf[pos + 1] !== 0x0a)
          refuse("chunk data is not followed by CRLF");
        out.push(buf.subarray(pos, pos + 2));
        pos += 2;
        this.state = "chunk-size";
      } else if (this.state === "head") {
        const end = buf.indexOf("\r\n\r\n", pos);
        if (end === -1) {
          const rest = buf.subarray(pos);
          if (couldBeRequestLine(rest) === false)
            refuse("a proxy connection carried bytes that are not an HTTP request");
          if (rest.length > MAX_HEAD)
            refuse(`request head exceeds ${MAX_HEAD} bytes, so it cannot be checked`);
          break;
        }
        if (end + 4 - pos > MAX_HEAD)
          refuse(`request head exceeds ${MAX_HEAD} bytes, so it cannot be checked`);
        const head = buf.subarray(pos, end + 4);
        this.state = this.screenHead(head.toString("latin1"));
        out.push(head);
        pos = end + 4;
      } else {
        // chunk-size and trailer are CRLF-terminated lines.
        const end = buf.indexOf("\r\n", pos);
        if (end === -1) {
          if (buf.length - pos > MAX_HEAD)
            refuse("chunk framing line is too long to check");
          break;
        }
        this.screenLine(buf.subarray(pos, end).toString("latin1"));
        out.push(buf.subarray(pos, end + 2));
        pos = end + 2;
      }
    }
    if (pos < buf.length) this.held = Buffer.from(buf.subarray(pos));
    return Buffer.concat(out);
  }

  private screenLine(line: string): void {
    if (this.state === "chunk-size") {
      const size = (line.split(";", 1)[0] as string).trim();
      if (!/^[0-9a-fA-F]{1,12}$/.test(size)) refuse("invalid chunk size");
      this.remaining = parseInt(size, 16);
      this.state = this.remaining === 0 ? "trailer" : "chunk-data";
    } else if (line === "") {
      this.state = "head";
    } else if (!TOKEN_HEADER.test(line)) {
      refuse("malformed trailer line");
    }
  }

  private screenHead(text: string): FramerState {
    const lines = text.slice(0, -4).split("\r\n");
    const m = REQUEST_LINE.exec(lines[0] as string);
    if (m === null) refuse("malformed request line");
    const method = m[1] as string;
    const target = m[2] as string;
    const names = new Map<string, string[]>();
    for (const line of lines.slice(1)) {
      const h = TOKEN_HEADER.exec(line);
      if (h === null) refuse("malformed header line");
      const key = (h[1] as string).toLowerCase();
      names.set(key, [...(names.get(key) ?? []), h[2] as string]);
    }
    const hosts: (string | null)[] = [];
    if (method === "CONNECT") hosts.push(hostnameOf(target));
    else if (target.includes("://")) {
      try {
        hosts.push(new URL(target).hostname.replace(/^\[|\]$/g, ""));
      } catch {
        hosts.push(null);
      }
    }
    for (const v of names.get("host") ?? []) hosts.push(hostnameOf(v));
    for (const h of hosts) {
      if (h === null) refuse("request target or Host header cannot be parsed");
      if (isProviderHost(h)) throw new EgressBlockedError(h);
    }
    if (method === "CONNECT") return "await-tunnel";
    const lengths = names.get("content-length") ?? [];
    const encodings = names.get("transfer-encoding") ?? [];
    if (encodings.length > 0) {
      if (lengths.length > 0)
        refuse("request has both Content-Length and Transfer-Encoding");
      const last = (encodings.join(",").split(",").pop() as string)
        .trim()
        .toLowerCase();
      if (last !== "chunked") refuse("Transfer-Encoding does not end in chunked");
      return "chunk-size";
    }
    if (lengths.length > 1) refuse("request has several Content-Length headers");
    if (lengths.length === 1) {
      const raw = lengths[0] as string;
      if (!/^\d{1,15}$/.test(raw)) refuse("invalid Content-Length");
      this.remaining = Number(raw);
      return this.remaining > 0 ? "body" : "head";
    }
    return "head";
  }
}

/** true: a TLS record header (handshake/alert/app-data, SSL3 to TLS1.3); false: not; null: more bytes needed. */
function tlsRecordStart(buf: Buffer): boolean | null {
  const want = [0x16, 0x03];
  for (let i = 0; i < Math.min(buf.length, 3); i++) {
    const b = buf[i] as number;
    if (i < 2 ? b !== want[i] : b > 0x04) return false;
  }
  return buf.length < 3 ? null : true;
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

function toBuffer(data: unknown, encoding: Encoding): Buffer {
  if (typeof data === "string")
    return Buffer.from(data, encoding === "buffer" ? "utf8" : encoding);
  if (data instanceof Uint8Array)
    return Buffer.from(data.buffer, data.byteOffset, data.byteLength);
  throw new TypeError(
    "net.Socket write received a chunk that is neither a string nor bytes",
  );
}

// Proxy sockets carrying per-instance sink patches, so uninstall can remove them.
const screened = new Set<WeakRef<net.Socket>>();
const refused = new WeakMap<net.Socket, Error>();

function screenSocket(sock: net.Socket, isProxy: Promise<boolean>): void {
  const sinks = sock as unknown as SocketSinks;
  const framer = new RequestFramer();
  const origSink = sinks._write;
  const origSinkV = sinks._writev;

  const decide = (data: Buffer): Buffer | Error => {
    const prior = refused.get(sock);
    if (prior !== undefined) return prior;
    try {
      return framer.feed(data);
    } catch (e) {
      if (!(e instanceof EgressBlockedError)) throw e;
      refused.set(sock, e);
      return e;
    }
  };
  // Every write path (write, end(chunk), cork/uncork, writev) ends in _write or _writev.
  // Writable issues one sink call at a time (the next waits for `cb`), so holding a call
  // until the proxy verdict keeps write order.
  sinks._write = function (this: net.Socket, data, encoding, cb): void {
    void isProxy.then((proxied) => {
      if (!proxied) return origSink.call(this, data, encoding, cb);
      const outcome = decide(toBuffer(data, encoding));
      if (outcome instanceof Error) return cb(outcome);
      if (outcome.length === 0) return cb();
      return origSink.call(this, outcome, "buffer", cb);
    });
  };
  sinks._writev = function (this: net.Socket, chunks, cb): void {
    void isProxy.then((proxied) => {
      if (!proxied) return origSinkV.call(this, chunks, cb);
      const outcome = decide(
        Buffer.concat(chunks.map((c) => toBuffer(c.chunk, c.encoding))),
      );
      if (outcome instanceof Error) return cb(outcome);
      if (outcome.length === 0) return cb();
      return origSink.call(this, outcome, "buffer", cb);
    });
  };
  const ref = new WeakRef(sock);
  screened.add(ref);
  sock.once("close", () => screened.delete(ref));
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
      // Patched now, so no write can precede the patch; the sinks hold writes until the
      // connected peer is identified.
      if (isProxyPortCandidate(args)) screenSocket(this, decideProxy(this));
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
  for (const ref of screened) {
    const sock = ref.deref();
    if (sock === undefined) continue;
    // Drop the per-instance patch: the prototype sinks apply again.
    delete (sock as Partial<SocketSinks>)._write;
    delete (sock as Partial<SocketSinks>)._writev;
  }
  screened.clear();
  proxyAddresses.clear();
}
