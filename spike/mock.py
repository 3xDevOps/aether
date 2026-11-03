#!/usr/bin/env python3
"""Mock Anthropic: HTTP on :PORT (ANTHROPIC_BASE_URL) and an HTTPS-intercepting CONNECT proxy on :PPORT.
Behaviour is read per request from $SPIKE_DIR/config.json. Log: $SPIKE_DIR/mock.log (jsonl)."""
import json, os, socket, ssl, sys, threading, time, http.server, socketserver
D = os.environ.get("SPIKE_DIR", "/tmp/spike-run"); os.makedirs(D, exist_ok=True)
PORT = int(os.environ.get("MOCK_PORT", 8801)); PPORT = int(os.environ.get("PROXY_PORT", 8802))
CERT = os.path.dirname(os.path.abspath(__file__)) + "/certs/"
LOG = open(D + "/mock.log", "a", buffering=1); LL = threading.Lock()
def cfg():
    try: return json.load(open(D + "/config.json"))
    except Exception: return {}
def log(rec):
    rec["t"] = round(time.time(), 3)
    with LL: LOG.write(json.dumps(rec) + "\n")
KEEP = {"authorization", "x-api-key", "anthropic-beta", "anthropic-version", "user-agent", "x-app", "content-type", "anthropic-dangerous-direct-browser-access"}
class H(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *a): pass
    def _do(self):
        n = int(self.headers.get("content-length") or 0); body = self.rfile.read(n) if n else b""
        host = self.headers.get("host", ""); c = cfg(); path = self.path.split("?")[0]
        hdr = {k.lower(): v for k, v in self.headers.items() if k.lower() in KEEP}
        rec = {"m": self.command, "host": host, "path": self.path, "hdr": hdr, "blen": len(body)}
        status, out, ctype = 404, b'{"error":"mock-404"}', "application/json"
        if path == "/v1/messages":
            try: rec["model"] = json.loads(body).get("model"); stream = json.loads(body).get("stream")
            except Exception: stream = True
            st = c.get("messages_status", 200)
            if c.get("messages_status_by_token", {}).get(hdr.get("authorization", "").replace("Bearer ", "")):
                st = c["messages_status_by_token"][hdr["authorization"].replace("Bearer ", "")]
            if st != 200:
                status, out = st, json.dumps({"type": "error", "error": {"type": "authentication_error", "message": "mock auth failure"}}).encode()
            else:
                status, ctype = 200, "text/event-stream" if stream else "application/json"
                msg = {"id": "msg_mock", "type": "message", "role": "assistant", "model": rec.get("model") or "mock", "content": [], "stop_reason": None, "usage": {"input_tokens": 5, "output_tokens": 1}}
                if stream:
                    ev = lambda e, d: f"event: {e}\ndata: {json.dumps(d)}\n\n"
                    out = (ev("message_start", {"type": "message_start", "message": msg}) + ev("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}) + ev("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": c.get("reply", "hello from mock")}}) + ev("content_block_stop", {"type": "content_block_stop", "index": 0}) + ev("message_delta", {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": None}, "usage": {"output_tokens": 4}}) + ev("message_stop", {"type": "message_stop"})).encode()
                else:
                    msg["content"] = [{"type": "text", "text": c.get("reply", "hello from mock")}]; msg["stop_reason"] = "end_turn"; out = json.dumps(msg).encode()
                time.sleep(c.get("messages_delay", 0))
        elif path == "/v1/oauth/token":
            try: rec["body_keys"] = {k: (v if k in ("grant_type", "client_id", "scope") else "<redacted>") for k, v in json.loads(body).items()}
            except Exception: pass
            time.sleep(c.get("token_delay", 0))
            if "token_status" in c and c["token_status"] != 200: status, out = c["token_status"], b'{"error":"invalid_grant"}'
            else:
                status = 200; out = json.dumps(c.get("token_response", {"access_token": "fake-access-refreshed", "refresh_token": "fake-refresh-rotated", "expires_in": 3600, "scope": "user:inference user:profile user:sessions:claude_code user:mcp_servers user:file_upload"})).encode()
        elif path == "/api/oauth/profile":
            status = 200; out = json.dumps(c.get("profile", {"account": {"uuid": "00000000-0000-0000-0000-000000000001", "email": "mock@example.invalid", "display_name": "Mock", "full_name": "Mock User", "created_at": "2024-01-01T00:00:00Z"}, "organization": {"uuid": "00000000-0000-0000-0000-000000000002", "organization_type": "claude_max", "rate_limit_tier": "default_claude_max_5x", "has_extra_usage_enabled": False, "billing_type": "stripe_subscription", "subscription_created_at": "2024-01-01T00:00:00Z", "cc_onboarding_flags": {}}})).encode()
        elif path.endswith("/hello"):
            status, out = 200, b"{}"
        elif path == "/api/oauth/usage":
            status = 200; out = b'{"five_hour":{"utilization":1.0,"resets_at":null},"seven_day":null}'
        elif path == "/api/event_logging/v2/batch" or path.startswith("/api/event_logging"):
            status, out = 200, b"{}"
        rec["status"] = status; log(rec)
        self.send_response(status); self.send_header("content-type", ctype); self.send_header("content-length", str(len(out))); self.send_header("request-id", "req_mock"); self.end_headers(); self.wfile.write(out)
    do_GET = do_POST = do_PUT = do_DELETE = do_HEAD = _do
class TS(socketserver.ThreadingMixIn, http.server.HTTPServer): daemon_threads = True; allow_reuse_address = True
class TLSH(H):
    pass
def proxy():
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER); ctx.load_cert_chain(CERT + "srv.pem", CERT + "srv.key")
    class P(socketserver.BaseRequestHandler):
        def handle(self):
            f = self.request.makefile("rb"); line = f.readline().decode(); 
            while f.readline() not in (b"\r\n", b"\n", b""): pass
            if not line.startswith("CONNECT"):
                log({"proxy_nonconnect": line.strip()}); self.request.sendall(b"HTTP/1.1 502 no\r\n\r\n"); return
            tgt = line.split()[1]; log({"proxy_connect": tgt})
            self.request.sendall(b"HTTP/1.1 200 OK\r\n\r\n")
            try: s = ctx.wrap_socket(self.request, server_side=True)
            except Exception as e: log({"tls_fail": str(e), "tgt": tgt}); return
            # serve one TLS connection with keepalive via the same handler
            class R(TLSH):
                def setup(self): self.rfile = s.makefile("rb"); self.wfile = s.makefile("wb")
                def finish(self): pass
            R(s, ("proxy", 0), None)
    srv = socketserver.ThreadingTCPServer(("127.0.0.1", PPORT), P); srv.daemon_threads = True; srv.allow_reuse_address = True; srv.serve_forever()
threading.Thread(target=proxy, daemon=True).start()
log({"mock": "up", "port": PORT, "pport": PPORT})
TS(("127.0.0.1", PORT), H).serve_forever()
