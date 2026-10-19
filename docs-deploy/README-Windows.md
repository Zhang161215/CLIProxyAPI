# Windows 部署

> 版本 `0.5.0-antidetect` · 提交 `aa89d97d` · 静态编译，无需安装 Go 或任何运行库
> 架构对照：`x64` 用 amd64 包，`ARM64`（如骁龙本）用 arm64 包。查看：`echo %PROCESSOR_ARCHITECTURE%`

## 一、解压

把 `cpa-windows-amd64.exe` 和 `cpa-postmortem-windows-amd64.exe` 放到同一个目录，例如 `C:\cpa\`。

## 二、配置

`C:\cpa\config.yaml`：

```yaml
port: 8787
auth-dir: C:\cpa\auths

api-keys:
  - "换成你自己的-key"

remote-management:
  secret-key: "换成你自己的管理密码"
  allow-remote: false

claude-header-defaults:
  timezone: "America/Los_Angeles"

observability:
  logs:
    logging-to-file: true
    logs-max-total-size-mb: 512
```

## 三、环境变量（用 .env，别用 setx）

程序会自动读取**工作目录下的 `.env`**，比系统环境变量方便且不需要重启：

`C:\cpa\.env`：

```
CPA_ANTIDETECT_RATE=1
CPA_ANTIDETECT_BURST=3
CPA_ANTIDETECT_WINDOW_GATE=1
CPA_ANTIDETECT_CONTENT_GATE=1
CPA_ANTIDETECT_EGRESS={"enforce":false,"pools":{"local":{"proxy_url":""}}}
```

> 若你用 PowerShell 临时跑，也可以：`$env:CPA_ANTIDETECT_RATE="1"`；cmd 用 `set CPA_ANTIDETECT_RATE=1`。

## 四、登录账号

```cmd
cd C:\cpa
cpa-windows-amd64.exe --claude-login
```

浏览器会打开 OAuth 页面，完成后凭证落在 `C:\cpa\auths\`。

> **这台 Windows 机器就是你账号的机器**。之后不要换到别的机器上跑——注册机、授权机、使用机应当是同一台。

## 五、启动

前台：

```cmd
cpa-windows-amd64.exe --config C:\cpa\config.yaml
```

看到这两行就对了：

```
CLIProxyAPI Version: 0.5.0-antidetect, Commit: aa89d97d
antidetect: claude_identity version=2.1.280 ... tls_hello_from=2.1.280 cch_from=2.1.280
```

## 五半、环境变量为什么不生效（排查清单）

`CPA_ANTIDETECT_*` 是**进程环境变量**，不是配置文件项。没设对时，服务能正常跑，
只是那些开关全是默认值——最典型的表现是画像里 `paced` 一直是 0。

按顺序查三件事：

**1. 变量设在哪个作用域**

```powershell
[Environment]::GetEnvironmentVariable('CPA_ANTIDETECT_RATE','Machine')   # 服务通常只读得到这个
[Environment]::GetEnvironmentVariable('CPA_ANTIDETECT_RATE','User')
```

服务以 `LocalSystem` 运行时，**读不到 Administrator 的用户变量**。要设在 Machine 作用域。

**2. 进程是什么形态**（服务、计划任务、手动启动，三种都可能）

```powershell
# 服务
Get-CimInstance Win32_Service | Where-Object { $_.PathName -like '*CLIProxy*' } | Select-Object Name,StartName,State
# 计划任务
Get-ScheduledTask | Where-Object { $_.Actions.Execute -like '*CLIProxy*' -or $_.TaskName -like '*CPA*' } |
  Select-Object TaskName,State
# 正在跑的进程
Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -like '*CLIProxyAPI*' -or $_.Name -like '*cpa*' } |
  Select-Object ProcessId,Name,CommandLine
```

计划任务默认以**当前用户**运行（用户变量可见），服务默认以 **LocalSystem** 运行（只有 Machine 变量可见）——
两种形态的结论完全不同，所以要先确认。

**3. 重启顺序**

环境变量只在新进程启动时读取。**先设变量，再重启**，顺序反了等于没设。

**4. 用启动日志确认**

`v0.5.3` 起，每次启动会打一行实际生效的开关：

```
antidetect: options rate=1 burst=3 max_wait=30s content_gate=on window_gate=on egress=observe
```

看到 `rate=0` 就是变量没进来，不用再靠并发测试反推。

**5. 关于 `.env`**

程序只读**工作目录**下的 `.env`。服务/计划任务的工作目录经常不是程序目录
（服务默认是 `C:\Windows\System32`），所以 `.env` 放在程序旁边可能读不到。
要稳妥就用 Machine 作用域的环境变量，或者显式设置工作目录。

## 六、做成后台服务（二选一）

**方案 A：NSSM（推荐）**

```cmd
nssm install CLIProxyAPI "C:\cpa\cpa-windows-amd64.exe" "--config C:\cpa\config.yaml"
nssm set CLIProxyAPI AppDirectory C:\cpa
nssm start CLIProxyAPI
```

**方案 B：任务计划程序**

新建任务 → 触发器「计算机启动时」→ 操作「启动程序」指向 exe，参数 `--config C:\cpa\config.yaml`，起始于 `C:\cpa`，勾选「不管用户是否登录都运行」。

## 七、防火墙

如果要从别的机器访问，放行端口：

```powershell
New-NetFirewallRule -DisplayName "CLIProxyAPI" -Direction Inbound -LocalPort 8787 -Protocol TCP -Action Allow
```

只在本机用就不用管。

## 八、自检

```powershell
Invoke-WebRequest http://127.0.0.1:8787/healthz -UseBasicParsing | Select-Object -ExpandProperty StatusCode
# 期望 200
```

## 九、封号后分析

```cmd
cpa-postmortem-windows-amd64.exe -file C:\cpa\logs\<日志文件> -credential <哈希> -since 48h
```

凭证哈希从日志里 `credential=` 字段取。

## 十、Windows 上与 Linux 的差异

| 项 | 说明 |
|---|---|
| 路径 | 配置里用反斜杠，YAML 里建议用正斜杠 `C:/cpa` 更稳妥 |
| 日志位置 | `C:\cpa\logs\`（开了 `logging-to-file` 之后） |
| 信号 | 没有 systemd，用 NSSM 或任务计划 |
| 时区 | 系统时区设对，或显式配 `claude-header-defaults.timezone` |
| 单实例 | 不要在同一个 `auth-dir` 上跑两个实例，会争抢凭证文件 |

## 十一、校验

```powershell
Get-FileHash cpa-windows-amd64.exe -Algorithm SHA256
```

与 `SHA256SUMS-Windows.txt` 对照。
