# Capture guide

Three captures feed the anti-detection work. They are ordered by **value per
minute**, and only the first one is needed to unblock the current milestone.

Everything here runs against your own account with the genuine Claude Code
client. Nothing is uploaded anywhere; the scripts write local files only.

---

## Capture 1 — request shape (highest value, ~10 minutes)

**Unlocks:** P0-1 (version constants), P0-2 (confirmed clients must be forwarded
without rewriting), and the `cch` seed check.

**What it produces:** the ordered header list and the raw JSON body of one real
`/v1/messages` call — including the `x-anthropic-billing-header` system block
with `cc_version` and `cch`, plus `metadata.user_id`, `tools`, and the exact
`system` layout.

```bash
# terminal 1
mitmproxy -s tools/capture/dump_claude_request.py --save-stream-file /tmp/cc.flow

# terminal 2 — point the real CLI at the proxy
NODE_USE_ENV_PROXY=1 \
HTTPS_PROXY=http://127.0.0.1:8080 \
NODE_EXTRA_CA_CERTS="$HOME/.mitmproxy/mitmproxy-ca-cert.pem" \
claude
# then send a single short prompt and quit

# terminal 3
ls -la ./capture-out/
```

If the CLI ignores `HTTPS_PROXY`, fall back to transparent interception: run
mitmproxy on a separate host, set the client's gateway to it, and use
`--mode transparent`. The output format is identical.

**Read the result for three numbers:**

| Field | Where | Why |
|---|---|---|
| `cc_version=<x.y.z>.<abc>` | `system[0].text` | the version the client actually claims **today** |
| `cch=<5 hex>` | same line | the digest to verify the seed against |
| `x-stainless-package-version` | headers | must match the version in the ledger |

**Do not commit capture output.** `.gitignore` already excludes `capture-out/`;
keep it that way — bodies contain your prompts.

---

## Capture 2 — TLS ClientHello (~5 minutes, no decryption needed)

**Unlocks:** P0-1's `TLSHelloFrom` field, and the decision of whether the fork's
hand-written uTLS spec is still valid.

```bash
# on the machine running the real CLI
sudo tcpdump -i any -w /tmp/cc-cli.pcap 'host api.anthropic.com and tcp port 443'
# send one prompt, then Ctrl-C
tools/capture/ja4.sh /tmp/cc-cli.pcap
```

This is a plaintext handshake — no certificate work, no MITM.

Compare the printed `ja4` / extension order against
`internal/runtime/executor/helps/utls_client.go`. Two different JA4s mean the
spec is stale, and the only correct fix is regenerating it from this capture.

---

## Capture 3 — `cch` seed verification (instant, reuses capture 1)

**Unlocks:** whether `CCHSeeds` in `profile.go` needs a new entry.

Take the body from capture 1 and recompute:

```bash
go run ./tools/capture/verify-cch -file capture-out/<file>.json
# machine-readable:
go run ./tools/capture/verify-cch -file capture-out/<file>.json -json
```

The tool prints the identity it found on the wire (`cc_version`, `cc_entrypoint`,
`cch`, the stainless family) and then recomputes the digest with the recorded
seed. `verdict: MATCH` means the seed for that version is confirmed; `MISMATCH`
means the projection changed and signing must be treated as unverified for that
version. It exits 1 on a mismatch, so it can gate a script.

If it does not match for the captured version, the seed changed and the version
must **not** be signed with the old constant — see `SeedFor`'s contract.

---

## What is deliberately not captured

| Item | Why |
|---|---|
| HTTP/2 SETTINGS frame, GOAWAY, connection-reuse curve | No public implementation exists; low expected value at this stage |
| Telemetry rows | The official client disables analytics on gateways and third-party providers; faking them is out of scope (see PLAN §4.2) |
| Any credential material | The addon redacts `authorization`, `x-api-key` and cookies before writing |

---

## Keeping captures out of git

`.gitignore` should contain:

```
capture-out/
*.pcap
/tmp/cc.flow
```

Verify with `git status --short` before committing anything from this directory.
