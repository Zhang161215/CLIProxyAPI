#!/usr/bin/env python3
"""mitmproxy addon: capture one genuine Claude Code request for fingerprint work.

What it does
------------
Writes, per request, a JSON file containing:
  - the ordered header list (order matters: Go sorts maps, Node does not)
  - the raw request body (so the billing header, cch value and system layout
    can be diffed against what this fork emits)

It never writes Authorization, x-api-key, cookie, or any anthropic-* auth
material. The body is written verbatim because the cch digest is computed over
it; if your body contains customer data, run this against a scratch account.

Usage
-----
    mitmproxy -s tools/capture/dump_claude_request.py --save-stream-file /tmp/cc.flow
    # in another shell, point the real CLI at it, then send one prompt:
    #   NODE_USE_ENV_PROXY=1 HTTPS_PROXY=http://127.0.0.1:8080 \
    #   NODE_EXTRA_CA_CERTS=~/.mitmproxy/mitmproxy-ca-cert.pem claude

Output goes to $CAPTURE_DIR (default ./capture-out), one file per request.
"""

from __future__ import annotations

import json
import os
import time

from mitmproxy import http

CAPTURE_DIR = os.environ.get("CAPTURE_DIR", "./capture-out")
MAX_BODY = 2 * 1024 * 1024
REDACT_HEADERS = {
    "authorization",
    "x-api-key",
    "cookie",
    "set-cookie",
    "proxy-authorization",
}
INTERESTING_PATHS = ("/v1/messages", "/v1/messages/count_tokens")


def _safe_name(value: str) -> str:
    return "".join(ch if ch.isalnum() or ch in "-_." else "_" for ch in value)[:80]


def request(flow: http.HTTPFlow) -> None:
    path = flow.request.path
    if not any(path.startswith(p) for p in INTERESTING_PATHS):
        return

    body = flow.request.get_content() or b""
    if len(body) > MAX_BODY:
        body = body[:MAX_BODY]

    headers = []
    for name, value in flow.request.headers.fields:
        key = name.decode("latin-1")
        val = value.decode("latin-1")
        if key.lower() in REDACT_HEADERS:
            val = "<redacted>"
        headers.append([key, val])

    record = {
        "captured_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "method": flow.request.method,
        "scheme": flow.request.scheme,
        "host": flow.request.host,
        "path": path,
        # Keep the order: the upstream sees the server order, not a sorted set.
        "headers_ordered": headers,
        "body": body.decode("utf-8", errors="replace"),
    }

    os.makedirs(CAPTURE_DIR, exist_ok=True)
    stamp = time.strftime("%Y%m%d-%H%M%S", time.gmtime())
    name = f"{stamp}-{_safe_name(flow.request.host)}-{_safe_name(path)}.json"
    out = os.path.join(CAPTURE_DIR, name)
    with open(out, "w", encoding="utf-8") as handle:
        json.dump(record, handle, ensure_ascii=False, indent=2)
    flow.metadata["capture_file"] = out
