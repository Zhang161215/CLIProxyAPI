# 更新日志

> 本文件只记录**本 fork 相对上游 CLIProxyAPI 的增量**，上游自己的变更看官方 `CHANGELOG.md`（用 `tools/changelog-watch` 追踪）。
> 格式参照 Keep a Changelog；版本号是本 fork 自己的，与上游无关。

---

## [Unreleased]

候选：
- 调度层按窗口剩余量主动分流（当前只有准入拦截，不会切号）
- 内容闸规则热加载（当前改规则要重启）
- 审计日志独立落盘与轮转（当前走标准日志管道）
- 出口 IP 漂移检测（当前只做逻辑绑定，不校验 IP 是否变化）

---

## [0.3.0] — 2026-10-18 · 按外部情报补齐四个维度

依据见 [INTEL-2026-10.md](INTEL-2026-10.md)。

### 新增

**T-13 一号一时区** · 基线已支持，本次补文档

- 优先级：凭证 attributes `timezone` → 凭证 metadata → `claude-header-defaults.timezone`
- 影响 currentDate 与 Environment 块；`DEPLOY.md` 已写明配置方式

**T-14 429 观察期信号** · `monitor` + `postmortem`

- 上游返回 429 时记 `RateLimited`，即使随后重试成功也计数
- 画像 `summary` 增加 `ratelimited=`；报告新增信号「upstream rate limits: N（429 连续出现常见于封禁前）」
- 依据：同批反代号里只有反复 429 的那个号几天后封

**T-15 内容指纹聚类** · `internal/antidetect/fingerprint/`

- 从请求体提取**工作目录路径、git remote、提交者邮箱**，**只存 sha256 前 12 位**
- ssh 与 https 两种 remote 写法归一化后同指纹
- 跨凭证碰撞时记 `content_fingerprint / collision`，**只告警不阻断**，同一碰撞只报一次
- 排除 `git@host` 这种伪邮箱
- 依据：源码级遥测审计显示 CLI 每请求携带路径与 git 提交者；另有"封禁沿项目传播"的个案
- 验证：`fingerprint_test.go`（10 条）+ `claude_fingerprint_test.go`（3 条）

### 变更

- **出口评价口径**：从"IP 纯净度"改为「出口数量 / 地区一致性 / 共享度」。`egress` 包注释与 `DEPLOY.md` 同步
- `audit` 增加 `DecisionCollision`

### 说明

T-16 是纯文档调整，没有代码逻辑变化：社区证据里机房 IP 长期稳定与住宅 IP 被共用后封禁并存，继续按"纯净度"选代理属于把钱花错地方。

---

## [0.2.1] — 2026-10-18 · 防静默失效

### 新增

**窗口头缺失告警** · `claude_window.go`

- 响应里没有任何 `anthropic-ratelimit-unified-*` 头时，按凭证每 10 分钟记一次
  `quota_window_headers / missing`
- 动机：官方若改名，准入闸会继续运行但**永不生效**，且不报错——"看起来健康"比"明确报错"更危险
- 验证：`claude_window_headers_test.go`（3 条：只报一次 / 超时重报 / 有头不报）

**JSON 日志解析** · `tools/postmortem/`

- 同时支持默认文本格式与 logrus JSON 输出（按行首 `{` 自动判别）
- 动机：部署切换日志格式后，回溯工具不能跟着失效
- 验证：`main_test.go` 新增 JSON 用例（含"忽略其他组件的记录"）

**部署与灰度清单** · `DEPLOY.md`

- 默认值说明、环境变量建议值、7 天灰度顺序、上线自检、日常观察指标、回滚表、每周固定动作

### 变更

- `audit` 增加 `DecisionMissing`

---

---

## [0.2.0] — 2026-10-18 · 封号回溯

### 新增

**监控：按凭证的行为画像** · `internal/antidetect/monitor/`

- 每凭证滚动统计：请求数、放行/拦截/节流次数、**distinct 出口 IP / UA / entrypoint / session**、窗口最高利用率
- 集合容量上限 32，超出置 `Overflow`（本身就是一个信号）
- 每 5 分钟 flush 一次写审计日志，然后清零——每行描述一个**时间窗**，不是一生累计
- 验证：`internal/antidetect/monitor/monitor_test.go`（11 条）

**回溯：封号后归因** · `tools/postmortem/`

```bash
go run ./tools/postmortem -file <服务日志>                 # 全部凭证
go run ./tools/postmortem -file <日志> -credential <哈希>   # 单个凭证
go run ./tools/postmortem -file <日志> -since 48h
```

输出：画像汇总 → 风险信号（按封号案例里的常见度排序）→ 决策计数 → 最后五条决策。

- 只读日志，不连数据库；日志里出现过的事实才能被分析
- 凭证 ID 用日志里的哈希，原文从不落盘
- 验证：`tools/postmortem/main_test.go`（6 条）

**接入点**

| 位置 | 记录内容 |
|---|---|
| 请求进入执行器 | `Observe`（请求数、IP、UA、entrypoint、session） |
| 内容闸命中 | `Block`（规则 ID） |
| 节流超预算 | `Pace` |
| 窗口观测 | `Utilization`（取最大值） |

### 变更

- `audit.Record` 增加 `Summary` 字段；`internal/logging` 白名单同步加入 `summary`

### 修复

- `postmortem` 的字段扫描器在未加引号的值之后会吞掉下一个 key，导致 `event` 字段丢失；改为正则匹配（`fieldPattern`），并补了回归用例

---

## [0.1.0] — 2026-10-18 · 防封基座

### 新增

**版本一致性守护** · `internal/antidetect/versionguard/`

- 身份台账 `Profile`：advertised version / stainless 三件套 / TLS 抓包来源 / CCH 验证来源
- 启动断言四层一致；不一致**返回错误拒绝启动**（不用 `log.Fatal`，符合仓库规范）
- CCH seed 按版本查表；**未验证版本不猜 seed**，降级并告警
- 逃生阀：`CPA_ANTIDETECT_ALLOW_VERSION_DRIFT=1`
- 验证：`profile_test.go` / `guard_test.go`

**客户端三态判定** · `helps/claude_client_state.go`

- 二值 `Confirmed` → `Confirmed / Unknown / Rejected`
- **Unknown（entrypoint 无法识别）一律透传**，不改 UA、不注入 billing、不改 system
- 已知 native 但缺闸 = 抄 header 的假客户端，仍按原逻辑 cloak；已知非 native（sdk-ts 等）也保持原行为
- 配套：`ApplyClaudeCallerDeviceHeaders` 保留调用方身份，只补缺失值
- 验证：`claude_client_state_test.go`、`claude_executor_client_state_test.go`

**内容闸** · `internal/antidetect/content/`

- 只看**最新一条 user 正文**；跳过 `tool_result`、历史轮次、system
- 三分类（蒸馏/越狱/NSFW）、13 条内置规则中英各半
- 命中 → 403 + `StatusCode()` + `IsRequestScoped()`（不冷却凭证），**不选号不打上游**
- 开关：`CPA_ANTIDETECT_CONTENT_GATE=off`
- 验证：正样本 13 条 + 负样本 10 条（含"忽略之前的错误"这类正常工程句子）

**出站节流** · `internal/antidetect/throttle/`

- 每凭证令牌桶，时钟可注入（仓库规范禁止测试依赖真实 sleep）
- 等待有上限，超时回 429 而不是无限阻塞；取消时退还令牌
- 开关：`CPA_ANTIDETECT_RATE` / `_BURST` / `_MAX_WAIT`，默认 `0` = 不节流
- 验证：`throttle_test.go`、`claude_throttle_test.go`

**出口绑定** · `internal/antidetect/egress/`

- 凭证用 `egress_id` 声明出口（attributes 优先、metadata 回落）
- 强制模式下未绑定 → 409；两个凭证争同一出口 → 后者被拒；换出口自动释放旧出口
- 策略 JSON 写错只关掉强制，不拖垮服务
- 开关：`CPA_ANTIDETECT_EGRESS`
- 验证：`egress_test.go`（11 条）、`claude_egress_test.go`（5 条）

**额度窗口** · `internal/antidetect/window/`

- 5h / 7d 双窗口解析（epoch 与 RFC3339 都吃），脏值丢弃
- 三态：`<0.90` 正常 / `0.90–0.95` 降级 / `>0.95` 熔断
- **官方"残留 100%"当新窗口**：`resets_at` 已过期时 utilization 按 0 算
- 快照 TTL 10 分钟，过期不参与判定
- 响应头 → 下一次请求准入（blocked 直接 429）
- 开关：`CPA_ANTIDETECT_WINDOW_GATE=off`
- 验证：`window_test.go`（8 条）、`store_test.go`（9 条）、`claude_window_gate_test.go`（5 条）

**设备池** · `internal/auth/claude/identity.go`

- 池大小 1（原行为）→ 可 1–8；`SelectDeviceID` 按 `sha256(session) % len` 选，同会话恒定
- **已有凭证的池不重建**（重建会轮换设备 ID）
- 开关：`CPA_ANTIDETECT_DEVICE_POOL`
- 验证：`identity_pool_test.go`

**审计日志** · `internal/antidetect/audit/`

- 一条决策一行结构化日志；凭证 ID 一律 `sha256` 前 12 位
- 六类事件：`version_guard` / `content_gate` / `cloak_gate` / `egress_binding` / `pacing` / `quota_window`
- 验证：`audit_test.go`、`internal/logging/global_logger_audit_fields_test.go`

### 工具

| 工具 | 用途 |
|---|---|
| `tools/changelog-watch` | 拉官方 CHANGELOG，与基线 diff，STRONG 关键词命中返回 1 |
| `tools/capture/dump_claude_request.py` | mitmproxy 插件，导出 header 顺序 + body（Authorization 脱敏） |
| `tools/capture/verify-cch` | 抓包复算 CCH，判 `MATCH` / `MISMATCH`，给出下一动作 |
| `tools/capture/hello-probe` | 不需要 root，直接读 ClientHello（cipher / 扩展顺序 / ALPN / SNI） |
| `tools/capture/ja4.sh` | pcap → JA4（需要 tshark 与 sudo） |

### 修复

- **版本台账与事实不符**：原版宣称 2.1.280 而 TLS/CCH 来自 2.1.220。真机抓包验证后对齐到 2.1.280：
  - `cch` 复算 `MATCH`（seed `0x4D659218E32A3268` 未变）
  - ClientHello 17 个 cipher + 前 13 个扩展顺序**逐项相同**（缺的 padding/PSK 是上下文相关项）
- **日志 formatter 丢字段**：`LogFormatter` 只渲染白名单字段，审计字段会被静默丢弃。已扩白名单并加回归测试

### 不做

- **不伪造客户端遥测**：官方在第三方 provider / 网关 / 自带 OAuth 部署上本就关闭遥测，"没有遥测"是合法状态；伪造的代价高于收益
- **不改非 Claude 链路**：Gemini / Codex / Grok / Kimi / Meta / Devin 一行未动
- **不改调度器结构**：所有闸都是"发请求前拦截"，不动选号逻辑

---

## 维护手册（每次跟上游时照做）

1. **跟上游**
   ```bash
   git fetch upstream && git rebase upstream/main   # 集成分支
   ```
   冲突热点只有 9 个文件（`claude_executor_{execute,stream,request,cloaking}.go`、`helps/utls_client.go`、`auth/claude/identity.go`、`logging/global_logger.go`、`cmd/server/main.go`、`.gitignore`）。

2. **跑回归**
   ```bash
   go build ./... && go vet ./... && go test ./...
   ```
   全绿才算跟上。

3. **查官方改了什么**
   ```bash
   go run ./tools/changelog-watch
   ```
   返回 1 = 有 STRONG 命中（协议/指纹相关），需要跑一次抓包复验。

4. **复验身份**
   ```bash
   mitmproxy -s tools/capture/dump_claude_request.py --save-stream-file /tmp/cc.flow
   # 另开终端，真客户端直连（不要经过任何反代）发一句话
   go run ./tools/capture/verify-cch -file capture-out/<name>.json
   ```
   `MATCH` → 只需更新台账版本号；`MISMATCH` → **不要签**，把 `CCHFrom` 留在旧版本。

5. **封号之后**
   ```bash
   go run ./tools/postmortem -file <服务日志> -credential <哈希> -since 48h
   ```
   看 `signals` 段：多出口 / 多 UA / 内容命中 / 窗口饱和，四条里命中哪条就对应改哪一块配置。
