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
async function startProxy(): Promise<{ port: number; lines: string[]; close: () => void }> {
  const lines: string[] = [];
  const sockets = new Set<net.Socket>();
  const server = net.createServer((sock) => {
    sockets.add(sock);
    sock.on("error", () => undefined);
    sock.once("data", (d: Buffer) => {
      lines.push(d.toString("latin1").split("\r\n", 1)[0] as string);
      sock.end("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n");
    });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  return {
    port: (server.address() as net.AddressInfo).port,
    lines,
    close: () => {
      for (const s of sockets) s.destroy();
      server.close();
    },
  };
}

function viaProxy(port: number, opts: http.RequestOptions): Promise<Error | http.IncomingMessage> {
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
      const out = await viaProxy(proxy.port, { method: "CONNECT", path: "api.openai.com:443" });
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
      const out = await viaProxy(proxy.port, { path: "http://api.groq.com/openai/v1/models" });
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

  it("restores Socket.prototype.write on uninstall", () => {
    const before = net.Socket.prototype.write;
    installEgressGuard()();
    expect(net.Socket.prototype.write).toBe(before);
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
    for (const h of ["s3.us-east-1.amazonaws.com", "bedrock.us-east-1.amazonaws.com", "googleapis.com"]) {
      expect(isProviderHost(h), h).toBe(false);
    }
  });

  it("parses proxy request lines", () => {
    expect(proxiedTargetHost(Buffer.from("CONNECT api.openai.com:443 HTTP/1.1\r\n\r\n"))).toBe("api.openai.com");
    expect(proxiedTargetHost("POST http://openrouter.ai/x HTTP/1.1\r\n")).toBe("openrouter.ai");
    expect(proxiedTargetHost(Buffer.from("GET /v1/models HTTP/1.1\r\n"))).toBeNull();
    expect(proxiedTargetHost(Buffer.from([0x16, 0x03, 0x01]))).toBeNull();
  });
});
