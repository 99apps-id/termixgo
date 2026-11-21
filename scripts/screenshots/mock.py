#!/usr/bin/env python3
"""Scripted OpenAI-compatible model for demo screenshots.

It streams slowly so a frame can be captured mid-run. It is a stand-in for a
model only: the UI, tools and approvals around it are the real program."""
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

WORD_DELAY = 0.07


def last_turn(messages):
    """Return (user text, number of tool results since that user message)."""
    tools = 0
    for message in reversed(messages):
        role = message.get("role")
        if role == "tool":
            tools += 1
        elif role == "user":
            content = message.get("content")
            if isinstance(content, list):
                content = " ".join(p.get("text", "") for p in content if isinstance(p, dict))
            if (content or "").startswith("[System:"):
                continue  # a nudge from the agent loop, still the same turn
            return content or "", tools
    return "", tools


class Handler(BaseHTTPRequestHandler):
    # HTTP/1.0 closes the connection after the stream, which ends the SSE body.
    protocol_version = "HTTP/1.0"

    def log_message(self, *args):
        pass

    def do_GET(self):
        body = json.dumps({"data": []}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def send_chunk(self, obj):
        self.wfile.write(b"data: " + json.dumps(obj).encode() + b"\n\n")
        self.wfile.flush()

    def words(self, text, field, delay=WORD_DELAY):
        for word in text.split(" "):
            self.send_chunk({"choices": [{"delta": {field: word + " "}}]})
            time.sleep(delay)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        request = json.loads(self.rfile.read(length) or b"{}")
        messages = request.get("messages", [])
        has_tools = bool(request.get("tools"))
        user, n = last_turn(messages)

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()

        usage = {"prompt_tokens": 2400 + n * 900, "completion_tokens": 120 + n * 60}

        if not has_tools:
            self.send_chunk({"choices": [{"delta": {"content": "Orders rate limiting"}}]})
            self.end_stream(usage, "stop")
            return

        if "pagination" in user.lower():
            self.plan(n, usage)
        else:
            self.orders(n, usage)

    def call(self, name, args, index=0):
        self.send_chunk({"choices": [{"delta": {"tool_calls": [{
            "index": index, "id": f"call_{name}_{index}", "type": "function",
            "function": {"name": name, "arguments": json.dumps(args)}}]}}]})

    def end_stream(self, usage, reason):
        self.send_chunk({"choices": [{"delta": {}, "finish_reason": reason}]})
        self.send_chunk({"choices": [], "usage": {**usage, "total_tokens": usage["prompt_tokens"] + usage["completion_tokens"]}})
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def orders(self, n, usage):
        if n == 0:
            self.words("The orders endpoint is registered in the API package. I will find the handler first, then wrap it with a per client limiter so the existing behavior stays the same.", "reasoning_content")
            self.words("Let me find where the orders endpoint is registered.", "content")
            self.call("grep", {"pattern": "orders", "path": "internal"})
            self.end_stream(usage, "tool_calls")
        elif n == 1:
            self.words("Found it. Reading the handler before I change it.", "content")
            self.call("read_file", {"path": "internal/api/orders.go"})
            self.end_stream(usage, "tool_calls")
        elif n == 2:
            self.words("The route is mounted without any limiter. I will wrap it with RateLimit at 100 requests a minute per client.", "reasoning_content")
            self.words("Adding the limiter around the orders route.", "content")
            self.call("edit", {
                "path": "internal/api/orders.go",
                "old_string": '\tmux.HandleFunc("/orders", ordersHandler(store))',
                "new_string": '\tmux.Handle("/orders", RateLimit(100, time.Minute, ordersHandler(store)))',
            })
            self.end_stream(usage, "tool_calls")
        elif n == 3:
            self.words("Now the tests.", "content")
            self.call("run_command", {"command": "sh scripts/test.sh"})
            self.end_stream(usage, "tool_calls")
        else:
            answer = ("Done. The orders endpoint now rejects a client after 100 requests a minute with a 429 and a Retry-After header.\n\n"
                      "- **Change:** `internal/api/orders.go` wraps the route in `RateLimit(100, time.Minute, ...)`.\n"
                      "- **Behavior:** other routes are untouched, and the limiter keys on the client address.\n"
                      "- **Verified:** `sh scripts/test.sh` passes, including the new `TestOrdersRateLimit`.\n\n"
                      "Next I would move the limit into config so it can differ per environment.")
            self.words(answer.replace("\n", " \n"), "content", 0.05)
            self.end_stream(usage, "stop")

    def plan(self, n, usage):
        if n == 0:
            self.words("A plan first: the list endpoint needs a cursor, a page size cap and tests.", "reasoning_content")
            self.call("todo_write", {"todos": [
                {"id": "t1", "title": "Add a cursor and limit to GET /orders", "status": "in_progress"},
                {"id": "t2", "title": "Cap the page size at 100", "status": "pending"},
                {"id": "t3", "title": "Return next_cursor in the response", "status": "pending"},
                {"id": "t4", "title": "Cover paging with tests", "status": "pending"},
            ]})
            self.end_stream(usage, "tool_calls")
        elif n == 1:
            plan = ("## Plan: paginate GET /orders\n\n"
                    "1. Add `cursor` and `limit` query parameters in `internal/api/orders.go`.\n"
                    "2. Cap `limit` at 100 and default it to 25.\n"
                    "3. Return `next_cursor` so clients can continue.\n"
                    "4. Add `TestOrdersPaging` for the first page, the last page and a bad cursor.\n\n"
                    "Nothing is changed until you approve.")
            self.call("submit_plan", {"plan": plan})
            self.end_stream(usage, "tool_calls")
        else:
            self.words("Understood. I will keep planning.", "content")
            self.end_stream(usage, "stop")


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 18700
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
