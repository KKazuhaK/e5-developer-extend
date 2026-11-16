# 架构设计（草案）

技术栈：**Go + Wails v3**（2026-11-16 决定）。

## 1. 版本基线（2026-11-16 核实）

| 组件 | 版本 | 说明 |
|---|---|---|
| Go | 1.27.x（最低 1.26） | Wails v3 要求 Go ≥ 1.26；建议 ≥ 1.26.10 / 1.27.3（含 `crypto/x509` 安全修复） |
| Wails | v3.1.0（v3.0.0 于 2026-10-26 发布正式版） | 自带托盘、开机自启、单实例、通知、应用内更新（支持 GitHub Releases） |
| msal-go | v1.10.1（v1.11.0 还在 beta） | 支持设备码、浏览器交互、client credentials、ROPC |
| Linux GUI 依赖 | GTK4 + WebKitGTK 6.0 | v3 默认用这套；据发布说明，GTK3 只保留到 v3.1；[v3.1.0](https://github.com/wailsapp/wails/releases/tag/v3.1.0) 另有实验性的无 CGO 后端 |
| Windows GUI 依赖 | WebView2 运行时 | Windows 10/11 一般已预装；安装包里要带引导安装程序以防万一 |
| macOS GUI 依赖 | WKWebView（系统自带） | 无需额外依赖 |

**目标平台：Windows、macOS、Linux 三端**，x64 和 arm64 都支持。

## 2. 形态

同一个仓库产出两个可执行文件，共用 `internal/` 下的核心：

- **`e5renew`（CLI）**：纯 Go、无 CGO、静态链接。服务器和无桌面环境用，可作为 systemd 或 launchd 服务常驻。
- **`e5renew-gui`（GUI，显示名称待定）**：Wails v3 应用。带托盘，运行引擎，关窗后留在托盘继续跑。

**常驻进程不碰 WebView，窗口单独开一个进程**（依据 2026-11-16 的实测，见下表）：

| 状态（Wails v3.1.0，GTK4，Linux，CPU 渲染） | 进程树 PSS |
|---|---|
| 只有托盘，从没开过窗口 | **60 MB** |
| 开着一个窗口 | ~320 MB（另一次单窗口测试为 ~350 MB，见 dev-setup.md） |
| 窗口 `Close()` 之后 | ~305 MB（WebKit 进程**没有退出**） |
| 再开一次窗口，再关掉 | ~432 MB（多了一个 WebKit 进程，**越开越多**） |

原因：Wails v3 的 GTK4 后端在窗口关闭后不会结束对应的 WebKitWebProcess，
见 [wailsapp/wails#6505](https://github.com/wailsapp/wails/issues/6505)（2026-10-29 提交，目前未修复）。
该 issue 的复现结果显示 GTK3 后端没有这个问题，但据 [v3.0.0 发布说明](https://github.com/wailsapp/wails/releases/tag/v3.0.0)，GTK3 只保留到 v3.1。

另外，就算没有这个泄漏，一个进程只要初始化过 WebView，关窗后主进程也还占约 107 MB，比从没开过窗口时的 60 MB 多。
所以无论这个 bug 修不修，都按进程来隔离：

```
桌面（Win/macOS/Linux）：
        e5renew-gui（托盘 + 引擎，常驻，不创建 WebView，~60 MB）
            │ 点“打开主窗口”
            ▼
        e5renew-gui --window（单独进程，只负责界面，通过 IPC 连常驻进程；
                               关窗即退出，内存全部还给系统）
服务器：e5renew run（纯 Go、无 CGO，引擎常驻，没有界面；
                    Linux 用 systemd，Windows 用服务，macOS 用 launchd）
```

上表的内存数据只在 Linux 上测过。Windows（WebView2）和 macOS（WKWebView）上关窗后会不会残留进程还没测，
但按进程隔离的设计在三个平台上都适用。

- 引擎代码只有一份（`internal/engine`）。桌面上由托盘进程运行，服务器上由 CLI 运行；文件锁保证同一时间只跑一个。
- 窗口进程是同一个可执行文件加 `--window` 参数，不需要额外打包一个程序。
- 可选优化：Linux 托盘改用纯 Go 的 DBus StatusNotifier 实现（如 `fyne.io/systray`），常驻内存预计能降到 20 MB 左右。
  但 macOS 上这条路需要 CGO，而且要和 Wails 的托盘二选一。先用 Wails 自带的托盘，以后再看是否值得。

**同一时间只有一个引擎在跑**：

- 引擎启动时对数据目录加文件锁。要跨平台（Unix 上是 `flock`，Windows 上是 `LockFileEx`），用 `github.com/gofrs/flock` 封装。
- 已有引擎在跑时，CLI 的 `status` / `pause` / `run-now` 等命令通过本地 Unix socket 控制它，而不是自己再起一个。
- GUI 在已有 CLI 守护进程的情况下也作为客户端连上去，反过来也一样。

## 3. 目录结构

```
e5-renew/
├── cmd/
│   └── e5renew/            # CLI 入口（cobra）
├── gui/                    # Wails v3 应用
│   ├── main.go
│   ├── services/           # 暴露给前端的 Go 服务（薄封装 internal/）
│   └── frontend/           # 前端：React + TypeScript + Vite
├── internal/
│   ├── config/             # TOML 配置：账号、调度、选项
│   ├── auth/               # msal-go 封装：4 种登录方式 + 令牌缓存持久化
│   ├── secret/             # 系统钥匙串 + 加密文件回退
│   ├── catalog/            # Graph API 库（内嵌 YAML）、按权限和身份筛选
│   ├── engine/             # 每账号调度循环、工作时段、随机化、自动暂停
│   ├── store/              # 状态（state.json）与按天的调用日志（JSONL）
│   └── ipc/                # 本地 Unix socket 控制接口（JSON-RPC）
├── catalog/apis.yaml       # API 库源文件（go:embed）
├── packaging/              # systemd unit、launchd plist、AppImage、Homebrew
└── docs/
```

## 4. 核心模块

### 4.1 认证 `internal/auth`

| 方式 | 身份 | 用在 | msal-go API |
|---|---|---|---|
| 设备码 | 委托 | CLI 首选；GUI 也可用 | `public.Client.AcquireTokenByDeviceCode` |
| 浏览器交互（PKCE + localhost 回调） | 委托 | GUI 首选 | `public.Client.AcquireTokenInteractive` |
| Client Secret | 应用 | 两者 | `confidential.Client.AcquireTokenByCredential` |
| 账号密码（ROPC） | 委托 | 没开 MFA 的账号（不推荐） | `public.Client.AcquireTokenByUsernamePassword` |

- 委托身份之后都靠 `AcquireTokenSilent`，依赖 refresh token。
- **令牌缓存**：实现 msal-go 的 `cache.ExportReplace` 接口，每个账号一份缓存，加密落盘（AES-GCM），密钥放钥匙串。
- **ROPC 不适用的情况**：开了 MFA 或条件访问的账号，以及个人 Microsoft 账号。UI 里要说明，并引导用户改用设备码。
- `Client Secret` 有过期时间：记录到期日，到期前在 GUI 通知或在 CLI 日志里提醒。

### 4.2 凭据 `internal/secret`

- 优先用系统钥匙串：Windows 凭据管理器、macOS Keychain、Linux Secret Service（GNOME Keyring 或 KWallet）。
  Windows 的凭据管理器单条有 2.5 KB 上限，所以那里只存主密钥；令牌缓存用主密钥加密后写文件。
- 没有桌面会话的服务器用回退方案：放在 `$XDG_DATA_HOME/e5renew/` 下的加密文件，主密钥来自环境变量或 systemd credentials（`LoadCredential=`）。
- 候选库：`99designs/keyring`（多后端，含文件后端）或 `zalando/go-keyring`（更小，只有系统后端）。

### 4.3 API 库 `internal/catalog`

- 格式见 [graph-api-catalog.md](graph-api-catalog.md)：每条记录包括分类、名称、方法、委托路径、应用路径、所需权限。
- 按账号筛选：
  1. 账号身份类型（委托或应用）；
  2. 已授予的权限：从令牌的 `scp` / `roles` 声明里读，和每条 API 的 any-of 权限列表求交集，不用另外配置；
  3. 用户勾选的分类。
- 应用身份下，路径里的 `/me` 换成 `/users/{target}`，`target` 是配置里指定的用户，或者启动时从 `/users` 里随机取一个。

### 4.4 调度引擎 `internal/engine`

每个账号一个 goroutine：

```
loop:
  next = 计算下次运行时间(now, 间隔 + 随机抖动, 工作日, 工作时段, 运行期限)
  等待到 next（可被 暂停/立即运行/退出 打断）
  round = 从可用 API 中抽 N 个（N 随机，可配置），可选随机顺序
  for api in round:
      token = auth.Get(account)          # 静默刷新，失败就标记需要重新登录
      resp  = graph.Do(api)              # 请求间加 1–10 s 随机间隔
      store.Log(account, api, status, latency)
  autopause.Evaluate(最近 K 轮) → 可能暂停 + 原因
```

- **时间窗口**：早于开始时间推到当天开始；晚于结束时间推到次日；非工作日一直后推。时区用系统本地时区。
- **自动暂停** 初版规则：
  - 连续 M 轮全部失败，暂停；
  - 401/403 比例超过阈值，标记需要重新登录或补权限；
  - 429 时按 `Retry-After` 退避，不算失败。
- **HTTP**：标准库 `net/http`，读取 `HTTPS_PROXY` 等环境变量，也支持配置里单独指定代理；请求带正常的 User-Agent。

### 4.5 存储 `internal/store`

- 状态存 `state.json`（原子写入），调用日志按天存 JSONL 文件，默认保留 30 天。
  不用 SQLite：省下 6–8 MB 体积，文件也方便用户直接查看。详见 [design/engine.md](design/engine.md)。
- 配置用 TOML（人能直接编辑），运行状态和日志单独存放。
- 目录遵循平台惯例：
  - Linux：`$XDG_CONFIG_HOME/e5renew`、`$XDG_DATA_HOME/e5renew`
  - macOS：`~/Library/Application Support/e5renew`
  - Windows：`%APPDATA%\e5renew`（配置）、`%LOCALAPPDATA%\e5renew`（数据和日志）

## 5. CLI 草案

```
e5renew account add [--device-code | --browser | --client-secret | --password]
e5renew account list | remove <name> | relogin <name>
e5renew run            # 前台运行引擎（给 systemd / launchd 用）
e5renew run-now [name] # 立刻跑一轮
e5renew status         # 各账号状态、下次运行时间、最近成功率
e5renew logs [name] [--follow]
e5renew pause|resume <name>
e5renew service install|uninstall|status   # systemd user unit / launchd plist / Windows 服务或计划任务
e5renew doctor         # 体检：后台服务、自启、linger、令牌、Secret 到期、最近一次成功（见第 9 节）
e5renew catalog list   # 查看 API 库和每条需要的权限
```

## 6. GUI 页面草案

- **账号列表**：状态、身份类型、下次运行、今日成功率、暂停原因；可按“出错 / 暂停”筛选，也能搜索。
- **账号编辑**：登录方式、Client ID / Tenant、API 分类勾选、权限检测结果。
- **调用日志**：每轮明细和失败原因。
- **设置**：工作日和工作时段、运行期限、随机化选项、代理、开机自启、关闭窗口时的行为、主题。
- **托盘**：开始/暂停全部、立即运行、打开主窗口、退出。

## 7. 构建与发布

> 实际方案以 [design/release.md](design/release.md) 为准：不用 GoReleaser、第一版不做 AppImage 和包管理器清单，原因见该文第 2 节。

- **CLI**：用 GoReleaser 交叉编译 windows/linux/darwin × amd64/arm64，发布到 GitHub Releases；
  另提供 Homebrew tap、winget / Scoop 清单，以及给服务器用的 Docker 镜像。
- **Windows GUI**：在 GitHub Actions 的 Windows runner 上构建，打 NSIS 安装包（Wails 自带）。
  不签名的话 SmartScreen 会提示“未知发布者”；代码签名证书以后再考虑。
- **GUI**：
  - Linux：在 Linux 上构建，打 AppImage、deb、rpm；
  - macOS：在 GitHub Actions 的 macOS runner 上构建，打成 `.app` 和 `.dmg`。
- **macOS 签名和公证**：需要 Apple Developer 账号（每年 99 美元）。不签名也能用，但用户得手动在“隐私与安全性”里放行。
- 开发环境搭建见 [dev-setup.md](dev-setup.md)。

## 8. 风险与限制

- **续期规则不透明**：微软说开发者订阅按“开发活动”评估是否续期，没有公开具体口径。调用 Graph 是常见做法，但**不保证一定续期**。README 里要写清楚。
- **新账号门槛**：现在新开 E5 开发者订阅需要 Visual Studio 订阅或合作伙伴资格，这个工具只对已有订阅有意义。
- **ROPC 前景**：微软在持续收紧 ROPC，强制 MFA 也在扩大范围。ROPC 只作为可选项，默认用设备码或浏览器登录。
- **权限最小化**：API 库只放只读接口；写操作（建了又删的临时文件或邮件）作为可选分类，默认关闭。

## 9. 长期后台运行

这个程序要连续跑几个月甚至几年，所以“能稳定跑下去、出了问题能被发现”比功能多少更重要。
下面分三块：程序本身、后台托管，以及文档和引导。

### 9.1 程序本身

**不崩、崩了也能恢复**
- 每个账号的循环都有 `recover`，一个账号出 panic 只影响它自己，并且会记录日志，过一段时间自动重启这个循环。
- 进程级的崩溃交给系统托管来重启（见 9.2）。
- 启动时检查上次是否异常退出；状态文件原子写入，日志只追加，断电后最多丢最后一行。

**睡眠和唤醒**
- 不用 `time.Sleep(下次运行 - 现在)` 这种一次性的长时间睡眠。Go 的定时器用单调时钟，在 Linux 上系统挂起期间单调时钟不走，唤醒后会晚很久才触发。
- 改成每 30–60 秒醒一次，用**墙上时间**和 `next_run` 比较。唤醒、改系统时间、夏令时切换都能自动纠正。
- 唤醒后检测到时间跳跃时，先等网络恢复（随机延迟 1–3 分钟），避免一醒来就失败。

**网络和服务端异常**
- 断网（DNS 失败、连接被拒）**不算账号失败**，不会触发自动暂停，只做指数退避重试。
- 429 / 503 按 `Retry-After` 退避。
- 只有认证失败（401、`invalid_grant`）和权限不足（403）才算账号层面的问题。

**凭据会过期**
- 委托身份的 refresh token 有效期 90 天，每次使用会顺延。只要程序一直在跑就不会过期；停机超过 90 天就要重新登录。
- 如果管理员撤销了会话、改了密码、或者条件访问策略有变化，也要重新登录。这时把账号标记为“需要重新登录”，并发通知。
- Client Secret 有固定到期日（最长 24 个月）。到期前 30、7、1 天各提醒一次。

**资源占用长期不涨**
- 调用日志按天数和条数自动清理，定期 `VACUUM`；日志文件按大小轮转。
- 不在内存里无限累积数据（日志列表、事件订阅、HTTP 连接）。
- 闲置时几乎没有 CPU 唤醒：每分钟醒一次，没到时间就继续睡。
- **CI 里加长时间运行测试**：用假时钟模拟连续运行 1 年；另外在真实环境里挂机 72 小时，观察内存曲线。

**能看到它还活着**
- 状态里记录每个账号的“上次成功调用时间”。
- 超过阈值（例如 3 天）没有成功调用，就在 GUI 托盘图标上标红、发桌面通知、`doctor` 报警。
- 可选的外部通知：Webhook（可以接 Telegram、Bark、Server 酱、企业微信等）。这样服务器上的 CLI 出了问题也能知道。
- 写一个心跳文件（时间戳），方便外部监控（如 Uptime Kuma 的 push 监控）。

**升级不中断**
- GUI 用 Wails 自带的更新器；CLI 提供 `e5renew self-update`，或者交给包管理器。
- 配置和数据格式带版本号，升级时自动迁移，并保留旧版本的备份。

### 9.2 后台托管（各平台）

| 平台 | 桌面（GUI） | 服务器 / 无界面（CLI） | 要注意的坑 |
|---|---|---|---|
| Linux | XDG autostart（`~/.config/autostart/*.desktop`） | systemd **user** service，`Restart=on-failure` | **默认用户退出登录后 user service 会被停掉**，要执行 `loginctl enable-linger $USER`，也可以改用 system service |
| macOS | 登录项（Wails 自带的开机自启） | launchd LaunchAgent，`KeepAlive` + `RunAtLoad` | App Nap 可能让后台程序的定时器变慢：引擎运行时要声明后台活动，并实测确认；合盖睡眠时程序本来就不会跑 |
| Windows | 登录时启动（注册表 Run 键或开始菜单启动项） | Windows 服务（`golang.org/x/sys/windows/svc`），设置“失败后重启”；或者用计划任务“登录时运行” | 用 LocalSystem 运行的服务读不到用户凭据管理器里的密钥，服务模式要用单独的密钥方案；还有“快速启动”和现代待机对唤醒的影响 |
| Docker | — | 官方镜像，`restart: unless-stopped`，数据目录挂卷 | 端口只绑 127.0.0.1（如果开了状态页） |

另外：
- systemd 加 `WatchdogSec`，程序定期 `sd_notify("WATCHDOG=1")`。主循环卡死时会被 systemd 自动重启。
- `e5renew service install` 一条命令完成上表里的配置（包括提示开启 linger），`service status` 检查是否真的在跑。

### 9.3 文档和引导

**GUI 首次运行向导**
1. 登录（推荐浏览器登录；没有浏览器时用设备码）
2. 检测权限，列出可用的 API；缺权限时给出 Entra 后台的具体操作步骤
3. 立刻跑一轮试调用，展示结果
4. **开启开机自启和后台运行（默认勾选）**，并解释关掉窗口后程序仍在托盘里运行
5. 设置提醒方式（桌面通知 / Webhook）

**CLI 引导**
- `e5renew init`：交互式完成登录、选 API、试调用、`service install`。
- 每个子命令出错时给出下一步该做什么，而不只是打印错误。

**`e5renew doctor`（GUI 里也有同样的体检页）**
- 后台服务或托盘进程在运行吗？开机自启开了吗？Linux 上 linger 开了吗？
- 每个账号：令牌是否有效、Secret 还剩几天、上次成功是什么时候、有没有被自动暂停
- 系统时间准不准（与 Graph 响应头里的 `Date` 比较）、代理和网络是否可达
- 每一项都带修复建议或一键修复

**文档**（`docs/user/`）
- 各平台的安装和“如何让它一直在后台跑”，按 Windows、macOS、Linux 桌面、Linux 服务器、Docker 分开写
- Entra 应用注册图文教程（委托身份和应用身份各一份），附推荐的权限清单
- 常见问题：电脑睡眠时会不会跑、关机多久会出问题、令牌过期怎么办、怎么确认它真的在工作
- 明确说明：不保证续期；电脑关机或睡眠期间不会调用，长期用建议放在常开的机器或服务器上
