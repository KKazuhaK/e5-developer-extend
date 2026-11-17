# 检查更新与一键升级

本文定下“发现新版本”和“一键升级”的做法。发布物的构成见 [release.md](release.md)，GUI 的整体结构见 [gui.md](gui.md)。

已经定下的前提：

- 版本来源只有 GitHub Release（`KKazuhaK/e5-developer-extend`），不另建更新服务器。
- 安装包本身**没有代码签名**（见 [signing.md](signing.md)），所以程序自己升级时的信任根是**我们自己的发布签名**：
  发布流水线用一把 Ed25519 密钥给 `SHA256SUMS` 签名，公钥编进程序。验证不过就不装，只给“打开发布页”。
- 开发构建（`version.Version` 是 `dev`，或形如 `0.0.0-dev.g<提交号>`）**从不**自动检查、不弹提示、不自动升级。
- 检查更新只读公开的 GitHub API，不带任何凭据；不上传任何用户数据。

## 1. 决策一览

| 问题 | 决定 | 原因 |
|---|---|---|
| 签名格式 | minisign 兼容的 Ed25519（只用“不预哈希”的 `Ed` 形式），文件 `SHA256SUMS.minisig` | 用户可以用现成的 `minisign -Vm SHA256SUMS -P <公钥>` 自己验证；Go 里用标准库 `crypto/ed25519` 自己解析，不引入依赖 |
| 为什么不用 minisign 默认的 `ED`（预哈希） | 不支持 | 预哈希要 BLAKE2b，标准库没有；`SHA256SUMS` 很小，不预哈希没有任何损失 |
| 谁来签名 | 发布流水线的 `publish` 任务，用 `go run scripts/release/sign-sums.go`，私钥来自仓库密钥 `RELEASE_SIGNING_KEY` | 不需要在 runner 上安装 minisign；格式由同一份代码生成和验证 |
| 没有私钥密钥时 | `publish` 任务照常成功，只是不产生 `.minisig`，打印警告 | 不能因为没配密钥让发版失败；程序侧看到没有签名就拒绝自动安装 |
| 公钥 | 编进 `internal/update/pubkey.go`；仓库里是**明显的占位符**，由所有者运行 `scripts/release/gen-signing-key.go` 生成后替换 | 密钥只能由所有者生成，AI 或脚本不能代劳、不能提交私钥 |
| 公钥是占位符时 | 仍然能“发现新版本”，但**只提供“打开发布页”**，不下载、不安装 | 没有信任根就不能装任何东西 |
| 检查结果存在哪 | 数据目录里的 `update.json`（不是 `state.json`） | `state.json` 只有引擎一个写者（引擎把整份状态放在内存里反复整体写回）；命令行和桌面窗口也会检查，写进去会被引擎覆盖，甚至覆盖掉引擎刚写的内容 |
| “每个版本只通知一次” | 记在 `state.json` 的全局通知记录里（`update_available`，key 是版本号） | 沿用现有告警机制：冷却、免打扰、投递失败重试都直接可用；这份记录只有引擎写 |
| 稳定/预发布 | 配置 `[update] channel = "stable"`（默认）或 `"prerelease"` | 稳定通道用 `/releases/latest`（GitHub 保证它不是预发布或草稿）；预发布通道读最近 15 个发布，按 semver 选最高的 |
| “稍后”（不再提示这个版本） | 窗口里按版本记在浏览器存储里 | 只是界面的偏好，不是需要可靠保存的状态 |
| 托盘菜单的“升级到 x.y.z” | 打开窗口的概览页（横幅在那里） | 托盘进程不带窗口服务；升级过程会重启托盘本身，由窗口进程来做更安全 |

## 2. 发布签名

### 2.1 格式

`SHA256SUMS.minisig` 是标准的 minisign 签名文件：

```
untrusted comment: signature from e5extend release key
base64( "Ed" || 密钥ID(8 字节) || Ed25519 签名(64 字节) )
trusted comment: timestamp:<秒>	file:SHA256SUMS
base64( 对 (签名 || trusted comment) 的 Ed25519 全局签名 )
```

公钥是 `base64("Ed" || 密钥ID || 公钥(32 字节))`，以 `RW` 开头，正是 `minisign -P` 接受的写法。
程序验证时检查：密钥 ID 一致、对文件的签名、对 trusted comment 的签名；遇到预哈希的 `ED` 明确报错。
已用真实的 minisign 0.12 双向互验过（我们签、minisign 验；minisign 签、我们验），后者作为测试向量放进了单元测试。

用户自己验证：

```bash
minisign -Vm SHA256SUMS -P RWxxxxxxxx...     # 公钥写在 internal/update/pubkey.go 和 README 里
sha256sum --ignore-missing -c SHA256SUMS
```

### 2.2 所有者要做的事（只做一次）

```bash
go run scripts/release/gen-signing-key.go            # 在一台可信的机器上
#   打印公钥；把私钥写进 e5extend-release-signing.key（权限 0600，已存在则拒绝覆盖）
# 1. 把打印出的公钥填进 internal/update/pubkey.go 的 PublicKeyText，提交
# 2. 把私钥存成仓库密钥，然后删掉本地文件（另存一份到密码管理器）
gh secret set RELEASE_SIGNING_KEY < e5extend-release-signing.key
shred -u e5extend-release-signing.key
```

之后每次打 `v*` 标签，`publish` 任务会签名并把 `SHA256SUMS.minisig` 一起放进草稿 Release。

私钥丢失或泄露：生成新密钥，发一个新版本（公钥变了，旧版本里的公钥验不过新签名，用户要手动装一次）。
**不要**用同一个版本号重新签名。轮换前发布的版本仍用旧公钥验证，所以旧版本无法自动升级到用新密钥签名的版本——这是预期的代价，写进发布说明。

### 2.3 流水线

`release.yml` 的 `publish` 任务在算出 `SHA256SUMS` 之后：

- 有 `RELEASE_SIGNING_KEY`：`go run scripts/release/sign-sums.go dist/release/SHA256SUMS` 生成 `.minisig`（`setup-go` 已在该任务中加入）；
  脚本签完后用编进程序的公钥再核对一次，公钥还是占位符或与私钥不匹配时打印 `::warning::`（签名仍然产生）。
- 没有：打印 `::warning::` 并跳过，任务成功。
- 密钥只通过环境变量传给这一步，不写文件、不进日志。`RELEASE_SIGNING_KEY` 不会暴露给来自 PR 的运行：签名只在标签触发的 `publish` 任务里发生。
- `SHA256SUMS` 在签名**之后**不再改动；`.minisig` 不出现在 `SHA256SUMS` 里（签名文件不能给自己签名）。

发布说明模板和 `release.md` 的发布物表都加上这个文件。

## 3. 检查更新

### 3.1 请求

`GET https://api.github.com/repos/KKazuhaK/e5-developer-extend/releases/latest`（预发布通道：`/releases?per_page=15`）。

- 不带 `Authorization`；`Accept: application/vnd.github+json`；`User-Agent: e5extend/<版本>`。
- 走配置里的 `network.proxy`（和 Graph 请求同一个 `auth.NewHTTPClient`）；超时 20 秒。
- `ETag` / `If-None-Match`：收到 304 就沿用 `update.json` 里的上次答案，304 不消耗限额。
- 限流：`429`，或 `403` 且 `X-RateLimit-Remaining: 0` / 有 `Retry-After` → 记 `not_before`（`Retry-After` 或 `X-RateLimit-Reset`），在此之前不再请求。
- 其他失败（网络、5xx）→ 指数退避：10 分钟起，翻倍，最长 6 小时；失败**不影响**引擎的任何其他工作。
- 响应体最多读 8 MiB。404（还没有发布）当作“没有新版本”，不算失败。
- 传输错误里去掉请求地址（代理地址可能带密码）。

### 3.2 版本比较

自己实现的 semver（`internal/update/semver.go`）：预发布低于正式版，数字标识按数值比较，数字低于字母。
`IsDevBuild`：`dev`、解析不出版本、或预发布第一段是 `dev`。开发构建的 `Newer()` 永远是 false。

### 3.3 频率和位置

- 引擎（后台服务或托盘里的引擎）的每个 tick 看 `update.json`：距上次成功检查满 24 小时才请求（失败时按退避）。检查在独立 goroutine 里进行，不拖慢轮次。
- 命令行 `e5extend update check` 和窗口里的“立即检查”总是请求（`force`），也更新 `update.json`。窗口打开时若缓存已超过一天且没开 `check = false`，会静默检查一次（引擎没在运行的机器上也能提示）。
- `update.json` 的内容：`channel`、`etag`、`checked_at`、`attempted_at`、`not_before`、`failures`、`latest`（标签、版本、页面地址、是否预发布、发布时间）。
  损坏或版本更新的文件当作空缓存。换了通道就重新检查。离线时一切照旧，只是不更新。

### 3.4 配置

```toml
[update]
check   = true        # 默认 true；false 时引擎不自动检查，命令行和窗口的手动检查仍可用
channel = "stable"    # stable | prerelease
```

### 3.5 通知、doctor、命令行

- 新事件 `update_available`（级别 info），消息写明新旧版本、发布页地址和升级命令。**每个版本只通知一次**：
  引擎的 `Evaluate` 里加一个全局作用域的评估——有更新且这个版本还没记录就 `announce`（key = 版本号）；用户升级之后记录自动清除；之后又出更新的版本会再通知一次。
  它走现有渠道、事件过滤（`notify.events`）、冷却、免打扰和投递重试。内置中英文标题和提示，模板预览有示例。
- `e5extend doctor` 新增一行 `update`：有新版本时是 `ok` 级别的提示（不影响退出码），写明版本和 `e5extend update install`；只读 `update.json`，离线可用，开发构建和 `check = false` 不显示。
- 命令行：
  - `e5extend update check [--json]`：现在就查，打印当前/最新版本、发布页、这份安装能不能自动升级及原因。有新版本时退出码 0（信息，不是错误）。
  - `e5extend update install [--yes]`：先查；有新版本才继续；显示将要做的事（安装方式、下载的文件）并要求确认（`--yes` 跳过，非交互终端必须带 `--yes`）；做完说明做了什么、要不要重启。

## 4. 识别安装方式，决定怎么升级

`internal/update/detect.go` 的 `Detect` 只看可注入的环境（GOOS、可执行文件路径、文件是否存在、环境变量、`dpkg -S`/`rpm -qf` 的结果），不碰真实系统，所以能在测试里描述任何一台机器。

| 种类 | 判断 | 下载的文件 | 升级动作 |
|---|---|---|---|
| Docker | Linux 且有 `/.dockerenv` 或环境变量 `E5EXTEND_DOCKER` | — | 只通知；说明 `docker pull ghcr.io/kkazuhak/e5-developer-extend:<版本>` |
| Windows 安装包 | 可执行文件所在目录有 `uninstall.exe`，或就是 `%LOCALAPPDATA%\Programs\E5 Developer Extend` | `E5-Developer-Extend_<v>_windows_<arch>_setup.exe` | 后台启动 `setup.exe /S`（分离进程，用户级，无需管理员），然后**本进程立即退出**。安装程序自己结束托盘和后台任务、替换文件、重新启动任务，托盘原来在运行就静默重启（`installer.nsi` 已经这样做） |
| Windows 命令行 zip | 其他 Windows 安装 | `e5extend_<v>_windows_<arch>.zip` | 对目录里已有的 `e5extend.exe`、`e5extend-bg.exe`：先把正在运行的文件**改名**为 `.old`（Windows 允许改名但不允许覆盖或删除），再写入新文件，失败则改名回去；之后重启计划任务；`.old` 文件下次运行时清理 |
| macOS 应用 | 可执行文件路径含 `.app/Contents/MacOS/` | `E5-Developer-Extend_<v>_macos_universal.dmg` | `hdiutil attach -nobrowse -readonly` 挂载，`ditto` 复制到 `.app` 同目录的暂存名，把旧 `.app` 改名为 `.old`、暂存名改为 `.app`（失败则还原），删旧的，`hdiutil detach`。`.app` 所在目录不可写时不动手，说明手动拖拽的步骤。之后重启后台服务，并让用户重启应用 |
| macOS 命令行 | 其他 macOS 安装 | `e5extend_<v>_darwin_universal.tar.gz` | 从 tar.gz 取出 `e5extend`，写成同目录的 `.new` 再 `rename` 覆盖（原子）；目录不可写则给出 `sudo install` 命令；之后重启 launchd 服务 |
| Linux deb / rpm | `dpkg -S` / `rpm -qf` 认领了这个文件 | 命令行包；桌面包也装着（旁边有 `e5extend-gui`）时**两个包都下载** | root：直接 `apt-get install -y a.deb b.deb`（或 `dnf install -y`）一次装完；窗口里：`pkexec` 提权（找得到 `pkexec` 时）；命令行或没有 `pkexec`：**不动手**，打印确切的 `sudo ...` 命令，下载的文件保留在临时目录。两个包必须一起装（桌面包精确依赖同版本的命令行包） |
| Linux 独立程序 | 其他 Linux 安装 | `e5extend_<v>_linux_<arch>.tar.gz` | 同 macOS 命令行：原子替换，目录不可写则给 `sudo install` 命令；之后 `systemctl --user restart e5extend`（服务已安装时） |

升级之后的收尾：

- **后台服务**：已经安装的服务继续跑旧进程。升级成功后由 `ops` 调用服务管理器的新方法 `Restart`（systemd：`systemctl --user restart`；launchd：`launchctl kickstart -k`；计划任务：`/End` 再 `/Run`）。服务没装就什么都不做；重启失败只是警告，结果里照实说明。Windows 安装包升级不需要（安装程序自己做了）。
- **托盘和窗口**：新程序已经在磁盘上，但运行中的进程还是旧的。桌面版给出“立即重启”：写入已有的 `gui-quit` 文件让托盘退出，再用一个延迟几秒的分离进程重新启动托盘（只在托盘正在运行或开机启动已打开时）。
- 每一步都记入结果的 `steps`（稳定的 code + 英文文本），界面按 code 显示中文，命令行直接打印文本。

### 不会自动升级的情况（`Plan.Reason`）

`docker`、`dev`（开发构建）、`no_key`（公钥是占位符）、`unsupported`（无法识别的安装方式，或没有这个 CPU 架构的发布文件）。
这些情况界面只给“打开发布页”。另外有两种在**执行时**才发现：发布里没有签名（`ErrNoSignature`，在下载任何东西之前就拒绝）、没有写权限或没有管理员权限（给出手动命令）。

## 5. 安全

1. **先验证，后使用。** 流程固定：取发布信息 → 检查有签名文件 → 下载程序文件、`SHA256SUMS`、`.minisig` → 验证签名 → 校验程序文件的 SHA-256 → 才解包或运行。没有签名、公钥是占位符、签名不对、校验和不符，一律拒绝，不留下任何已安装的东西，临时文件删除。
2. 下载地址只允许 `https://github.com/KKazuhaK/e5-developer-extend/releases/download/` 开头（重定向只允许 https，最多 5 次）；文件名不许含路径分隔符；单个文件最大 400 MiB，解出的程序最大 300 MiB。
3. 解包只取预期的文件名（tar 里的 `e5extend`、zip 里的 `e5extend.exe`/`e5extend-bg.exe`），**不用归档里的路径**拼目标路径，没有路径穿越。
4. 只升级到**更新**的版本（`Newer`），不降级；开发构建拒绝。
5. 工作目录是 `os.MkdirTemp` 创建的 0700 目录；超过一天的旧工作目录在下次升级时清理。
6. 提权只用系统的 `pkexec` 或让用户自己 `sudo`；程序不接触、不保存任何密码。
7. 命令行升级需要确认；窗口里要点“立即升级”。从不静默自动升级。
8. 日志和错误里不写代理地址、不写下载地址的查询参数。

## 6. 桌面版

### 6.1 后端（`gui/services/update.go`）

`UpdateService`（Wails 服务），方法：

- `Info() UpdateInfo`：不联网，读 `update.json` 和安装方式。字段：当前版本、最新版本、是否有新版本、发布页、通道、上次检查时间、`plan`（`mode` auto/manual、`reason`、`kind`）、`dev`、`checkEnabled`。
- `Check() UpdateInfo`：联网检查（强制），返回新的信息；失败时 reject，错误里带原因（限流、网络）。
- `Install() error`：立即返回，在后台升级；进度和结果通过事件推送。同一时间只允许一个升级（`Backend.begin()` 保证窗口关闭时等它做完）。
- `Restart() error`：升级完成后重启托盘/应用（见第 4 节）。
- `OpenReleasePage()`：打开发布页。

事件 `update:progress`：`{stage: "download"|"verify"|"install"|"restart"|"done"|"error", file, done, total, result?, error?}`。
`result` 就是 `update.Outcome`（步骤、手动命令、是否需要退出/重启）。Windows 安装包：先推送 `done`，约 1.5 秒后窗口进程退出，让安装程序接手。

`ops` 层（`internal/ops/update.go`）提供命令行和 GUI 共用的 `UpdateInfo`、`UpdateCheck`、`UpdateInstall`，负责：读配置（代理、通道、是否开启）、构造 `update.Service` 和 `update.Installer`（注入真实的 exec、`os.Executable`、文件系统）、升级后重启服务。测试里全部注入假的。

### 6.2 界面

- **概览页横幅**：有新版本且这一版没点过“稍后”时显示：版本号、“发布说明”链接、“立即升级”按钮（升级中显示进度条和阶段：下载中 / 验证中 / 安装中）、“稍后”（只对这个版本隐藏）。
  不能自动升级时按钮换成“打开发布页”，并说明原因（Docker、开发构建、没有签名公钥、无法识别的安装方式）。
  升级结果：成功（列出做了什么；需要时显示“立即重启”）；需要手动操作时显示确切的命令和“复制”按钮；失败显示原因。
- **关于页**：当前版本、最新版本、上次检查时间、“立即检查”；有新版本时同样的“立即升级”。
- **托盘菜单**：有新版本时多一项“升级到 x.y.z”（`Snapshot` 带上缓存里的信息，托盘每 30 秒读一次，不联网），点击打开窗口的概览页。
- 中英文文案；Mock 变体 `?update=available|downloading|error|none`（还有 `manual`、`restart`，用来看手动命令和“立即重启”）。

## 7. 测试

不联网：GitHub API 和下载地址都指向 `httptest` 假服务器；`exec`、文件系统路径、环境变量全部注入。

- semver 与通道、发布 JSON 解析、`PickLatest`；
- ETag/304、限流、退避、每日节奏、`update.json` 在多个进程间共享、开发构建不自检；
- 资产选择矩阵（每种安装方式 × amd64/arm64，预发布文件名）；
- 签名：真实 minisign 的输出、我们自己的签名、被篡改的消息、被改的 trusted comment、别的密钥、预哈希格式、空/缺失签名、占位符公钥；
- 校验和不符、文件不在签名列表里；
- 安装方式识别（可注入的文件系统和环境）；
- Linux 独立程序、Windows 改名替换（含回滚）、macOS `.app` 替换（假的 `hdiutil`/`ditto`）、deb/rpm 命令（root、`pkexec`、手动 `sudo`）、Windows 安装包只启动一次且带 `/S`、目录不可写时给手动命令；
- 引擎：一天一次、只通知一次、升级后清除、离线不出错、`check = false`；
- GUI 服务：假 `update.Service`/`Installer`，进度事件的顺序、只允许一个升级。

## 8. 需要在真机上验证的

这些只能在对应平台上确认，CI 里没有：

- Windows：从旧版本的安装包一键升级到新版本（安装程序接手、托盘和后台任务被重新启动）；命令行 zip 的改名替换在后台任务运行时可用；
- macOS：`hdiutil attach`、`ditto` 之后 `.app` 能打开（Gatekeeper 对未经浏览器下载的文件不加隔离标记，需要确认确实如此）；`launchctl kickstart -k`；
- Linux：`pkexec` 弹出授权窗口、`apt-get`/`dnf` 一次装两个包；`systemctl --user restart`；
- 真实的发布流程：设置密钥后打一个预发布标签，确认 `SHA256SUMS.minisig` 出现在草稿 Release 里，并能被 `minisign -Vm` 验证。

## 9. 以后再做

- 差分升级、自动在安静时段升级（现在从不静默升级）。
- 签名密钥轮换时的双公钥过渡。
- 命令行升级时的 `pkexec`（现在只在窗口里用，命令行给 `sudo` 命令）。
