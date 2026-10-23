# CLIProxyAPI Fork · Claude 防封优化改造计划

> 基线：`router-for-me/CLIProxyAPI` @ `911f49c`（2026-10-17）
> 分支：`feat/antidetect`
> 本计划只覆盖 **Claude 链路**。Gemini / Codex / Grok / Kimi / Meta / Devin 不在范围内。

---

## 进度看板

| 项 | 状态 | 产物 |
|---|---|---|
| P0-1 版本一致性守护 | 🟡 骨架完成，等抓包收口 | `internal/antidetect/versionguard/`、启动接入 `cmd/server/main.go` |
| P0-2 四闸 fail-safe | ✅ 完成 | `helps/claude_client_state.go`、`claude_executor_cloaking.go`、`claude_executor_request.go` |
| P0-3 CHANGELOG 追踪 | ✅ 完成 | `tools/changelog-watch/`（强/弱关键词两档，基线已落盘；当前官方 2.1.302） |
| P1-1 内容闸 | ✅ 完成（默认开，可关） | `internal/antidetect/content/`、`claude_content_gate.go`、execute/stream 各 1 处接入 |
| P1-2 节流 | ✅ 完成（默认关闭） | `internal/antidetect/throttle/`、`claude_throttle.go`、execute/stream 各 1 处接入 |
| P1-3 出口绑定 | ⬜ 未开始 | — |
| P2-1 device pool | ✅ 完成（默认 1，可放大） | `internal/auth/claude/identity.go`（会话稳定映射） |
| P2-2 窗口建模 | ⬜ 未开始 | — |
| P2-3 遥测 | ⛔ 决定不做（见 §4.2） | — |
| P3 可观测 | 🟡 包完成，两处已接入 | `internal/antidetect/audit/`（凭证哈希、内容闸、cloak 决策） |

### 运行时开关（全部默认保守）

| 变量 | 默认 | 作用 |
|---|---|---|
| `CPA_ANTIDETECT_ALLOW_VERSION_DRIFT` | 未设 = 拒绝启动 | 允许版本漂移时继续 |
| `CPA_ANTIDETECT_CONTENT_GATE` | 开 | `off` 关内容闸 |
| `CPA_ANTIDETECT_RATE` / `_BURST` | `0` = 不节流 | 每凭证 QPS 与突发量 |
| `CPA_ANTIDETECT_MAX_WAIT` | `30` 秒 | 等待上限，超时返回 429 而非阻塞 |
| `CPA_ANTIDETECT_DEVICE_POOL` | `1` | 新凭证的 device 池大小（1–8） |

**当前测试状态**：`executor` / `helps` 全绿；`versionguard` 只有 `TestAdvertisedIdentityIsInternallyConsistent` 为红，那是待抓包修复的 bug 本身。

### P0-2 落地时的两个决定（重要）

1. **三态判定收窄了。** 最初版本把"有任何 CC 信号但未确认"都当作 Unknown 透传，结果打挂了上游两个刻意设计的测试：
   - `CopiedVSCodeAgentSDKHeadersWithoutMetadataAreCloaked`（native entrypoint 但缺 `metadata.user_id` = 抄 header 的假客户端 → 应当 cloak）
   - `AgentSDKEntrypointWithStrongSignalsUsesCLICloak`（`sdk-ts` 通用 Agent SDK → 应当 cloak 成 CLI）

   收窄后的规则：**只有"entrypoint 无法识别"才透传**。已知 native 但缺闸 = 假客户端，照旧 cloak；已知非 native（sdk-ts/sdk-py）= 产品行为，照旧 cloak。这样既保住上游意图，又覆盖真正的风险场景——**上游每版都可能新增 entrypoint**（映射表已有 27 个），新值落到我们这里就是"无法识别"。

2. **光改 cloak 不够，UA 还有第二处覆写。** `applyClaudeHeaders` 的"未确认"分支会无条件重写 UA + 4 个 stainless 头（`claude_device_profile.go:605-649`）。新增 `ApplyClaudeCallerDeviceHeaders`：保留调用方身份，只补缺失值。验收测试 `TestClaudeExecutor_UnknownEntrypointIsNotCloaked` 断言 UA 原样、无 billing 头、无 CLI 身份块、不注入对话轮。

   > 注意：验收标准是"不伪造 CLI 身份"，**不是字节完全一致**。system 字符串转块数组、补 `cache_control` 属于语义中立归一化，允许发生。

---

## 0. 目标与非目标

### 目标

| # | 目标 | 判据 |
|---|---|---|
| G1 | 消除"一条请求即可判定"的硬伤 | 版本/指纹四层一致，启动即断言 |
| G2 | 降低"真客户端被改写"的风险 | 未知 entrypoint 走透传而非 cloak |
| G3 | 补齐内容层 | 蒸馏/越狱命中可拦，误伤可放 |
| G4 | 补齐行为层 | 有节流、有窗口预算、有作息 |
| G5 | 补齐可观测性 | 封号后能回溯到具体信号 |

### 非目标（明确不做）

- ❌ 不重写 TLS 栈。保留 uTLS，但把 spec 改成**抓包驱动生成**，不靠手抄。
- ❌ 不做 VM 级隔离（那是另一套架构，成本量级不同）。
- ❌ 不解决"一个号几个人用"——这是运营决策，代码层面只能降低证据强度。
- ❌ 不改非 Claude provider 的任何行为。

---

## 1. 现状基线（代码事实，非推测）

| 编号 | 问题 | 证据 | 影响 |
|---|---|---|---|
| **B1** | 版本号分裂：UA/`cc_version` 报 `2.1.280`，CCH/TLS/OAuth spec 是 `2.1.220` | `helps/claude_device_profile.go:23`、`cloaking.go:315` vs `claude_signing.go:218`、`helps/utls_client.go:199-202`、`auth/claude/utls_transport.go:109-112` | 一条请求可判，且每天重复 |
| **B2** | 四闸判定直接控制 cloak，误判会放大到全链路 | `helps/claude_client_detection.go:138-161` → `cloaking.go:1635-1643` | 上游微调一个字段 → 整批真客户端被改写 |
| **B3** | 无内容层 | 全仓无内容分类；`cloak` 只做零宽字符混淆（`config_types.go:579` 自述 "bypass certain content filters"） | 唯一"一封终封"的层裸奔 |
| **B4** | Claude 路径无出站节流/抖动 | claude 路径 `time.Sleep` 零命中 | 请求间隔是机器分布 |
| **B5** | 无 IP↔账号绑定、无地理一致性校验 | `config.example.yaml:224,530`；`timezone` 只用于 currentDate（`cloaking.go:1239-1269`） | 一个号多出口直接暴露 |
| **B6** | device pool 容量 1，`SelectDeviceID` 忽略 sessionID | `internal/auth/claude/identity.go:11-15,266-277` | 等于不轮换 |
| **B7** | 无 5h/7d 窗口建模，只有被动配额观测 | `sdk/cliproxy/auth/quota_signals.go` | 窗口打满后仍硬冲 |
| **B8** | 完全不发客户端遥测 | 无 `tengu_*` 相关代码 | 长期静默是弱信号 |
| **B9** | 无封号回溯能力 | request log 不落 UA/entrypoint/device/出口 IP 的组合 | 封了只能猜 |

---

## 2. 仓库与分支策略

### 2.1 远端

```
upstream  https://github.com/router-for-me/CLIProxyAPI.git   # 只读，只 fetch
origin    <你的 fork>                                         # 只推自己的分支
```

```bash
git remote rename origin upstream
git remote add origin git@github.com:<你>/CLIProxyAPI.git
git push -u origin feat/antidetect
```

### 2.2 分支

| 分支 | 用途 |
|---|---|
| `main` | 纯 upstream 镜像，永不提交 |
| `feat/antidetect` | 集成分支 |
| `p0/versionguard` … `p3/observability` | 单项补丁，一个 PR 一件事 |

### 2.3 目录约定（关键）

**新代码全部进新包，不改原文件。** 必须改的原文件，控制在个位数，且改动行数 < 20 行/文件。

```
internal/antidetect/          # 新增：本 fork 的全部原创逻辑
  versionguard/               # P0-1
  detect/                     # P0-2
  content/                    # P1-1
  throttle/                   # P1-2
  egress/                     # P1-3
  window/                     # P2-2
  audit/                      # P3
```

理由：上游每天提交，改原文件 = 每天都可能冲突；加新包 = 几乎不冲突。

### 2.4 跟上游的节奏

- 每周 `git fetch upstream && git rebase upstream/main`（集成分支）
- 每月一次全量回归
- **冲突热点监控**：`claude_*.go`、`helps/claude_*.go` 每次变动都要看——那就是上游在改协议，你的常量和逻辑要跟着动

**实测上游节奏（截至 `911f49c`）**：

| 指标 | 数值 |
|---|---|
| 最近 30 天总提交 | **409** |
| 其中触及 `claude_*.go` / `helps/claude_*.go` | **58**（≈ 2 次/天） |

这个数字决定了 fork 的唯一可行形态：**只改常量、不改逻辑**。

| 改动类型 | 冲突概率 | 解决成本 | 结论 |
|---|---|---|---|
| 改一个字符串/数字常量（如版本号） | 高 | 秒级（一行冲突） | ✅ 允许 |
| 在新包里加功能 | 极低 | 无 | ✅ 首选 |
| 在原文件里加分支/改判断 | 高 | 高（逻辑纠缠） | ❌ 禁止 |
| 在原文件里插入调用 | 中 | 低 | ⚠️ 限量（每文件 ≤ 1 处） |

---

## 3. 改造清单

每项格式：**目标 → 落点 → 做法 → 验收 → 回滚**。

### P0-1 版本一致性守护 `internal/antidetect/versionguard`

**目标（G1）**：让"宣称的版本"和"实际的指纹"永远对得上，对不上就拒绝启动。

**落点**：
- 新增 `internal/antidetect/versionguard/profile.go`、`guard.go`
- 改 5 处常量引用：`helps/claude_device_profile.go:23,591-599`、`cloaking.go:315,416-417`、`claude_signing.go:218`、`helps/utls_client.go:199-202`、`auth/claude/utls_transport.go:109-112`

**做法**：

```go
// 唯一事实源
type Profile struct {
    Version        string // 2.1.280
    PackageVersion string // 0.112.1
    RuntimeVersion string // v26.3.0
    OS, Arch       string // MacOS / arm64
    TLSHelloFrom   string // 抓包来源版本，必须 == Version
}
```

1. 启动时 `guard.Assert()`：`UA.Version == cc_version == CCHComment.Version == TLSHelloFrom`
2. 不一致 → **返回 error 让 `cmd/server` 启动失败**（仓库规范禁止 `log.Fatal`）；提供 `antidetect.versionguard.allow_drift: true` 逃生阀，此时只 `logrus.Warn`
3. 启动日志打印指纹摘要（一行，便于比对）
4. 单测断言四处常量相等

**增补（来自外部实现，见 §4.6 #2）——未知版本的安全降级**：

CCH seed 不应是全局常量，而应**按 `major.minor.patch` 查表**：

```go
var CCHSeeds = map[string]uint64{
    "2.1.177": 0x4d659218e32a3268,
    "2.1.280": 0x4d659218e32a3268, // 待抓包确认是否仍然相同
}
```

- 命中 → 正常签名
- **未命中 → 不要给一个"自信但错误"的哈希**，改为随机值并打 WARN

理由：错误的固定哈希是一个稳定、可聚类的特征，比随机更容易被单独挑出来。这是 dario 的取舍，我们采纳。

> 代码内注释一律英文（仓库规范）；本文件为内部文档，保留中文。

**验收**：故意把 TLSHelloFrom 改成别的版本 → 启动失败；改回 → 通过。

**回滚**：配置 `antidetect.versionguard.enforce: false`。

---

### P0-2 四闸 fail-safe：默认透传 `internal/antidetect/detect`

**目标（G2）**：判定不确定时，**不要改写**，宁可透传。

**落点**：`helps/claude_client_detection.go:138-161`、`cloaking.go:1635-1643`

**做法**：
1. 把二值 `Confirmed bool` 扩成三态：`Confirmed / Unknown / Rejected`
2. `Unknown` 时：**不 cloak**，走透传，并打一条 `antidetect.detect.unknown` 日志（带 UA/entrypoint）
3. `Rejected` 才 cloak（保持现有行为）
4. 新增指标：`rejected_ratio`，异常升高即告警

**增补（见 §4.6 #9）——confirmed 时头部零改写**：

判定为真客户端后，不只关闭 body cloak，**头部也要原样转发**。dario 实测过旧版本实现"13 个 CC 身份头里 12 个被替换、1 个丢弃、0 个转发"——那等于把真客户端改造成更可疑的形态。验收时用真 CLI 抓包逐头对比。

**动机案例（§4.6 #14）**：某网关把 UA 改写成非 `claude-cli/*`，导致下游误判为"非 CC"并走 mimic 路径，而 `metadata.user_id` 又因正则不匹配旧格式被原样放行，最终 **token 属于账号 A、`user_id` 属于账号 B**，首个请求即封。这正是"判错 + 继续改写"的复合事故。

**验收**：用 3 条真 CLI 请求（`cli` / `sdk-cli` / `claude-vscode`）跑回归，全部 `Confirmed`；伪造一条未知 entrypoint，走 `Unknown` 且不被改写。

**回滚**：`antidetect.detect.mode: strict|legacy`。

---

### P1-1 内容闸 `internal/antidetect/content`

**目标（G3）**：挡住唯一"一封终封"的层。

**落点**：优先**插件**（`request-interceptor` + `BeforeAuth`，见 `examples/plugin/request-lifecycle`）；如果插件能力不够，再 in-repo 挂到 `handlers_interceptors.go`。

**做法（最小可用，三层）**：

| 层 | 实现 | 默认 |
|---|---|---|
| 1 硬正则 | 蒸馏模板、越狱话术、NSFW 关键词；扫最新一条 user 正文，跳过 `tool_result` | 开 |
| 2 相似度 | 对已拒答正文做 MinHash（shingle ≥ 24 字符；正文 < 512 字符跳过相似度，只做精确匹配） | 开 |
| 3 决策模型 | 可选，外部 HTTP 打分；**未配置 = 这道闸不存在，启动时打 WARN** | 关 |

**关键设计**：
- 命中返回 403 + 自定义 body，**不消耗上游额度**
- 命中记 device 级封禁（可选），并落审计日志
- **误伤处理**：命中详情落盘，支持"一键放行本次"，不做永久白名单

**验收**：准备 50 条蒸馏模板 + 50 条正常编程提问，召回 ≥ 90%，误伤 ≤ 2%。

**回滚**：`antidetect.content.enabled: false`（整层）。

---

### P1-2 出站节流与抖动 `internal/antidetect/throttle`

**目标（G4）**：让请求间隔看起来是人在用，不是压测。

**落点**：`sdk/cliproxy/auth/conductor_execution.go` 调度前；每凭证独立桶。

**做法**：
1. **Token bucket**：按凭证限 QPS + 并发上限（默认 1–2）
2. **抖动**：放行前 `sleep(rand(0, 300ms))`；同会话内不做抖动（避免拖慢真实交互）
3. **节律窗口**：可选作息（`active_hours`），窗口外不调度该凭证
4. **退避**：429 用 `Retry-After` + 指数退避 + 抖动，**禁止立即重试**

**验收**：压测 10 QPS 时实际出站 ≤ 配置值；间隔分布直方图无"整秒对齐"尖峰。

**测试约束**：节流单测不得依赖真实 `time.Sleep`（仓库规范：跨平台定时器精度问题），必须注入可控时钟（`nowFunc`）。

**回滚**：`antidetect.throttle.enabled: false`。

---

### P1-3 出口绑定与地理一致性 `internal/antidetect/egress`

**目标（G4/G5）**：一个账号永远只走一条线。

**落点**：`internal/config` + auth metadata + 调度前校验。

**做法**：
1. `auth.json` 增加 `egress_id`（逻辑 ID），`antidetect.egress.pools[egress_id]` 配 `proxy_url` / `country` / `timezone`
2. 调度前断言：`auth.egress_id` 已配且在池中；未配 → **拒绝调度**（fail closed），不回落直连
3. **漂移检测**：记录首见出口 IP，变化超阈值 → 告警并暂停该凭证
4. **地理一致性**：`timezone` / `country` 与出口 IP 的 GeoIP 不一致 → WARN（不阻断，先观察）

**验收**：故意给两个账号配同一 `egress_id` → 告警；换 IP 后 → 暂停 + 告警。

**回滚**：`antidetect.egress.enforce: false`。

---

### P2-1 device pool 1→N，按 session 选择

**目标（G2）**：不同会话看起来像不同的合法会话，而不是"一台机器一个设备反复横跳"。

**落点**：`internal/auth/claude/identity.go:11-15,266-277`

**做法**：
1. `ClaudeDevicePoolSize` 从 1 改为可配（默认 1，最大 4）
2. `SelectDeviceID` 改为：`hash(sessionID) % len(pool)`，**同 session 稳定**
3. 池持久化到 auth metadata，重启不丢

**验收**：同 session 多次请求 device 不变；不同 session 命中不同 device；重启后一致。

**回滚**：配置回 1，行为与当前完全一致。

---

### P2-2 5h/7d 窗口建模 `internal/antidetect/window`

**目标（G4）**：额度逼近上限时主动降速，而不是被 429 打回来。

**落点**：新增包，接 `quota_signals.go` 的被动观测。

**做法**（思路自研，不抄代码）：
1. 从响应头 `anthropic-ratelimit-unified-5h-*` / `7d-*` 读 utilization + `resets_at`
2. 处理"官方残留 100% 态"：`resets_at` 已过期但 utilization=1 时，视为新窗口（不要误判满额）
3. 三态闸：`< 0.90` 正常调度；`0.90–0.95` 降级（只接小请求）；`> 0.95` 熔断
4. 面板/日志暴露每个凭证的窗口状态与倒计时

**增补（见 §4.6 #7）——轮询本身也是被检测面**：

`/api/oauth/usage` 不是免费接口。多账号 + 多面板刷新会把它打爆，返回 429，而 429 又会被误读成"账号受限"。

| 参数 | 建议值 | 来源 |
|---|---|---|
| 结果缓存 TTL | 300s | 9router 实测值 |
| per-token 429 冷却 | 180s（按 token 记） | 同上 |
| 429 影响范围 | **只影响配额查询，不影响对话** | 同上 |

另外该端点要带 `anthropic-beta: oauth-2025-04-20` 与 CLI 的 UA——轮询请求的指纹和被检测面是同一套。

**验收**：模拟 utilization 曲线，验证三态切换；与官方 `/api/oauth/usage` 对齐误差 < 5%。

**回滚**：`antidetect.window.enabled: false`。

---

### P2-3 遥测层：**结论是不做**（原计划为"补全"，现降级）

**依据**（见 §4.2）：官方 `mods/telemetry` 明确写了——遥测在第三方 provider、云网关、自带 OAuth URL 的部署上**主动关闭**。所以"没有遥测"是合法部署的正常状态。

**伪造的代价**：每行需带 install device id + 账号 id + 一致的 `env` 块 + build time。只要一处不一致，就从"正常静默"变成"遥测在撒谎"，后者是明确异常。

**保留的例外**：如果将来确实要做，必须先满足三个前提：
1. `env` 块由**真实探测**生成，进程指标用连续曲线而非每次随机
2. device id 与 auth 里的 device 一致
3. A/B 验证（各 20 号 × 14 天）证明有正收益

否则不做。**默认关闭，且不写实现。**

---

### P3 可观测性与封号回溯 `internal/antidetect/audit`

**目标（G5）**：下次封号，10 分钟内能回答"是哪条信号"。

**落点**：新增包 + 现有 request log。

**必须在 payload 规则之前或之后都不改业务语义**：仓库规范要求「payload 配置是发往上游前的最后一道语义屏障」。内容闸只做**准入判定（放行/拒绝）**，不做字段改写；若需要改写，必须放在 payload 规则**之前**完成。

**每条请求必落**：

| 字段 | 用途 |
|---|---|
| `request_id` / `credential_id` | 关联 |
| `client_ip` / `egress_ip` / `egress_id` | 多出口检测 |
| `user_agent` / `entrypoint` / `detect_state` | 客户端形态 |
| `device_id` / `account_uuid` / `session_id` | 身份漂移 |
| `betas` / `model` / `system_hash` | 协议形状 |
| `quota_5h` / `quota_7d` / `reset_at` | 窗口位置 |
| `content_gate`（命中/放行/未过闸） | 内容层 |
| `upstream_status` / `upstream_refusal` | 上游反馈 |

**查询**：按 `credential_id` + 时间窗聚合，输出"该号在过去 24h 出现过的不同 IP / UA / device 数量"——这三个数就是封号自检表。

---

## 4. 从开源 commit 提炼的策略

### 4.1 最重要的发现：官方 CHANGELOG 才是权威变更源

`anthropics/claude-code` 是公开仓库，**每天**更新 `CHANGELOG.md`。这是所有指纹常量的源头。

| 事实 | 数值 | 含义 |
|---|---|---|
| 官方最新版本（CHANGELOG 头部） | **2.1.302** | — |
| 本 fork 基线宣称 | 2.1.280 | **落后 22 个版本** |
| vm2api 宣称 | 2.1.296 | 落后 6 个版本 |
| CHANGELOG 总长度 | 9193 行 | 每次改动都可追溯到版本 |

**结论：不跟踪 CHANGELOG 的指纹维护是盲目的。** 据此新增工作项 **P0-3**。

---

### P0-3 官方变更追踪 `tools/changelog-watch`

**目标**：把"官方改了什么"变成一条每天自动跑的流水线，而不是靠人偶尔想起。

**做法**：
1. 每天拉 `https://raw.githubusercontent.com/anthropics/claude-code/main/CHANGELOG.md`
2. 与上次快照 diff，按关键词打标签：

| 关键词 | 影响 |
|---|---|
| `telemetry` / `analytics` | P2-3 遥测层 |
| `beta` / `header` / `User-Agent` | P0-1 指纹层 |
| `prompt cache` / `cache` | 协议层 cache 布局 |
| `classifier` / `safety` / `policy` | 内容层与判定 |
| `gateway` / `third-party` / `Bedrock` / `Vertex` | 官方对"非官方部署"的容忍边界 |
| `subagent` / `compaction` | 会话层形状 |
| `system prompt` | persona 模板 |

3. 命中即生成一条待办（GitHub Issue 或本地 TODO），附 CHANGELOG 原文行
4. 每周人工过一遍，决定是否要重新抓包

**成本**：约 100 行脚本 + 一个 cron。**收益**：把"版本漂移"从被动变主动。

---

### 4.2 官方 `mods/telemetry`：遥测层的真实规格

官方仓库把内置插件源码也公开了，`mods/telemetry/` 就是一个完整实现。它回答了 CPA 完全空白的那一层：

| 维度 | 官方实现（`mods/telemetry/README.md`） |
|---|---|
| 事件名 | `tengu_plugin_<event>`（插件）、`tengu_feature_<kind>`（功能使用） |
| 每行携带 | event id、**install device id**、**账号 id**、session id、model、client type、entrypoint、interactivity |
| `env` 块 | platform/arch（一次 `uname`）、terminal、shell、包管理器、运行时、CI/GitHub Actions、远程容器、部署方式、Linux 发行版与内核、WSL、工作目录的版本控制、引擎 version/base version/build time |
| 发送 | 批次 POST 到 event-logging ingest，**用会话自己的凭证**；首行后几秒、满 100 行、会话结束各一次；5xx/超时/429 重试一次 |
| 关闭条件 | `DISABLE_TELEMETRY` / `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` / `DO_NOT_TRACK` / 测试运行 / **第三方 provider（Bedrock、Vertex、Foundry）** / **云网关** / **自带 OAuth URL 的部署** |

**这个表推翻了一个流行假设。**

> ❌ 旧判断：「不发遥测 = 异常信号」→ 所以要伪造遥测。
> ✅ 实际情况：**官方在第三方 provider、网关、自带 OAuth 的部署上主动不发遥测。** "没有遥测"在合法部署里是**正常状态**，不是异常。

而且伪造的代价极高：每一行都要带 install device id + 账号 id + 一致的 `env` 块 + 引擎 build time。任何一处对不上，就从"没有遥测"（正常）变成"遥测在撒谎"（明确异常）。

vm2api 正是踩了这个坑：它的 `buildProcessJson` **每次采样独立随机**（`telemetry-env.mjs:223-239`），相邻两次 rss 会从 300MB 跳到 499MB——这种时间序列在分析侧一眼就是假的。

**结论：P2-3（遥测补全）降级为「不做」，理由写进第 8 节。**

---

### 4.3 官方 `mods/types/claude-code.d.ts`：可当行为字典用

`mods/types/claude-code.d.ts`（517 KB）是引擎的完整类型声明，公开可读。它的价值不在遥测，而在**校验**：当你要确认某个字段、事件名、beta 的拼写与取值时，这是比逆向二进制更可靠的来源。

用法：把它拉下来，作为 `tools/changelog-watch` 的第二个输入，diff 出"新增/删除的字段与事件"。任何一格变化，都意味着 wire 可能变了。

---

### 4.4 上游节奏的量化（已并入 §2.4）

| 指标 | 数值 |
|---|---|
| 最近 30 天总提交 | 409 |
| 触及 `claude_*.go` / `helps/claude_*.go` | 58（≈ 2 次/天） |

这个数字是"只改常量、不改逻辑"这条纪律的依据。

---

### 4.5 同类项目扫描

| 项目 | ★ | 语言 | 与本 fork 的关系 |
|---|---|---|---|
| `NookMux/AntiHub-ALL` | 415 | Python | 多平台 2API，方向不同（网页逆向），不参考 |
| `Hello-Application-XH/HelloGML` | 336 | TypeScript | 网页版转 2API，非 CLI 指纹方向 |
| `askalf/dario` | — | TS | **最重要的参照物**：唯一公开的 cch 真算法 + 真实抓包 fixture |
| `decolua/9router` | — | TS | **反向教材**：cch 是 sha256 冒充的，但节奏工程值得学 |
| `wtfwhs/tengu-decoded` | — | — | 遥测事件目录（1163 个 `tengu_*`）+ 端点表 |
| `PlaygrndLabs/sub2api` | — | — | 空壳仓库，32 个文件零命中，不是目标 |

---

### 4.6 外部实现的采纳决定

调研结论按"我们现状 → 决定"落地。

| # | 发现 | 来源 | 我们现状 | 决定 |
|---|---|---|---|---|
| 1 | cch 必须对**投影后**的 body 算：`cch=00000` 复位 → **model 值置空（保留 key）** → 删 `fallbacks`/`fallback_credit_token`/`max_tokens` → compact JSON，再 `xxh64 & 0xFFFFF` | `askalf/dario` `src/cch.ts` | **已一致**：`claude_signing.go:279-512` 的归一化编辑做的就是这套投影 | ✅ 已验证，不改 |
| 2 | seed 按 `major.minor.patch` 查表；**未知版本回退随机**，理由是"一个自信但错误的哈希比随机更容易被单独挑出来" | 同上 | 全局单常量，无版本表 | ✅ **采纳**（并入 P0-1） |
| 3 | 真实抓包里 `metadata.user_id` 的 `account_uuid` **可以是空串** | dario fixture `cch-cc2.1.177.json` | 我们强制合成非空 UUIDv5 | ⚠️ **改为可配**：默认保留合成，但允许 `account_uuid: ""` 形态，避免"过于规整"本身成为信号 |
| 4 | `context_management.edits[clear_thinking_20251015]` 与 `thinking:{type:"adaptive"}` 同生共死；Haiku / ≤4.5 会 400 | dario `cc-template.ts:2481` | 已注入 `context_management`（`cloaking.go:1488-1528`），thinking 门控在 `request.go` | ✅ 已验证；补一条**模型代际门控**回归测试 |
| 5 | `ttl:1h` 必须配 `extended-cache-ttl-2025-04-11` beta；客户端 beta 里没有就不镜像；半转发会 400 | dario；`issue #678` | 已有配对逻辑（`claude_executor_execute.go:182`） | ✅ 已验证；补"不镜像"分支的测试 |
| 6 | `count_tokens` 有**专属 beta** `token-counting-2024-11-01` | `tengu-decoded` | 已实现（`claude_executor_tokens.go:206`） | ✅ 已验证 |
| 7 | `/api/oauth/usage` 要限频：`USAGE_CACHE_TTL_MS=300000` + per-token `429` 冷却 `180000`，429 只影响配额不影响对话 | `9router` `usage/claude.js` | 无（被动观测） | ✅ **采纳**（并入 P2-2） |
| 8 | 响应侧限流头只回传 `retry-after` / `x-should-retry` / `anthropic-ratelimit-*` | `9router` `upstreamHeaders.js` | 已剥网关头（`header_filter.go`），无白名单逻辑 | ⚠️ 评估后决定：**不做**（下游是 API 客户端，不是真 CLI，转发反而多一层形状） |
| 9 | 真 CC 走 passthrough 时 **13 个身份头原样转发**；dario 实测旧版本 12 个被替换、0 个转发 | dario `issue #885` | confirmed 时 cloak 关闭，但头部仍部分重写 | ✅ **采纳**（并入 P0-2）：confirmed 时头部应零改写 |
| 10 | 抓包模板**禁止**回放宿主属性（`x-stainless-os` / `x-stainless-arch`），否则 Linux 主机会宣称 `Windows` | dario `issue #854` | 我们硬编码 `MacOS`/`arm64` | ✅ **采纳**：改为"Profile 内部自洽 + 启动断言"，不回放宿主值 |
| 11 | **decoy 工具注入**：追加 20 个真 CC 工具名（`Task`、`Bash`…，描述统一 "currently unavailable"），客户端工具加 `_ide` 后缀并在响应侧还原 | `9router` `claudeCloaking.js` | 无 | ⚠️ **先评估不实现**：收益不确定，双向 decloak 的流式还原一旦漏掉一个，客户端直接 unknown tool |
| 12 | `billing` 块**不得**做零宽字符混淆；上游新版已把 billing 扩成 7 段链（含 `cc_workload` / `cc_is_subagent` / `cc_prev_req` / `cc_prompt_id`） | 上游 `086ad91`；dario | 基线 `911f49c` 已含该链（`cloaking.go:152-193`） | ✅ 已在上游；fork 别回退 |
| 13 | 9router 的 cch 用 `sha256.slice(0,5)` 冒充，`buildHash` 每次随机 | `9router` | — | ❌ **反面教材**：形状对、算法错，比不发更容易被单独挑出 |
| 14 | UA 被中间网关改写 → 平台误判非 CC → `user_id` 与 token 不属于同一账号 → **首个请求即封** | 复盘：`litianc.cn/2026/03/OAuth_UserID_Leak/` | 我们的四闸同样基于 UA 正则 | 🔴 **直接动机**：这正是 P0-2 要防的事故形态 |

**一句话总结这张表**：CPA 在"单请求协议层"其实做得比大多数同类项目正确（cch 投影、context_management、ttl 配对都对），**真正的差距在失败模式上**——判错之后的动作是"改写"，而 dario 的选择是"透传或降级"。第 14 条就是判错一次、全账号报废的实例。

---

## 5. 风险控制

| 风险 | 缓解 |
|---|---|
| 改指纹常量把自己改出另一种不一致 | P0-1 的 guard 就是护栏；任何常量改动必须先过 guard |
| 内容闸误伤正常用户 | 三层可独立开关；只拦不封是默认；有"放行本次" |
| 节流拖慢真实交互 | 同会话内不抖动；只对新建会话与并发做限制 |
| 出口绑定导致全站不可用 | 灰度：先只对 1 个凭证 enforce，观察 24h |
| 上游大改协议导致 fork 失效 | 每周 rebase；`claude_*.go` 变动即视为告警 |
| 遥测补全反而暴露 | 默认关闭，必须 A/B |

**统一原则：每一项都有独立开关，默认值 = 关闭或与上游一致。**

---

## 6. 验收矩阵

| 维度 | 方法 | 通过标准 |
|---|---|---|
| 编译 | `go build ./...` | 无错误 |
| 静态检查 | `go vet ./...` | 无告警 |
| 单测 | `go test ./internal/antidetect/...` | 全绿 |
| 回归 | 现有 `internal/runtime/executor/*claude*_test.go` | 全绿（这是最重要的护栏） |
| 真客户端 | `ANTHROPIC_BASE_URL` 指向本服务，跑 `cli`/`sdk-cli`/`vscode` | 全部 confirmed、不被改写 |
| 线上观察 | 7 天 | 号存活率、429 率、cache 命中率、被拒率四条曲线 |

---

## 7. 排期（4 周）

| 周 | 内容 | 产出 |
|---|---|---|
| W1 | P0-1 版本守护 + P0-2 判定 fail-safe + P0-3 CHANGELOG 追踪 + P3 观测 | 可编译、可观测、可回滚，`versionguard` 测试转绿 |
| W2 | P1-1 内容闸 + P1-2 节流 | 两个独立开关上线 |
| W3 | P1-3 出口绑定 + P2-1 device pool | 单凭证灰度 |
| W4 | P2-2 窗口建模 + 复盘 | 灰度扩量，写复盘 |

---

## 8. 明确不做

1. 不为了"看起来更像"而增加任何**上游不会发的**字段。
2. 不做 TLS spec 的手工微调——必须从抓包生成。
3. 不把默认值改成"很激进"——默认保守，激进靠配置。
4. 不碰非 Claude provider。
5. 不做"绕过 Anthropic 判定"的军备竞赛式修改；只做**减少异常信号**的工程。
6. **不伪造客户端遥测**（官方在网关/第三方部署上本来就关遥测，伪造的期望收益为负，见 §4.2）。
