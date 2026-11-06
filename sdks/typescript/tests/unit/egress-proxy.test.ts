import { execFile } from "node:child_process";
import * as fs from "node:fs";
import * as dns from "node:dns";
import * as http from "node:http";
import * as net from "node:net";
import { createRequire } from "node:module";
import * as os from "node:os";
import * as path from "node:path";
import * as tls from "node:tls";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  EgressBlockedError,
  installEgressGuard,
  isProviderHost,
  proxiedTargetHost,
  uninstallEgressGuard,
} from "../../packages/core/src/egress.js";

/** Loopback forward proxy that records request lines and never forwards anything. */
async function startProxy(): Promise<{
  port: number;
  lines: string[];
  raw: Buffer[];
  close: () => void;
}> {
  const lines: string[] = [];
  const raw: Buffer[] = [];
  const sockets = new Set<net.Socket>();
  const server = net.createServer((sock) => {
    sockets.add(sock);
    sock.on("error", () => undefined);
    let received = Buffer.alloc(0);
    sock.on("data", (d: Buffer) => {
      received = Buffer.concat([received, d]);
      if (!received.includes("\r\n\r\n")) return;
      raw.push(received);
      lines.push(received.toString("latin1").split("\r\n", 1)[0] as string);
      sock.end("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n");
    });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  // The guard screens only the configured proxy, so name it the way an env-aware client does.
  const port = (server.address() as net.AddressInfo).port;
  process.env.HTTP_PROXY = `http://127.0.0.1:${port}`;
  return {
    port,
    lines,
    raw,
    close: () => {
      for (const s of sockets) s.destroy();
      server.close();
    },
  };
}

const tick = (ms = 10) => new Promise<void>((r) => setTimeout(r, ms));

/** Write `fragments` to the proxy as separate writes; resolve with the socket error, if any. */
async function writeFragments(
  port: number,
  fragments: Buffer[],
  how: "separate" | "corked" = "separate",
): Promise<Error | null> {
  const sock = net.connect({ host: "127.0.0.1", port });
  let failure: Error | null = null;
  sock.on("error", (e) => {
    failure = e;
  });
  sock.on("data", () => undefined);
  await new Promise<void>((r) => sock.once("connect", r));
  if (how === "corked") sock.cork();
  for (const f of fragments) {
    sock.write(f);
    if (how === "separate") await tick(5);
  }
  if (how === "corked") sock.uncork();
  await tick(150);
  sock.destroy();
  return failure;
}

const PROVIDER_CONNECT = Buffer.from(
  "CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\n\r\n",
);

function viaProxy(
  port: number,
  opts: http.RequestOptions,
): Promise<Error | http.IncomingMessage> {
  return new Promise((resolve) => {
    const req = http.request({ host: "127.0.0.1", port, ...opts });
    req.on("error", resolve);
    req.on("response", resolve);
    req.on("connect", (res) => resolve(res));
    req.end();
  });
}

afterEach(() => {
  vi.restoreAllMocks();
  uninstallEgressGuard();
  delete process.env.HTTP_PROXY;
});

describe("egress guard behind an HTTP proxy", () => {

  it("refuses a CONNECT to a provider host before any byte reaches the proxy", async () => {
    const proxy = await startProxy();
    installEgressGuard();
    try {
      const out = await viaProxy(proxy.port, {
        method: "CONNECT",
        path: "api.openai.com:443",
      });
      expect(out).toBeInstanceOf(EgressBlockedError);
      expect((out as EgressBlockedError).host).toBe("api.openai.com");
      expect(proxy.lines).toEqual([]);
    } finally {
      proxy.close();
    }
  });

  it("refuses an absolute-URI request for a provider host", async () => {
    const proxy = await startProxy();
    installEgressGuard();
    try {
      const out = await viaProxy(proxy.port, {
        path: "http://api.groq.com/openai/v1/models",
      });
      expect(out).toBeInstanceOf(EgressBlockedError);
      expect(proxy.lines).toEqual([]);
    } finally {
      proxy.close();
    }
  });

  it("lets a proxied request to a non-provider host through", async () => {
    const proxy = await startProxy();
    installEgressGuard();
    try {
      const out = await viaProxy(proxy.port, { path: "http://example.com/" });
      expect((out as http.IncomingMessage).statusCode).toBe(502);
      expect(proxy.lines).toEqual(["GET http://example.com/ HTTP/1.1"]);
    } finally {
      proxy.close();
    }
  });

  it("restores the Socket write sinks on uninstall", () => {
    const proto = net.Socket.prototype as unknown as Record<string, unknown>;
    const before = [proto._write, proto._writev];
    installEgressGuard()();
    expect([proto._write, proto._writev]).toEqual(before);
  });

  describe("fragmented request lines", () => {
    async function blocked(fragments: Buffer[], how?: "separate" | "corked") {
      const proxy = await startProxy();
      installEgressGuard();
      try {
        const err = await writeFragments(proxy.port, fragments, how);
        return { err, raw: proxy.raw };
      } finally {
        proxy.close();
      }
    }

    it("blocks a CONNECT split as CON + rest before the proxy sees it", async () => {
      const { err, raw } = await blocked([
        PROVIDER_CONNECT.subarray(0, 3),
        PROVIDER_CONNECT.subarray(3),
      ]);
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(raw).toEqual([]);
    });

    it("blocks a CONNECT written one byte at a time", async () => {
      const bytes = Array.from(PROVIDER_CONNECT, (_, i) =>
        PROVIDER_CONNECT.subarray(i, i + 1),
      );
      const { err, raw } = await blocked(bytes);
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(raw).toEqual([]);
    });

    it("blocks a split CONNECT written through cork/uncork (writev)", async () => {
      const { err, raw } = await blocked(
        [PROVIDER_CONNECT.subarray(0, 3), PROVIDER_CONNECT.subarray(3)],
        "corked",
      );
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(raw).toEqual([]);
    });

    it("blocks a split absolute-form GET to a provider", async () => {
      const req = Buffer.from(
        "GET http://api.openai.com/v1/models HTTP/1.1\r\nHost: api.openai.com\r\n\r\n",
      );
      const { err, raw } = await blocked([
        req.subarray(0, 5),
        req.subarray(5, 30),
        req.subarray(30),
      ]);
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(raw).toEqual([]);
    });

    it("blocks a provider request line longer than the old 2 KiB parse window", async () => {
      const req = Buffer.from(
        `GET http://api.openai.com/${"a".repeat(3000)} HTTP/1.1\r\n\r\n`,
      );
      const { err, raw } = await blocked([req]);
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(raw).toEqual([]);
    });

    it("refuses a request line that outgrows the 64 KiB head bound", async () => {
      const { err, raw } = await blocked([
        Buffer.from(`GET http://example.com/${"a".repeat(70000)}`),
      ]);
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(raw).toEqual([]);
    });

    it("delivers a split CONNECT to a non-provider host byte-identical", async () => {
      const req = Buffer.from(
        "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n",
      );
      const { err, raw } = await blocked([
        req.subarray(0, 3),
        req.subarray(3, 10),
        req.subarray(10),
      ]);
      expect(err).toBeNull();
      expect(raw).toEqual([req]);
    });
  });

  it("keeps the direct block when NO_PROXY exempts a provider from the proxy", async () => {
    const proxy = await startProxy();
    installEgressGuard();
    const env = {
      HTTPS_PROXY: `http://127.0.0.1:${proxy.port}`,
      NO_PROXY: "api.openai.com",
    };
    // Same routing rule an env-aware client applies: NO_PROXY hosts connect directly.
    const direct = env.NO_PROXY.split(",").includes("api.openai.com");
    try {
      expect(direct).toBe(true);
      const sock = net.connect({ host: "api.openai.com", port: 443 });
      const err = await new Promise<Error>((resolve) =>
        sock.once("error", resolve),
      );
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(proxy.raw).toEqual([]);
    } finally {
      proxy.close();
    }
  });

  it("covers the additional providers", () => {
    for (const h of [
      "bedrock-runtime.us-east-1.amazonaws.com",
      "us-central1-aiplatform.googleapis.com",
      "aiplatform.googleapis.com",
      "acme.openai.azure.com",
      "api.mistral.ai",
      "api.groq.com",
      "openrouter.ai",
      "api.together.xyz",
    ]) {
      expect(isProviderHost(h), h).toBe(true);
    }
    for (const h of [
      "s3.us-east-1.amazonaws.com",
      "bedrock.us-east-1.amazonaws.com",
      "googleapis.com",
    ]) {
      expect(isProviderHost(h), h).toBe(false);
    }
  });

  it("parses proxy request lines", () => {
    expect(
      proxiedTargetHost(
        Buffer.from("CONNECT api.openai.com:443 HTTP/1.1\r\n\r\n"),
      ),
    ).toBe("api.openai.com");
    expect(proxiedTargetHost("POST http://openrouter.ai/x HTTP/1.1\r\n")).toBe(
      "openrouter.ai",
    );
    expect(
      proxiedTargetHost(Buffer.from("GET /v1/models HTTP/1.1\r\n")),
    ).toBeNull();
    expect(proxiedTargetHost(Buffer.from([0x16, 0x03, 0x01]))).toBeNull();
  });
});

/** Loopback proxy that records every byte per connection and answers nothing. */
async function startSink(): Promise<{
  port: number;
  received: () => Buffer;
  close: () => void;
}> {
  const chunks: Buffer[] = [];
  const sockets = new Set<net.Socket>();
  const server = net.createServer((sock) => {
    sockets.add(sock);
    sock.on("error", () => undefined);
    sock.on("data", (d: Buffer) => chunks.push(d));
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const port = (server.address() as net.AddressInfo).port;
  process.env.HTTP_PROXY = `http://127.0.0.1:${port}`;
  return {
    port,
    received: () => Buffer.concat(chunks),
    close: () => {
      for (const s of sockets) s.destroy();
      server.close();
    },
  };
}

/** Write each part to a fresh proxy connection as its own write; return the first error. */
async function sendParts(port: number, parts: (Buffer | string)[]) {
  const sock = net.connect({ host: "127.0.0.1", port });
  let failure: Error | null = null;
  sock.on("error", (e) => {
    failure = e;
  });
  await new Promise<void>((r) => sock.once("connect", r));
  for (const part of parts) {
    sock.write(part);
    await tick(20);
  }
  await tick(100);
  sock.destroy();
  return failure as Error | null;
}

const ALLOWED = "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n";
const PROVIDER =
  "GET http://api.openai.com/v1/models HTTP/1.1\r\nHost: api.openai.com\r\n\r\n";

describe("egress guard frames every request on a proxy connection", () => {
  it("blocks a provider request pipelined behind an allowed one in one write", async () => {
    const sink = await startSink();
    installEgressGuard();
    try {
      const err = await sendParts(sink.port, [ALLOWED + PROVIDER]);
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(sink.received().length).toBe(0);
    } finally {
      sink.close();
    }
  });

  it("blocks a keep-alive provider request written after an allowed one", async () => {
    const sink = await startSink();
    installEgressGuard();
    try {
      const err = await sendParts(sink.port, [ALLOWED, PROVIDER]);
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(sink.received().toString("latin1")).toBe(ALLOWED);
    } finally {
      sink.close();
    }
  });

  it("passes a pipelined pair of allowed requests byte-identical", async () => {
    const sink = await startSink();
    installEgressGuard();
    try {
      const err = await sendParts(sink.port, [ALLOWED + ALLOWED, ALLOWED]);
      expect(err).toBeNull();
      expect(sink.received().toString("latin1")).toBe(ALLOWED.repeat(3));
    } finally {
      sink.close();
    }
  });

  it("does not screen a Content-Length body that quotes a provider request", async () => {
    const sink = await startSink();
    installEgressGuard();
    const body = "GET http://api.openai.com/ HTTP/1.1\r\n\r\n";
    const request =
      `POST http://example.com/ HTTP/1.1\r\nHost: example.com\r\n` +
      `Content-Length: ${body.length}\r\n\r\n${body}`;
    try {
      const err = await sendParts(sink.port, [request, ALLOWED]);
      expect(err).toBeNull();
      expect(sink.received().toString("latin1")).toBe(request + ALLOWED);
    } finally {
      sink.close();
    }
  });

  it("blocks a provider request that follows a chunked body", async () => {
    const sink = await startSink();
    installEgressGuard();
    const chunked =
      "POST http://example.com/ HTTP/1.1\r\nHost: example.com\r\n" +
      "Transfer-Encoding: chunked\r\n\r\n" +
      "5\r\nhello\r\n0\r\nX-Trailer: 1\r\n\r\n";
    try {
      const err = await sendParts(sink.port, [chunked + PROVIDER]);
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(sink.received().length).toBe(0);
    } finally {
      sink.close();
    }
  });

  it("refuses ambiguous framing", async () => {
    for (const headers of [
      "Content-Length: 3\r\nTransfer-Encoding: chunked\r\n",
      "Content-Length: 3\r\nContent-Length: 3\r\n",
      "Transfer-Encoding: gzip\r\n",
    ]) {
      const sink = await startSink();
      installEgressGuard();
      try {
        const err = await sendParts(sink.port, [
          `POST http://example.com/ HTTP/1.1\r\nHost: example.com\r\n${headers}\r\nabc`,
        ]);
        expect(err).toBeInstanceOf(EgressBlockedError);
        expect(sink.received().length).toBe(0);
      } finally {
        sink.close();
        uninstallEgressGuard();
      }
    }
  });

  it("leaves a non-HTTP exchange with a non-proxy server alone", async () => {
    const proxy = await startSink();
    const server = net.createServer((sock) => {
      sock.on("data", (d) => {
        if (d.toString() === "HELLO") sock.end("WORLD");
      });
    });
    await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
    installEgressGuard();
    try {
      const sock = net.connect({
        host: "127.0.0.1",
        port: (server.address() as net.AddressInfo).port,
      });
      const reply = await new Promise<string>((resolve, reject) => {
        sock.on("error", reject);
        sock.on("data", (d) => resolve(d.toString()));
        sock.write("HELLO");
      });
      expect(reply).toBe("WORLD");
    } finally {
      server.close();
      proxy.close();
    }
  });

  it("keeps stdout visible while the guard is installed", async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "attest-egress-"));
    try {
      const ts = createRequire(import.meta.url)("typescript") as typeof import("typescript");
      const source = fs.readFileSync(
        new URL("../../packages/core/src/egress.ts", import.meta.url),
        "utf8",
      );
      fs.writeFileSync(
        path.join(dir, "egress.mjs"),
        ts.transpileModule(source, {
          compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
        }).outputText,
      );
      fs.writeFileSync(
        path.join(dir, "run.mjs"),
        'import { installEgressGuard } from "./egress.mjs";\n' +
          "installEgressGuard();\n" +
          'console.log("INFO this must be visible");\n' +
          'process.stderr.write("WARN stderr too\\n");\n',
      );
      const { stdout, stderr } = await new Promise<{ stdout: string; stderr: string }>(
        (resolve, reject) =>
          execFile(
            process.execPath,
            [path.join(dir, "run.mjs")],
            { env: { ...process.env, HTTP_PROXY: "http://127.0.0.1:9" } },
            (e, so, se) => (e ? reject(e) : resolve({ stdout: so, stderr: se })),
          ),
      );
      expect(stdout).toBe("INFO this must be visible\n");
      expect(stderr).toBe("WARN stderr too\n");
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  });

  it.each([false, true])("lets TLS run through an allowed CONNECT tunnel (rejected first: %s)", async (rejectFirst) => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "attest-egress-tls-"));
    const key = path.join(dir, "key.pem");
    const cert = path.join(dir, "cert.pem");
    const sockets = new Set<net.Socket>();
    const track = (s: net.Socket) => {
      sockets.add(s);
      s.on("error", () => undefined);
    };
    let tlsServer: tls.Server | undefined;
    let proxy: net.Server | undefined;
    try {
      await new Promise<void>((resolve, reject) =>
        execFile(
          "openssl",
          ["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", key, "-out", cert,
            "-days", "1", "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost"],
          (e) => (e ? reject(e) : resolve()),
        ),
      );
      tlsServer = tls.createServer(
        { key: fs.readFileSync(key), cert: fs.readFileSync(cert) },
        (s) => {
          track(s);
          s.on("data", (d) => s.end(`echo:${d.toString()}`));
        },
      );
      await new Promise<void>((r) => tlsServer!.listen(0, "127.0.0.1", r));
      const tlsPort = (tlsServer.address() as net.AddressInfo).port;
      proxy = net.createServer((client) => {
        track(client);
        const tunnel = () => {
          const upstream = net.connect({ host: "127.0.0.1", port: tlsPort }, () => {
            client.write("HTTP/1.1 200 Connection Established\r\n\r\n");
            client.pipe(upstream);
            upstream.pipe(client);
          });
          track(upstream);
        };
        client.once("data", () => {
          if (!rejectFirst) return tunnel();
          // Refuse the first CONNECT and keep the connection open for the next one.
          client.write("HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n");
          client.once("data", tunnel);
        });
      });
      await new Promise<void>((r) => proxy!.listen(0, "127.0.0.1", r));
      const proxyPort = (proxy.address() as net.AddressInfo).port;
      process.env.HTTP_PROXY = `http://127.0.0.1:${proxyPort}`;
      installEgressGuard();

      const raw = net.connect({ host: "127.0.0.1", port: proxyPort });
      track(raw);
      await new Promise<void>((r) => raw.once("connect", r));
      if (rejectFirst) {
        raw.write("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n");
        const refusal = await new Promise<string>((r) => raw.once("data", (d) => r(d.toString())));
        expect(refusal.startsWith("HTTP/1.1 407")).toBe(true);
      }
      raw.write("CONNECT localhost:443 HTTP/1.1\r\nHost: localhost:443\r\n\r\n");
      const status = await new Promise<string>((r) => raw.once("data", (d) => r(d.toString())));
      expect(status.startsWith("HTTP/1.1 200")).toBe(true);
      const secure = tls.connect({ socket: raw, ca: fs.readFileSync(cert), servername: "localhost" });
      const reply = await new Promise<string>((resolve, reject) => {
        secure.on("error", reject);
        secure.on("secureConnect", () => secure.write("ping"));
        secure.on("data", (d) => resolve(d.toString()));
      });
      expect(reply).toBe("echo:ping");
    } finally {
      for (const s of sockets) s.destroy();
      tlsServer?.close();
      proxy?.close();
      fs.rmSync(dir, { recursive: true, force: true });
    }
  });
});

const CONNECT_ALLOWED = "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n";
const CONNECT_PROVIDER = "CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\n\r\n";

describe("egress guard after a CONNECT", () => {
  it("blocks a provider CONNECT written after a CONNECT the proxy refused", async () => {
    const sink = await startSink();
    installEgressGuard();
    try {
      const err = await sendParts(sink.port, [CONNECT_ALLOWED, CONNECT_PROVIDER]);
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(sink.received().toString("latin1")).toBe(CONNECT_ALLOWED);
    } finally {
      sink.close();
    }
  });

  it("screens plain HTTP written after an allowed CONNECT and delivers it intact", async () => {
    const sink = await startSink();
    installEgressGuard();
    try {
      const request = "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n";
      const ok = await sendParts(sink.port, [
        "CONNECT example.com:80 HTTP/1.1\r\nHost: example.com:80\r\n\r\n",
        request,
      ]);
      expect(ok).toBeNull();
      expect(sink.received().toString("latin1")).toContain(request);
      const err = await sendParts(sink.port, [
        "CONNECT example.com:80 HTTP/1.1\r\nHost: example.com:80\r\n\r\n",
        PROVIDER,
      ]);
      expect(err).toBeInstanceOf(EgressBlockedError);
    } finally {
      sink.close();
    }
  });
});

type Lookup = (host: string) => { address: string; family: number }[];

function stubLookup(resolve: Lookup | Error): void {
  vi.spyOn(dns.promises, "lookup").mockImplementation((async (host: string) => {
    if (resolve instanceof Error) throw resolve;
    return resolve(host);
  }) as never);
}

describe("egress guard identifies the proxy by its connected address", () => {
  it("blocks a provider CONNECT when the env names the proxy as localhost.", async () => {
    const sink = await startSink();
    process.env.HTTP_PROXY = `http://localhost.:${sink.port}`;
    installEgressGuard();
    try {
      const err = await sendParts(sink.port, [CONNECT_PROVIDER]);
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(sink.received().length).toBe(0);
    } finally {
      sink.close();
    }
  });

  it("blocks when a multi-address proxy name includes the connected address", async () => {
    const sink = await startSink();
    process.env.HTTP_PROXY = `http://proxy.multi.test:${sink.port}`;
    stubLookup(() => [
      { address: "10.255.255.1", family: 4 },
      { address: "127.0.0.1", family: 4 },
    ]);
    installEgressGuard();
    try {
      const err = await sendParts(sink.port, [CONNECT_PROVIDER]);
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(sink.received().length).toBe(0);
    } finally {
      sink.close();
    }
  });

  it("matches an IPv4-mapped IPv6 address of the proxy", async () => {
    const sink = await startSink();
    process.env.HTTP_PROXY = `http://proxy.mapped.test:${sink.port}`;
    stubLookup(() => [{ address: "::ffff:127.0.0.1", family: 6 }]);
    installEgressGuard();
    try {
      const err = await sendParts(sink.port, [CONNECT_PROVIDER]);
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(sink.received().length).toBe(0);
    } finally {
      sink.close();
    }
  });

  it("fails closed when the proxy name does not resolve on a configured proxy port", async () => {
    const sink = await startSink();
    process.env.HTTP_PROXY = `http://proxy.unresolved.test:${sink.port}`;
    stubLookup(new Error("ENOTFOUND"));
    installEgressGuard();
    try {
      const err = await sendParts(sink.port, [CONNECT_PROVIDER]);
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(sink.received().length).toBe(0);
    } finally {
      sink.close();
    }
  });

  it("leaves the proxy port on a different, non-proxy address alone", async () => {
    const sink = await startSink();
    process.env.HTTP_PROXY = `http://proxy.elsewhere.test:${sink.port}`;
    stubLookup(() => [{ address: "10.255.255.1", family: 4 }]);
    installEgressGuard();
    try {
      const err = await sendParts(sink.port, [CONNECT_PROVIDER]);
      expect(err).toBeNull();
      expect(sink.received().toString("latin1")).toBe(CONNECT_PROVIDER);
    } finally {
      sink.close();
    }
  });

  it("leaves a socket on another port alone", async () => {
    const sink = await startSink();
    process.env.HTTP_PROXY = `http://127.0.0.1:${sink.port + 1}`;
    installEgressGuard();
    try {
      const err = await sendParts(sink.port, [CONNECT_PROVIDER]);
      expect(err).toBeNull();
      expect(sink.received().toString("latin1")).toBe(CONNECT_PROVIDER);
    } finally {
      sink.close();
    }
  });
});

describe("egress guard follows proxy DNS rotation", () => {
  /** Dual-stack sink: one port reachable as 127.0.0.1 and ::1. */
  async function startDualSink(): Promise<{
    port: number;
    received: () => Buffer;
    close: () => void;
  }> {
    const chunks: Buffer[] = [];
    const sockets = new Set<net.Socket>();
    const server = net.createServer((sock) => {
      sockets.add(sock);
      sock.on("error", () => undefined);
      sock.on("data", (d: Buffer) => chunks.push(d));
    });
    await new Promise<void>((r) => server.listen(0, "::", r));
    return {
      port: (server.address() as net.AddressInfo).port,
      received: () => Buffer.concat(chunks),
      close: () => {
        for (const s of sockets) s.destroy();
        server.close();
      },
    };
  }

  async function sendVia(host: string, port: number, part: string) {
    const sock = net.connect({ host, port });
    let failure: Error | null = null;
    sock.on("error", (e) => {
      failure = e;
    });
    await new Promise<void>((r) => sock.once("connect", r));
    sock.write(part);
    await tick(100);
    sock.destroy();
    return failure as Error | null;
  }

  it("blocks a provider CONNECT via ::1 after the proxy name rotated from 127.0.0.1", async () => {
    const sink = await startDualSink();
    process.env.HTTP_PROXY = `http://proxy.rotate.test:${sink.port}`;
    const answers = [[{ address: "127.0.0.1", family: 4 }], [{ address: "::1", family: 6 }]];
    let call = 0;
    stubLookup(() => answers[Math.min(call++, answers.length - 1)] as never);
    installEgressGuard();
    try {
      const first = await sendVia("127.0.0.1", sink.port, CONNECT_PROVIDER);
      expect(first).toBeInstanceOf(EgressBlockedError);
      const second = await sendVia("::1", sink.port, CONNECT_PROVIDER);
      expect(second).toBeInstanceOf(EgressBlockedError);
      expect(sink.received().length).toBe(0);
    } finally {
      sink.close();
    }
  });

  it("delivers intact on the proxy port from an address in neither answer", async () => {
    const sink = await startDualSink();
    process.env.HTTP_PROXY = `http://proxy.rotate.test:${sink.port}`;
    stubLookup(() => [{ address: "10.255.255.1", family: 4 }]);
    installEgressGuard();
    try {
      const err = await sendVia("127.0.0.1", sink.port, CONNECT_PROVIDER);
      expect(err).toBeNull();
      expect(sink.received().toString("latin1")).toBe(CONNECT_PROVIDER);
    } finally {
      sink.close();
    }
  });

  it("blocks when the fresh lookup for an uncached address throws", async () => {
    const sink = await startDualSink();
    process.env.HTTP_PROXY = `http://proxy.rotate.test:${sink.port}`;
    let call = 0;
    vi.spyOn(dns.promises, "lookup").mockImplementation((async () => {
      if (call++ === 0) return [{ address: "127.0.0.1", family: 4 }];
      throw new Error("ENOTFOUND");
    }) as never);
    installEgressGuard();
    try {
      await sendVia("127.0.0.1", sink.port, ALLOWED);
      const err = await sendVia("::1", sink.port, CONNECT_PROVIDER);
      expect(err).toBeInstanceOf(EgressBlockedError);
      expect(sink.received().toString("latin1")).toBe(ALLOWED);
    } finally {
      sink.close();
    }
  });
});
