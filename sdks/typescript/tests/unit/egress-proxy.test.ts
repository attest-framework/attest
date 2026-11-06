import * as http from "node:http";
import * as net from "node:net";
import { afterEach, describe, expect, it } from "vitest";
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
  return {
    port: (server.address() as net.AddressInfo).port,
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

describe("egress guard behind an HTTP proxy", () => {
  afterEach(() => {
    uninstallEgressGuard();
  });

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

    it("refuses a request line that outgrows the buffer bound", async () => {
      const { err, raw } = await blocked([
        Buffer.from(`GET http://example.com/${"a".repeat(9000)}`),
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
