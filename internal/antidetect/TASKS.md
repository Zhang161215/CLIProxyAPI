# 任务总表

> 仓库：CLIProxyAPI fork · 分支 `feat/antidetect` · 基线 `911f49c`
> 设计论证见 [PLAN.md](PLAN.md)；本文件只回答"做到哪了、还剩什么、怎么验收"。
> 状态口径：✅ 完成并测试 · 🟡 部分完成 · ⬜ 未开始 · ⛔ 决定不做 · ⏸ 待外部输入

---

## 一、总览

| 阶段 | 项 | 状态 |
|---|---|---|
| P0 | 4 项 | 4 ✅ |
| P1 | 4 项 | 3 ✅ · 1 ⬜ |
| P2 | 3 项 | 2 ✅ · 1 ⛔ |
| P3 | 1 项 | ✅ |

**测试**：全仓 `go build ./...` / `go vet` 通过；`go test ./...` 仅 1 个红（故意留的，见 T-05）。

---

## 二、已完成

### T-01 版本一致性守护 ✅

- **产物**：`internal/antidetect/versionguard/`、`cmd/server/main.go`（启动接入）
- **行为**：启动时打印身份摘要；四层版本不一致则**拒绝启动**，`CPA_ANTIDETECT_ALLOW_VERSION_DRIFT=1` 可放行
- **验收**：`TestCheckRefusesDriftedIdentity`、`TestLedgerMatchesUpstreamSource`
- **开关**：`CPA_ANTIDETECT_ALLOW_VERSION_DRIFT`

### T-02 四闸 fail-safe ✅

- **产物**：`helps/claude_client_state.go`、`claude_executor_cloaking.go`、`claude_executor_request.go`
- **行为**：二值判定 → `Confirmed / Unknown / Rejected`；**无法识别的 entrypoint 透传**，不重写 UA、不注入 billing、不改 system 身份
- **验收**：`TestClaudeExecutor_UnknownEntrypointIsNotCloaked`、`TestClientState*` 共 7 条

### T-03 官方变更追踪 ✅

- **产物**：`tools/changelog-watch/`
- **行为**：拉官方 CHANGELOG，与本地基线 diff，关键词分 STRONG（失败）/ review（仅打印）两档；基线先落盘再打印
- **当前数据**：官方 **2.1.302**，本 fork 宣称 2.1.280
- **用法**：`go run ./tools/changelog-watch`；`-update` 接受新基线

### T-04 内容闸 ✅

- **产物**：`internal/antidetect/content/`、`claude_content_gate.go`、execute/stream 接入
- **行为**：只看最新一条 user 正文，跳过 `tool_result` 与历史轮次；命中 → 403 + `StatusCode()` + `IsRequestScoped()`（不冷却凭证），**不选号不打上游**
- **验收**：13 条正样本 + 10 条正常工程提问负样本 + 边界用例
- **开关**：`CPA_ANTIDETECT_CONTENT_GATE=off`

### T-05 节流 ✅

- **产物**：`internal/antidetect/throttle/`、`claude_throttle.go`
- **行为**：每凭证令牌桶，时钟可注入；等待有上限，超时回 429 而非阻塞；取消时退还令牌
- **验收**：5 条（默认不节流 / burst / 预算耗尽 429 / 取消 / key 回落）
- **开关**：`CPA_ANTIDETECT_RATE`、`_BURST`、`_MAX_WAIT`

### T-06 device pool 可放大 ✅

- **产物**：`internal/auth/claude/identity.go`
- **行为**：池大小 `1`（默认，等于原行为）→ 可 1–8；`SelectDeviceID` 按 `sha256(session) % len` 选，同会话恒定；**已有池不重建**
- **验收**：4 条（默认值 / 非法值回落 / 会话稳定 / 单元素池忽略会话）
- **开关**：`CPA_ANTIDETECT_DEVICE_POOL`

### T-07 审计日志 ✅

- **产物**：`internal/antidetect/audit/`
- **行为**：一条决策一行结构化日志；凭证 ID 一律 `sha256` 前 12 位，日志不出现原值
- **已接入六处**：
  | 事件 | 时机 | 决策值 |
  |---|---|---|
  | `version_guard` | 启动时版本漂移（放行/拒绝） | `unknown` / `blocked` |
  | `content_gate` | 内容规则命中 | `blocked` |
  | `cloak_gate` | 客户端状态未知、跳过改写 | `unknown_client` |
  | `egress_binding` | 出口未绑定或出口被占 | `blocked` |
  | `pacing` | 节流等待超预算 | `blocked` |
  | `quota_window` | 窗口越过阈值（观测/拦截） | `paced` / `blocked` |

- **过程中发现并修掉的一个真 bug**：仓库的自定义 `LogFormatter` 只渲染白名单字段（`logFieldOrder`），审计字段本来会被静默丢弃——即"日志里看得到一条记录，但看不到任何证据"。已把审计字段加进白名单，并加了回归测试 `TestAuditFieldsSurviveLogFormatter`。

### T-08 抓包工具链 ✅

- **产物**：`tools/capture/dump_claude_request.py`、`ja4.sh`、`verify-cch/`、`hello-probe/`
- **行为**：mitmproxy 插件导出 header 顺序 + body（脱敏）；pcap 出 JA4；`verify-cch` 复算摘要并给出下一步
- **验收**：9 条单测（投影稳定性 / cch 复位 / model 置空 / 嵌套数组剥离 / 身份提取 / 不匹配时报错）

---

## 三、待办

### T-09 出口绑定 ✅

- **产物**：`internal/antidetect/egress/`、`claude_egress.go`、execute/stream 各 1 处接入
- **行为**：凭证通过 `egress_id`（attributes 优先、metadata 回落）声明出口；强制模式下未绑定 → **409 拒绝调度**，不回退直连；同一出口被两个凭证占用 → 第二个被拒；凭证换出口时自动释放旧出口
- **验收**：11 条包测试 + 5 条接入测试（含共享出口被拒、metadata 回落、409 状态码）
- **开关**：`CPA_ANTIDETECT_EGRESS`（JSON），默认不强制
- **未做**：IP 漂移检测与地理一致性校验——需要出口侧回报真实 IP，属于下一层

### T-10 5h/7d 窗口建模 ✅

- **产物**：`internal/antidetect/window/`（模型 + 快照存储）、`claude_window.go`（观测 + 准入）
- **行为**：
  - 三态判定（`<0.90` 正常 / `0.90–0.95` 降级 / `>0.95` 熔断）
  - 官方"残留 100% 态"当新窗口：`resets_at` 已过期时 utilization 按 0 算
  - `5h` / `7d` 双窗口解析，epoch 与 RFC3339 都吃，脏值丢弃；`Worst()` 取更严一档
  - **响应头 → 下一次请求的准入**：`StateBlocked` 时直接 429，不再打上游
  - 快照 TTL 10 分钟：过期数据不参与判定，避免把已重置的号永久停掉
- **验收**：13 条包测试（阈值/残留态/解析/脏值/TTL/重置/隔离）+ 5 条接入测试（含"观测后下一次请求被拦"）
- **开关**：`CPA_ANTIDETECT_WINDOW_GATE=off`
- **不需要做**：原计划要给 `/api/oauth/usage` 轮询限频，但本仓生产代码里**没有后台轮询**，配额只来自响应头，因此没有这项待办

### T-11 遥测 ⛔

- **决定**：不做。官方在第三方 provider / 网关 / 自带 OAuth 部署上**主动关闭**遥测，"没有遥测"是合法状态；伪造反而制造新证据

---

## 四、抓包收口（T-12）✅ 已于 2026-10-18 完成

真机抓包结果（本机 CLI 2.1.280，直连，未经任何反代）：

| 项 | 线上实测 | 台账原值 | 结论 |
|---|---|---|---|
| `cc_version` | 2.1.280 | 2.1.280 | 一致 |
| `cch` | `01e0f` | — | 用 seed `0x4D659218E32A3268` **复算 MATCH** |
| stainless package / runtime | 0.112.1 / v26.3.0 | 同 | 一致 |
| stainless os / arch | MacOS / arm64 | 同 | 一致 |
| `cc_entrypoint` | sdk-cli | — | 与 `(external, sdk-cli)` UA 自洽 |
| TLS ClientHello | 17 cipher + 前 13 个扩展顺序 | spec 同 | **逐项一致** |

只有 TLS 的 `padding` 与 `pre_shared_key` 未出现——前者取决于 hello 长度，后者只在会话复用时出现，属预期。

**据此落地的改动**：`CCHSeeds` 增加 `2.1.280`；`TLSHelloFrom` / `CCHFrom` 从 `2.1.220` 升到 `2.1.280`；`utls_client.go` 注释更新为"已于 2.1.280 复验"。

> 注：上游 CHANGELOG 已到 2.1.302，但真实在跑的客户端是 2.1.280——客户端版本天然滞后。台账对齐的是**真实客户端**，不是最新发布号。

原流程保留在 [tools/capture/README.md](../../tools/capture/README.md)（下次官方改协议时照跑一遍即可）。

### 命令（三步，约 15 分钟）

```bash
# 1) 请求形状
mitmproxy -s tools/capture/dump_claude_request.py --save-stream-file /tmp/cc.flow
#    另开终端，让真客户端走代理，问一句就退出：
NODE_USE_ENV_PROXY=1 HTTPS_PROXY=http://127.0.0.1:8080 \
NODE_EXTRA_CA_CERTS="$HOME/.mitmproxy/mitmproxy-ca-cert.pem" claude

# 2) 解析 + 验证（一条命令给出结论）
go run ./tools/capture/verify-cch -file capture-out/<name>.json

# 3) TLS 指纹（可选，验证 uTLS spec 是否过期）
sudo tcpdump -i any -w /tmp/cc-cli.pcap 'host api.anthropic.com and tcp port 443'
tools/capture/ja4.sh /tmp/cc-cli.pcap
```

### 抓到之后要做的三件事（已脚本化，无判断）

1. 把 `verify-cch` 打印的 `adopt version` 写进 `Canonical().Version`
2. 若 `verdict: MATCH` → 把该版本的 seed 加进 `CCHSeeds`；若 `MISMATCH` → **不要签**，把 `CCHFrom` 留在旧版本并在 PLAN 记一笔
3. 把 `TLSHelloFrom` / `CCHFrom` 改成新版本 → `TestAdvertisedIdentityIsInternallyConsistent` 自动转绿

---

## 五、已知限制

| 限制 | 影响 |
|---|---|
| 版本台账需随官方发版复验 | 每次 CLI 大版本更新后跑一次 capture 流程，否则会重新漂移 |
| 窗口判定是准入而非调度 | T-10 在请求发出前拦截，不会主动把流量切到别的号 |
| 内容闸词表是内置的 | 误伤要改代码或 `CPA_ANTIDETECT_CONTENT_GATE=off`，尚无热加载 |
| 审计未落盘 | 走标准日志管道，尚无独立 rotate/retention |

---

## 六、验证命令

```bash
go build ./...                                   # 必须通过
go vet ./internal/antidetect/... ./tools/...     # 必须通过
go test ./...                                    # 全绿
go run ./tools/changelog-watch                   # STRONG 命中会返回 1
```

---

## 七、环境变量

| 变量 | 默认 | 作用 |
|---|---|---|
| `CPA_ANTIDETECT_ALLOW_VERSION_DRIFT` | 未设（拒绝启动） | 允许版本漂移继续运行 |
| `CPA_ANTIDETECT_CONTENT_GATE` | 开 | `off` 关闭内容闸 |
| `CPA_ANTIDETECT_RATE` / `_BURST` | `0` / `1` | 每凭证 QPS 与突发量；0 = 不节流 |
| `CPA_ANTIDETECT_MAX_WAIT` | `30` | 节流等待上限（秒），超时 429 |
| `CPA_ANTIDETECT_DEVICE_POOL` | `1` | 新凭证 device 池大小（1–8） |
| `CPA_ANTIDETECT_EGRESS` | 未设（不强制） | 出口策略 JSON：`{"enforce":true,"pools":{"hk-1":{"proxy_url":"..."}}}` |
| `CPA_ANTIDETECT_WINDOW_GATE` | 开 | `off` 关闭额度窗口准入 |
