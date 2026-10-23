# macOS 部署

> 二进制为 `CGO_ENABLED=1` 构建，动态链接系统库，插件可用。
> Apple Silicon 用 `cpa-darwin-arm64`，Intel 用 `cpa-darwin-amd64`。

## 一、解压与放行

```bash
tar -xzf cpa-darwin-arm64.tar.gz
shasum -a 256 -c SHA256SUMS.txt

mkdir -p ~/cpa && mv cpa-darwin-arm64 cpa-postmortem-darwin-arm64 ~/cpa/
chmod +x ~/cpa/cpa-*
```

未签名的二进制首次运行会被 Gatekeeper 拦下：

```bash
xattr -d com.apple.quarantine ~/cpa/cpa-darwin-arm64
```

## 二、配置

`~/cpa/config.yaml`：

```yaml
port: 8787
auth-dir: ~/cpa/auths

api-keys:
  - "换成你自己的-key"

remote-management:
  secret-key: "换成你自己的管理密码"
  allow-remote: false

# 与账号地区一致
claude-header-defaults:
  timezone: "America/Los_Angeles"

observability:
  logs:
    logging-to-file: true
    logs-max-total-size-mb: 512
```

## 三、环境变量

用 `.env`（程序自动读取工作目录下的 `.env`）：

```
CPA_ANTIDETECT_RATE=1
CPA_ANTIDETECT_BURST=3
CPA_ANTIDETECT_EGRESS={"enforce":false,"pools":{"local":{"proxy_url":""}}}
```

## 四、登录与启动

```bash
cd ~/cpa
./cpa-darwin-arm64 --claude-login
./cpa-darwin-arm64 --config config.yaml
```

启动后应看到：

```
antidetect: claude_identity version=2.1.280 ... tls_hello_from=2.1.280 cch_from=2.1.280
```

## 五、后台运行

macOS 没有 systemd，用 launchd：

`~/Library/LaunchAgents/dev.cpa.plist`：

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.cpa</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/REPLACE_ME/cpa/cpa-darwin-arm64</string>
    <string>--config</string>
    <string>/Users/REPLACE_ME/cpa/config.yaml</string>
  </array>
  <key>WorkingDirectory</key><string>/Users/REPLACE_ME/cpa</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict>
</plist>
```

```bash
launchctl load ~/Library/LaunchAgents/dev.cpa.plist
```

## 六、注意

- **本机就是账号的机器**：注册、授权、使用都在这一台，之后不要换机器
- 插件目录：`plugins/darwin/arm64/*.dylib`（或按插件的说明放）
- 封号分析：`./cpa-postmortem-darwin-arm64 -file <日志> -credential <哈希> -since 48h`
