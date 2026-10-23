# Case 2026-10: one account banned after four days on vm2api

> Recorded 2026-10-21. Kept as evidence for later decisions about the
> "run the real client in a VM" approach.

---

## 1. Conclusion

**This was a behaviour decision, not a fingerprint decision.**

The account lived 4 days 5 hours. The data shows one path:
normal traffic → accumulating 429s → failure storm → revocation.

Nothing in the sequence depends on how closely the requests resembled the
official client. Everything depends on volume and failure rate.

---

## 2. Facts

### 2.1 Account

| Field | Value |
|---|---|
| Email | `44.topmost.turbo@icloud.com` (Apple Hide My Email relay) |
| Billing | Apple in-app purchase, `billingType: apple_subscription` |
| Account created | 2026-10-16T12:46:52Z |
| Subscription started | 2026-10-16T12:55:47Z |
| First proxied request | 2026-10-16T12:56:31Z (44 s later) |
| Last successful request | 2026-10-20T15:34:32Z |
| Revocation visible | `OAuth refresh failed` from 2026-10-21T00:36Z |

### 2.2 Ban email

Subject: `Your account has been suspended`

> Hello,
>
> An internal investigation of suspicious signals associated with your account
> indicates a violation of our Usage Policy. As a result, we have revoked your
> access to Claude.
>
> To appeal our decision, log in to claude.ai with this account and you'll be
> taken to the appeals page. You can learn more about the process here.
>
> Regards
>
> Anthropic's Safeguards Team

Identical template to the CPA batch of October 2026: same team, same
`suspicious signals`, same `revoked your access`. Both architectures were judged
by the same process.

### 2.3 Daily timeline

| Date | Requests | OK | 429 | 5xx | Success |
|---|---|---|---|---|---|
| 10-16 | 349 | 349 | 0 | 0 | 100% |
| 10-17 | 788 | 631 | 38 | 114 | 80% |
| 10-18 | 1209 | 801 | 112 | 296 | 66% |
| 10-19 | 1943 | 247 | 302 | 1368 | **12.7%** |
| 10-20 | 1294 | 174 | 96 | 1024 | **13.4%** |
| 10-21 | 3 | 0 | 0 | 0 | — |

Peak: **10-19 11:00 — 690 requests, 0 successful**; 12:00 — 415 requests,
8 successful.

Successful traffic in total: 1,881,888 output tokens, 205,946,280 cache reads,
57 downstream sessions, 147 upstream sessions.

### 2.4 Error mix

| Error | Count | Origin |
|---|---|---|
| `503 account_pool_exhaust / no_eligible_accounts` | 2327 | local (single account, cooled down) |
| `429 quota_guard_request_too_large` | 258 | local guard |
| `429 api_key_concurrency_limit (4)` | 128 | local guard |
| `529 pool_overloaded / pool_queue_timeout` | 36 | local queue |
| `400 upstream_invalid_req: Invalid signature in thinking block` | 25 | **upstream rejection** |
| `400 OAuth refresh failed` | 3 | post-revocation |

The 2327 local failures matter because of what the upstream saw: a gateway that
kept knocking while it had nothing to serve.

---

## 3. Four self-inflicted configuration choices

| Setting | Value | Problem |
|---|---|---|
| `proxy` / `proxy_required` | `px-local` / **false** | contradicts the project's own rule ("slot SOCKS5 proxy is required"); raw datacenter egress |
| `vm.mac_oui` | `52:54:00` | QEMU's registered OUI; announces "virtual machine" |
| `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` | `1` | telemetry off inside the slot |
| `CLAUDE_CODE_GATEWAY_HINT_HEADERS` | `1` | **declares itself a gateway** |
| `compatibility.persona_preset` | `official_full` | full official prompt injection |

### On `CLAUDE_CODE_GATEWAY_HINT_HEADERS`

In the 2.1.280 binary, enabling this emits the following header family:

```
x-cc-gateway-version
x-claude-gateway-user-email
x-claude-gateway-user-id
x-gateway-model
x-gateway-upstream
x-gateway-upstream-kind
x-gateway-spend-admin
```

A slot that advertises "1:1 real client" was sending headers that say the
opposite.

---

## 4. Side-by-side with the CPA deployment

Both hosts sit in AWS us-west-2. Neither sends telemetry.

| | CPA | vm2api 1.3.188 |
|---|---|---|
| Egress | datacenter, direct | datacenter, direct |
| Client | synthesised by the proxy | real Claude Code |
| Telemetry | none | none (explicitly disabled) |
| Requests in 4 days | dozens | **2202 successful** |
| Peak | no burst | **690/hour at 0% success** |
| Gateway self-declaration | absent | **present** |
| Outcome | alive | revoked |

Same IP class, same telemetry state, same ban template — opposite outcomes.
Volume and failure rate are the only differences.

---

## 5. Attribution

### Supported by the data

1. **The failure storm is the dominant signal.** 2327 local failures, 302 upstream
   429s and 1368 5xx responses inside 36 hours.
2. **No back-off after the first 429s.** Retrying turned rate limiting into
   revocation.
3. **25 invalid thinking signatures** show session state being replayed across
   slots; the upstream saw discontinuous context.

### Not supported

- "Datacenter IPs always get banned" — the CPA host uses one and is alive.
- "No telemetry always gets banned" — neither host sends any.
- "The fingerprint looked synthetic" — this host ran the genuine client.

### Open

- How much the gateway self-declaration contributed. One sample cannot quantify
  it, but its direction is clearly negative.

---

## 6. Rules worth keeping

1. **Per-account daily ceiling: 300 requests.** Beyond that the traffic no longer
   looks like one person on a Pro plan.
2. **Failure-rate red line: above 20% for ten minutes, stop the account.**
3. **429 cooldown: at least 15 minutes, no retry.** Retrying is the most expensive
   action available.
4. **Never declare gateway semantics** in any header.
5. **Cover the VM identity**: MAC OUI, machine-id and SMBIOS must not be QEMU
   defaults.
6. **Architecture is not protection.** A genuine client with a clean fingerprint
   does not offset an abusive traffic shape.

---

## 7. What this means for this fork

| Existing control | Verdict |
|---|---|
| Quota window admission | works: it stops at the first 429, the exact gap this case shows |
| Content gate | unrelated but harmless |
| Throttle | directly suppresses the peak |

| Missing control | Proposal |
|---|---|
| **429 circuit breaker** | the registry counts `ratelimited` but nothing acts on it; add "N consecutive 429s within M minutes pauses the credential for a cooldown" |

---

## Appendix: live experiment on the second shared account

The relay runs one account per server and shares it across users. Two accounts
were created under identical conditions (iOS in-app purchase, iCloud address,
AWS us-west-2 origin, same timezone setting).

| | account A (cpa-5) | account B (cpa-6) |
|---|---|---|
| Created | 2026-10-18 19:27 | 2026-10-21 04:46 UTC |
| Outcome | `account_on_hold` on 2026-10-21 11:42 (day 3) | running |
| Successful requests | 4489 | 83 |
| 7d utilization at check | 0.92 | 0.86 |
| Distinct downstream IPs | 15 across 3 countries | unknown (logging was off) |
| Peak concurrent requests | 10 | not yet measured |

Both accounts are shared by multiple people, which is why their behaviour looks
like several unrelated users rather than one: that is what the upstream sees, and
it is the one signal a proxy cannot mask without refusing traffic.

Account B had file logging disabled, so its history before 2026-10-22 is gone.
Logging is now on (512 MB) and `credentials.concurrency.max-limit` was set to 3
at 2026-10-22 01:35 UTC, with 7d utilization already at 0.86.

Outcome to watch: if B outlives day 5, the concurrency cap is a usable lever for
relays; if B also dies around day 3, sharing is not survivable and one account per
user is the only option.

---

## Appendix B: cloaking A/B running since 2026-10-22

Two accounts, same fork, same region, same downstream users, opposite cloaking
setting. Both write logs from the start.

| | cpa-7 | cpa-8 |
|---|---|---|
| Fork version | v0.5.11 | v0.5.11 |
| `disable-claude-cloak-mode` | **true** (no identity synthesis) | **false** (default) |
| Account | `while-3-babbles@icloud.com` | `slam_hires1v@icloud.com` |
| Account created | 2026-10-21 | 2026-10-21 23:37 PDT |
| First day seen | 208 requests before the switch | 1578 requests, 79 × 429 |
| Utilization at baseline | 5h 0.88 / 7d 0.28 | 5h 0.31 / 7d 0.21 |

Both previous cloaking-on accounts (`cpa-5`, `cpa-6`) were revoked on days 3 and 4
with the same Safeguards message. If cpa-7 outlives cpa-8 here, cloaking is the
difference; if both die on the same schedule, it is not, and the remaining
candidates are account provenance or something no proxy can see.
