# 部署与灰度清单

> 配套 [CHANGELOG.md](CHANGELOG.md) 与 [TASKS.md](TASKS.md)。这份文件只回答"上线时按什么顺序开、开多大、出问题怎么退"。

---

## 一、默认值意味着"几乎不生效"

本 fork 装上去**不会改变原版行为**，所有闸默认保守：

| 能力 | 默认 | 装完是否生效 |
|---|---|---|
| 版本一致性守护 | 强制 | ✅ 生效（不一致拒绝启动） |
| 客户端三态判定 | 强制 | ✅ 生效 |
| 审计日志 | 开 | ✅ 生效（有请求才有输出） |
| 行为画像 | 开 | ✅ 每 5 分钟一行/凭证 |
| 内容闸 | 开（规则窄） | ✅ 生效 |
| 额度窗口准入 | 开 | ✅ 生效（需要响应头） |
| 节流 | **关**（`RATE=0`） | ❌ 需要配 |
| 出口绑定 | **不强制** | ❌ 需要配 |

**上线前先决定：节流和出口绑定要不要开。** 这两项才是真正拉开差距的。

---

## 二、环境变量

| 变量 | 建议值 | 说明 |
|---|---|---|
| `CPA_ANTIDETECT_ALLOW_VERSION_DRIFT` | **不设** | 设了就等于关掉版本守护，只在紧急排障时临时用 |
| `CPA_ANTIDETECT_CONTENT_GATE` | 不设（开） | 误伤时才设 `off` |
| `CPA_ANTIDETECT_RATE` | `1`（保守）/ `2`（宽松） | 每凭证每秒请求数 |
| `CPA_ANTIDETECT_BURST` | `3` | 允许的突发数 |
| `CPA_ANTIDETECT_MAX_WAIT` | `30` | 等待上限，超时回 429 |
| `CPA_ANTIDETECT_DEVICE_POOL` | `1` | 除非确定要区分多个会话，否则保持 1 |
| `CPA_ANTIDETECT_EGRESS` | 见下 | 一证一出口的 JSON 策略 |
| `CPA_ANTIDETECT_WINDOW_GATE` | 不设（开） | 排障时可设 `off` |

出口策略示例：

```bash
export CPA_ANTIDETECT_EGRESS='{"enforce":true,"pools":{
  "hk-1":{"proxy_url":"socks5://user:pass@1.2.3.4:1080","country":"HK","timezone":"Asia/Hong_Kong"},
  "us-1":{"proxy_url":"socks5://user:pass@5.6.7.8:1080","country":"US","timezone":"America/Los_Angeles"}
}}'
```

对应凭证的 `auths/<file>.json` 里加 `"egress_id": "hk-1"`（字段会落到 attributes）。

**出口评价标准（2026-10 调研后调整）**：社区证据指向的不是"住宅 vs 机房"，而是三个更实际的量：

| 维度 | 要求 | 为什么 |
|---|---|---|
| 出口数量 | 一证一条，不要轮换 | 轮换 = 一眼是代理 |
| 地区一致性 | 长期不跳变 | 有人稳定半年后新增第三个地区节点，几天内封 |
| 共享度 | 独占，不与别的号共用 | 被多人骑过的节点比机房更危险 |

机房 IP 长期稳定使用的案例同样存在，所以**不要为了"住宅"去买高价代理**，先保证上面三条。

### 时区（一号一时，基线已支持）

```json
// auths/<file>.json
{ "type": "claude", "timezone": "Asia/Shanghai", ... }
```

或配置级默认：`claude-header-defaults.timezone`。优先级：凭证 attributes → 凭证 metadata → 配置默认。

> 强制模式下，**没有 `egress_id` 的凭证会被 409 拒绝调度**。上线前先把所有凭证配好，否则会一片 409。

---

## 三、灰度顺序

```
第 1 天   单号灰度
          1 个号 + 内容闸 + 窗口准入 + 节流 1 QPS
          观察：429 率、审计日志里 blocked 数、postmortem 信号

第 2–3 天  扩到 3–5 个号
          每个号单独出口（egress 强制打开）
          观察：有没有意外的 409、窗口准入是否误伤

第 4–7 天  全量
          仍保持节流

第 2 周    复盘 postmortem，再决定是否放宽节流
```

**不要第一天就全量开出口强制**——配置漏一个就是 409 风暴。

---

## 四、上线前自检

```bash
# 1. 编译与测试
go build ./... && go vet ./... && go test ./...

# 2. 身份自洽（最关键）
./cli-proxy-api --config config.yaml
# 启动日志应出现：
#   antidetect: claude_identity version=... tls_hello_from=<同一个版本> ...
# 如果出现 "refusing to start"，说明台账和代码里的常量不一致

# 3. 官方变更检查
go run ./tools/changelog-watch
# 返回 1 = 有 STRONG 命中，需要跑抓包复验

# 4. 管理接口可达（需要配 secret-key）
curl -H "Authorization: Bearer <secret>" http://127.0.0.1:8787/v0/management/config
```

---

## 五、日常观察

**每 5 分钟每凭证一行**：

```
antidetect decision event="credential_profile" decision="accepted"
  credential="<hash>" summary="requests=120 allowed=118 blocked=2 ... ips=3 uas=2 ..."
```

盯三个数：

| 字段 | 健康值 | 异常含义 |
|---|---|---|
| `ips` | 1 | >1 = 这个号走了多个出口 |
| `uas` | 1 | >1 = 下游混用多种客户端 |
| `max_util` | <0.9 | ≥0.95 = 窗口被打满 |

**日志量估算**：每凭证每天 288 行。100 个号 ≈ 2.9 万行/天，约 15–20 MB。

### 落盘与轮转（上游已内置，只需打开）

```yaml
observability:
  logs:
    logging-to-file: true          # 写入轮转文件而不是 stdout
    logs-max-total-size-mb: 2048   # 超过后删最旧的文件
    request-log: false             # 需要逐请求日志时才开，量很大
```

轮转由 lumberjack 完成，无需外部 logrotate。审计记录走同一套日志管道，打开这个开关就会一起落盘。

> 建议：`logs-max-total-size-mb` 按「号数 × 0.2 MB/天 × 保留天数」估。100 个号保留 30 天约 600 MB。

---

## 六、封号之后

```bash
go run ./tools/postmortem -file <服务日志> -credential <哈希> -since 48h
```

按输出的 `signals` 对号入座：

| 信号 | 动作 |
|---|---|
| `multiple exits` | 检查 egress 是否真的强制了；代理商是否换了 IP |
| `mixed clients` | 下游是不是有人在用非 Claude Code 客户端 |
| `content blocks` | 收紧内容闸规则，或定位是哪类请求触发 |
| `quota window saturated` | 降节流值，或把流量分到别的号 |

四条都没有 → 问题不在我们观测范围内，去看官方反馈本身。

---

## 七、回滚

| 场景 | 动作 |
|---|---|
| 内容闸误伤 | `CPA_ANTIDETECT_CONTENT_GATE=off` 重启 |
| 节流拖慢 | `CPA_ANTIDETECT_RATE=0` 重启 |
| 出口绑定导致 409 | 去掉 `CPA_ANTIDETECT_EGRESS` 重启 |
| 窗口准入误拦 | `CPA_ANTIDETECT_WINDOW_GATE=off` 重启 |
| 版本守护挡住启动 | 先看日志里的 drift 行；临时 `CPA_ANTIDETECT_ALLOW_VERSION_DRIFT=1`，但**这是降级不是修复** |
| 整体回退 | 切回上一个 tag：`git checkout <tag> && go build` |

所有开关都是环境变量，不需要改代码；改完重启即可。

---

## 八、每周固定动作

```bash
# 1. 跟上游
git fetch upstream && git rebase upstream/main

# 2. 回归
go build ./... && go test ./...

# 3. 官方变更
go run ./tools/changelog-watch
```

`changelog-watch` 返回 1 时，按 [tools/capture/README.md](../../tools/capture/README.md) 跑一次抓包复验。
