# Linux 部署

## 一、先选包：能不能用插件是唯一区别

| 文件 | 编译 | 插件 | 用在哪 |
|---|---|---|---|
| `cpa-linux-<arch>.tar.gz` | `CGO_ENABLED=1`，动态链接 | **支持** | 普通发行版（Ubuntu/Debian/CentOS…）。默认选这个 |
| `cpa-linux-<arch>-no-plugin.tar.gz` | `CGO_ENABLED=0`，静态 | 不支持 | musl / OpenWrt / 极老系统 |

**为什么分两种**：插件加载器在 Linux 上是 `cgo && (linux||darwin)` 条件下才编译的
（`internal/pluginhost/loader_unix.go` 用 `dlopen`）。静态包里这个文件不参与编译，
运行时会退到 `loader_unsupported.go`，所有插件加载失败并报：

```
standard dynamic library plugin loading requires cgo on this platform
```

如果你不用插件，静态包更省事（无任何运行时依赖）。如果你要用 `quota-cutoff` 之类的插件，
必须用 cgo 版。

> cgo 版在 manylinux2014 里编译，最高只要求 `GLIBC_2.17`，CentOS 7 / Ubuntu 16.04 以上都能跑。

## 二、安装

```bash
tar -xzf cpa-linux-amd64.tar.gz
cd cpa-linux-amd64   # 包内平铺，实际解压到当前目录
sha256sum -c SHA256SUMS.txt
sudo mv cpa-linux-amd64 cpa-postmortem-linux-amd64 /opt/cpa/
sudo chmod +x /opt/cpa/cpa-*
```

## 三、配置

`/opt/cpa/config.yaml`：

```yaml
port: 8787
auth-dir: /opt/cpa/auths

api-keys:
  - "换成你自己的-key"

remote-management:
  secret-key: "换成你自己的管理密码"
  allow-remote: false

# 账号所在时区（VPS 默认 UTC，要和账号地区一致）
claude-header-defaults:
  timezone: "America/Los_Angeles"

observability:
  logs:
    logging-to-file: true
    logs-max-total-size-mb: 512
```

## 四、环境变量

`/opt/cpa/cpa.env`：

```bash
CPA_ANTIDETECT_RATE=1
CPA_ANTIDETECT_BURST=3
# 单机单号：不开强制绑定，但开出口漂移检测
CPA_ANTIDETECT_EGRESS={"enforce":false,"pools":{"local":{"proxy_url":""}}}
# 429 熔断：窗口内累计 N 次 429 就冷却（默认 5/15m/15m）
CPA_ANTIDETECT_BREAKER=5/15m/15m
```

**不要设** `CPA_ANTIDETECT_ALLOW_VERSION_DRIFT`——那是排障逃生阀。

## 五、登录并启动

```bash
cd /opt/cpa
./cpa-linux-amd64 --claude-login          # 在这台机器上授权
systemctl enable --now cpa                # 见下面的 unit
```

`/etc/systemd/system/cpa.service`：

```ini
[Unit]
Description=CLIProxyAPI (antidetect)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/cpa
EnvironmentFile=/opt/cpa/cpa.env
ExecStart=/opt/cpa/cpa-linux-amd64 --config /opt/cpa/config.yaml
Restart=on-failure
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

## 六、自检

```bash
# 1. 身份行四层一致
journalctl -u cpa | grep 'claude_identity'

# 2. 健康
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8787/healthz    # 200

# 3. 插件（只有 cgo 版才有输出）
journalctl -u cpa | grep 'plugin registered'

# 4. 确认是动态链接（cgo 版的标志）
file /opt/cpa/cpa-linux-amd64
```

## 七、封号后分析

```bash
grep -o 'credential="[a-f0-9]*"' /opt/cpa/logs/*.log | sort | uniq -c | sort -rn | head
./cpa-postmortem-linux-amd64 -file /opt/cpa/logs/<日志> -credential <哈希> -since 48h
```

## 八、回滚

所有闸都是环境变量控制的，改完 `systemctl restart cpa` 即可，不要重新编译。
