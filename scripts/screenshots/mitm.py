#!/usr/bin/env python3
"""Demo-only HTTPS proxy.

termixgo asks the providers' usage endpoints how much of each plan is left. In
a demo session there is no real login, so this proxy answers exactly those two
requests with sample figures, signed by a throwaway CA the demo process trusts
through SSL_CERT_FILE, and refuses every other destination. The program under
test is unchanged: it makes its normal requests."""
import json
import socketserver
import ssl
import sys
import threading
from datetime import datetime, timedelta, timezone

B = "/tmp/tgx-shots"


def iso(delta):
    return (datetime.now(timezone.utc) + delta).strftime("%Y-%m-%dT%H:%M:%SZ")


def answer(host, path):
    if host == "api.anthropic.com" and path.startswith("/api/oauth/usage"):
        return {
            "five_hour": {"utilization": 28, "resets_at": iso(timedelta(hours=2, minutes=14))},
            "seven_day": {"utilization": 59, "resets_at": iso(timedelta(days=3, hours=5))},
        }
    if host == "chatgpt.com" and path.startswith("/backend-api/wham/usage"):
        return {"rate_limit": {
            "primary_window": {"used_percent": 10, "reset_at": iso(timedelta(hours=4, minutes=40))},
            "secondary_window": {"used_percent": 34, "reset_at": iso(timedelta(days=5, hours=2))},
        }}
    return None


CTX = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
CTX.load_cert_chain(f"{B}/srv.pem", f"{B}/srv.key")


def read_head(sock):
    data = b""
    while b"\r\n\r\n" not in data and len(data) < 65536:
        chunk = sock.recv(4096)
        if not chunk:
            break
        data += chunk
    return data


class Handler(socketserver.BaseRequestHandler):
    def handle(self):
        try:
            head = read_head(self.request).decode("latin1")
            first = head.split("\r\n", 1)[0].split()
            if len(first) < 2 or first[0] != "CONNECT":
                self.request.sendall(b"HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n")
                return
            host = first[1].split(":")[0]
            if host not in ("api.anthropic.com", "chatgpt.com"):
                self.request.sendall(b"HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
                return
            self.request.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            tls = CTX.wrap_socket(self.request, server_side=True)
            request = read_head(tls).decode("latin1")
            line = request.split("\r\n", 1)[0].split()
            path = line[1] if len(line) > 1 else "/"
            body = answer(host, path)
            if body is None:
                tls.sendall(b"HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
            else:
                payload = json.dumps(body).encode()
                tls.sendall(b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nConnection: close\r\nContent-Length: "
                            + str(len(payload)).encode() + b"\r\n\r\n" + payload)
            tls.close()
        except Exception as error:  # a demo helper: log and move on
            print("proxy error:", error, file=sys.stderr)


class Server(socketserver.ThreadingMixIn, socketserver.TCPServer):
    allow_reuse_address = True
    daemon_threads = True


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 18701
    Server(("127.0.0.1", port), Handler).serve_forever()
