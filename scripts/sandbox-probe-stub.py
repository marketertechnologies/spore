"""Mock Anthropic Messages API for scripts/sandbox-probe.sh.

A prompt carrying `SBPROBE:<base64url json>` gets one scripted Bash
tool_use ({"command", "disable"}); once the conversation holds the
tool_result, the reply echoes it as `TOOL_RESULT is_error=<bool>` plus
the output, so the probe can grade it. Any other request (titles,
side queries) gets a plain "ok". Streaming and non-streaming both work.
"""
import base64
import http.server
import json
import re
import socketserver
import sys


def text_of(content):
    if isinstance(content, str):
        return content
    return "\n".join(b.get("text", "") for b in content if isinstance(b, dict) and b.get("type") == "text")


def result_text(block):
    c = block.get("content")
    if isinstance(c, str):
        return c
    return "\n".join(x.get("text", "") for x in (c or []) if isinstance(x, dict))


def plan(body):
    msgs = body.get("messages", [])
    spec = None
    for m in msgs:
        if m.get("role") == "user":
            found = re.search(r"SBPROBE:([A-Za-z0-9_=-]+)", text_of(m.get("content", "")))
            if found:
                spec = json.loads(base64.urlsafe_b64decode(found.group(1)))
    if spec is None:
        return [{"type": "text", "text": "ok"}], "end_turn"
    # Claude Code may append its own system message after the tool_result,
    # so scan every message rather than the last one.
    for m in msgs:
        if not isinstance(m.get("content"), list):
            continue
        for b in m["content"]:
            if isinstance(b, dict) and b.get("type") == "tool_result":
                text = "TOOL_RESULT is_error=%s\n%s" % (bool(b.get("is_error")), result_text(b))
                return [{"type": "text", "text": text}], "end_turn"
    tool_input = {"command": spec["command"], "description": "sandbox probe", "timeout": 600000}
    if spec.get("disable"):
        tool_input["dangerouslyDisableSandbox"] = True
    return [{"type": "tool_use", "id": "toolu_sbprobe", "name": "Bash", "input": tool_input}], "tool_use"


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def send_json(self, payload):
        data = json.dumps(payload).encode()
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        self.send_json({})

    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("content-length", 0)))
        try:
            body = json.loads(raw)
        except ValueError:
            body = {}
        if "/count_tokens" in self.path:
            self.send_json({"input_tokens": 10})
            return
        if "/v1/messages" not in self.path:
            self.send_response(404)
            self.end_headers()
            return
        blocks, stop = plan(body)
        msg = {
            "id": "msg_sbprobe", "type": "message", "role": "assistant",
            "model": body.get("model", "stub"), "content": [],
            "stop_reason": None, "stop_sequence": None,
            "usage": {"input_tokens": 10, "output_tokens": 10},
        }
        if not body.get("stream"):
            msg["content"], msg["stop_reason"] = blocks, stop
            self.send_json(msg)
            return
        self.send_response(200)
        self.send_header("content-type", "text/event-stream")
        self.end_headers()

        def event(kind, data):
            self.wfile.write(("event: %s\ndata: %s\n\n" % (kind, json.dumps(data))).encode())

        event("message_start", {"type": "message_start", "message": msg})
        for i, b in enumerate(blocks):
            if b["type"] == "text":
                start = {"type": "text", "text": ""}
                delta = {"type": "text_delta", "text": b["text"]}
            else:
                start = {"type": "tool_use", "id": b["id"], "name": b["name"], "input": {}}
                delta = {"type": "input_json_delta", "partial_json": json.dumps(b["input"])}
            event("content_block_start", {"type": "content_block_start", "index": i, "content_block": start})
            event("content_block_delta", {"type": "content_block_delta", "index": i, "delta": delta})
            event("content_block_stop", {"type": "content_block_stop", "index": i})
        event("message_delta", {"type": "message_delta", "delta": {"stop_reason": stop, "stop_sequence": None}, "usage": {"output_tokens": 10}})
        event("message_stop", {"type": "message_stop"})
        self.wfile.flush()


class Server(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True


if __name__ == "__main__":
    srv = Server(("127.0.0.1", 0), Handler)
    print(srv.server_address[1], flush=True)
    srv.serve_forever()
