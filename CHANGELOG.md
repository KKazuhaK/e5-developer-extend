# 变更记录

格式参照 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## 未发布

### 新增

- 检查更新和一键升级。引擎每天查一次 GitHub 上有没有新版本（走配置的代理，支持 `ETag`，遇到限流或离线按退避重试，开发构建从不检查），结果存在数据目录的 `update.json`。`config.toml` 新增 `[update]`：`check = true`（默认；`false` 只关闭每天的自动检查，手动检查不受影响）、`channel = "stable"`（默认）或 `"prerelease"`。新增告警事件 `update_available`（提示级别，每个版本只通知一次，走现有渠道、过滤、免打扰和重试）；`doctor` 在有新版本时多一行 `update`（不影响退出码）；`e5extend update check [--json]`、`e5extend update install [--yes]`。
- 升级前验证发布签名：发布流水线用 Ed25519 密钥给 `SHA256SUMS` 签名（minisign 兼容格式，文件 `SHA256SUMS.minisig`，可用 `minisign -Vm SHA256SUMS -P <公钥>` 自己验证），仓库密钥 `RELEASE_SIGNING_KEY` 不存在时发布照常成功、只是没有签名，程序也就只提示新版本并给出发布页。公钥编进程序（`internal/update/pubkey.go`，`RWQh2HcJZFOJwdJXpwW2YFjr8hItSVKWgwOQWcMS0lbd7ZJo0I2HTZ41`），私钥是仓库密钥 `RELEASE_SIGNING_KEY`；细节见 [docs/design/update.md](docs/design/update.md) 和 [docs/design/release.md](docs/design/release.md)。
- 按安装方式升级：Windows 安装包（静默运行新安装包）、Windows 命令行 zip（改名替换，正在运行的文件先改名为 `.old`）、macOS 应用（挂载 dmg 替换 `.app`）、macOS / Linux 的 tar 包（原子替换）、Linux deb / rpm（命令行包和桌面包一起装；桌面版用 `pkexec`，命令行不是 root 时给出确切的 `sudo` 命令）；Docker 只通知。升级后自动重启后台服务（服务管理器新增 `Restart`），先验证签名和 SHA-256，任何一步失败都不会改动现有程序。详见 [docs/design/update.md](docs/design/update.md) 和 [安装 → 升级](docs/user/install.md#升级)。
- 桌面版：概览页新版本横幅（版本号、发布说明、“立即升级”带进度、“稍后”按版本隐藏）、“关于”页的当前/最新版本和“立即检查”、托盘菜单“升级到 x.y.z”；Mock 变体 `?update=available|downloading|error|manual|restart|none`。
- Windows：`e5extend service install --headless` 让后台任务无人登录也运行（Windows Server / Server Core 开机后不用登录，用户注销后也不停）。任务的登录类型改为 S4U（不保存 Windows 密码），在登录触发器和重复触发器之外增加开机触发器（延迟 30 秒），失败重启等设置不变。因为 S4U 会话读不到用户的凭据管理器，主密钥会复制成配置目录里的 `master.key.dpapi`：用 DPAPI 本机范围加密，ACL 只授予当前用户、SYSTEM 和 Administrators。需要“作为批处理作业登录”权限（管理员默认有），注册任务通常要在管理员命令行里执行；权限不够时会打印完整的命令。`service status` 显示 `headless:`，`doctor` 新增 `headless` 项（密钥文件缺失、读不了、ACL 过宽）。卸载只在任务是 headless 时删除这个文件。其他平台上 `--headless` 会报错并指向 Linux 的 `--key-file` / `--enable-linger`。默认安装（登录时运行）不变。详见 [docs/user/background.md](docs/user/background.md) 和 [docs/design/service-doctor-notify.md](docs/design/service-doctor-notify.md)。
- 限流重试：收到 429（或带 `Retry-After` 的 503）时，按 `Retry-After`（秒数或 HTTP 日期）等待后重试同一个调用，同一轮里最多再试 2 次；要求等待超过 60 秒的直接跳过这次调用。限流不算失败：不计入连续失败（自动暂停）和 `failure_spike`，调用日志里的类别是 `throttled`。
- 接口不可用：某个接口返回 403 或 404 时，对这个账号暂停 24 小时（写进状态，重启后仍有效），之后几轮改调别的接口；只碰到这样的接口不算这一轮失败；一轮里没有任何成功、而且账号能调用的接口全部被拒绝（许可证或邮箱被收回）时才算失败，连续 3 轮自动暂停。自动暂停后的探测不会只凭令牌恢复，也会跳过已暂停的接口。手动恢复（`account resume`）和探测恢复会同时清除这些 24 小时的接口暂停，账号不会因为没有可调用的接口而马上再次自动暂停。`e5extend status`、`doctor`（`account.<名称>.apis`）、桌面版概览和账号页显示“N 个 API 暂停 24 小时”，以及今日限流次数。
- 自动恢复：因连续失败被自动暂停的账号，在暂停后 6 小时、再过 12 小时、之后每 24 小时用一次轻量调用（`/me`，应用身份用 `/organization` 或最便宜的可用接口）探测；成功就自动恢复并发 `recovered`。手动暂停的账号不会被自动恢复；`invalid_grant`、需要重新登录等仍是 `needs_attention`，不探测。`status`、`doctor` 和桌面版显示下一次探测的时间。
- 重启后的补跑：启动时如果某个账号的下一轮已经错过很久，只在几分钟内（带抖动）补跑一轮，不补齐错过的轮次，调用日志里有 `catch_up` 事件。
- 每日摘要：新事件 `daily_summary`（提示级别，默认关闭）。设置 `notify.daily_summary_at = "21:00"` 后每天发一条，列出每个账号过去 24 小时的成功、失败、限流次数，最近成功时间，暂停或需要处理的状态，暂停的 API 数，以及已知时 E5 订阅的剩余天数。免打扰时段内到点的话在时段结束时发。所有渠道类型都有中英文内置模板，`notify test --event daily_summary` 和 `notify preview daily_summary` 可用；通用 Webhook 的请求体多一个 `summary` 数组。桌面版“设置 → 高级设置 → 告警”新增开关和时间。
- 调用节奏新增两档预设：**密集（同原版强度）**（8 到 17 分钟一轮，每轮 15 到 25 个 API）和**急救（临时）**（5 到 10 分钟一轮，每轮 20 到 30 个 API，只用于订阅到期前的几天，界面上有提示，过后请手动改回“标准”）。某一轮要的 API 数比账号能用的多时，调用能用的全部。
- API 目录新增只读接口：洞察（热门文档）、相关人员、在线状态（presence）、直属下级、用户设置、OneDrive 驱动器列表、日历组、OneNote 分区，以及 Bookings（业务和货币，默认关闭；货币只有委托身份）。新增三个只读权限，自动创建应用会一并申请：委托身份 `People.Read`、`Presence.Read`、`Bookings.Read.All`，应用身份 `Presence.Read.All`、`Bookings.Read.All`。已有的应用注册没有这些权限时，对应接口只是不会被选中，其他不受影响；要补权限可以重新运行 `e5extend app setup`。没有收 `/me/insights/used` 和 `shared`、`/me/manager`（没有上级的账号会返回 404）、`/me/drive/recent` 和 `sharedWithMe`（微软已停止返回数据）、`/me/settings/regionalAndLanguageSettings`（只在 beta 里）。详见 [docs/graph-api-catalog.md](docs/graph-api-catalog.md)。
- 按 API 统计调用次数：引擎为每个账号记录每个 API 的成功、失败、总次数和最近一次的结果（存在 state.json 里，每个 API 一条）。命令行 `e5extend stats [账号]`；桌面版“日志”页新增“按 API 统计”，可按列排序。
- 桌面版概览页新增红色描边的“续期失败？”按钮，打开检查清单（旧版沙盒订阅退役、开发者控制面板、订阅卡片、健康检查、没有保证），清单也写进了 [常见问题](docs/user/faq.md)。

### 变更

- 默认全天调用（`work_start` 和 `work_end` 都是 `"00:00"`，两者相等表示全天）；以前是 08:00 到 22:00。已有的配置不变。
- 默认每天都调用（以前只在周一到周五）。已有的配置不变；要改回只在工作日，在桌面版“设置 → 调用节奏”里选“仅工作日”，或在配置里写 `work_days = ["mon", "tue", "wed", "thu", "fri"]`。
- 返回 403 的接口现在在第一次就暂停 24 小时（之前连续 3 次后暂停 7 天），404 也一样；`failure_spike` 和“今日失败”不再把 403、404 和限流算作失败。
- 自动暂停的原因里说明会自动再试一次。

### 文档

- 常见问题新增微软 Legacy sandbox 订阅退役的说明（微软的 FAQ 于 2026 年 11 月发布，没有给出统一的退役日期）：仍然符合资格的成员得到最后一次 60 天的续期；不再符合资格的成员，订阅在当前订阅期结束时到期，之后只读 30 天，再永久删除；Legacy 订阅不能再次续期或转换。请备份数据并到开发者控制台确认，任何工具都改变不了这件事（见 [常见问题](docs/user/faq.md)）。README 的“先读这个”里也加了一条；文档和 README 都没有承诺续期。

## [0.2.0] - 2026-11-16

产品改名为 **E5 Developer Extend**（命令行 `e5extend`），并新增自动创建应用注册、API 推荐、订阅到期检测、异常通知等功能。

### 新增

- 桌面版的健康检查页按界面语言显示每一项的说明和修复建议（之前中文界面里这些仍是英文）。命令行输出不变；`e5extend doctor --json` 的每一项新增 `code`、`params`、`parts`、`fixCode`、`fixParams`、`fixParts` 字段（只增不改）。
- 自动创建应用注册：`e5extend app setup`、`e5extend account add --auto`，以及 `e5extend init` 里的“自动创建（推荐）”。用租户管理员账号登录一次（通过微软官方的 Microsoft Graph Command Line Tools，令牌只放在内存里），程序在租户里建好应用、加好只读权限并授予管理员同意；可重复执行，只补缺的，不删除任何东西；可以采用手工建的同名应用。应用身份还会创建一个以本机命名的 Client Secret，直接存进 vault。详见 [docs/user/app-registration.md](docs/user/app-registration.md) 和 [docs/design/app-setup.md](docs/design/app-setup.md)。
- 备份与迁移：`e5extend backup export` / `backup import` 把配置、secret、登录状态、告警地址和运行状态导出成一个用备份密码加密的文件，在另一台机器（可以是另一个系统）上导入，不用重新登录、不用重新输入 secret；支持合并和替换。桌面版设置页新增“备份与迁移”。详见 [docs/user/backup.md](docs/user/backup.md)。

- 账号密码方式的账号在刷新令牌被拒绝（过期、被撤销、条件访问的登录频率策略 AADSTS70043）时，后台引擎会用 vault 里加密保存的密码自动重新登录，后台不再因此停下。只有登录过一次的账号才会这样；两次自动登录至少间隔 30 分钟；密码错误、账号被锁定或禁用、需要 MFA、被条件访问拦截时进入 `needs_attention`，并且不再自动尝试，直到 `account set-secret`、`account login` 成功或修改账号身份。`doctor`、`account test` 和桌面版只静默刷新，不会用密码登录。设备码和浏览器登录的账号不变。
- 条件访问“登录频率”策略（AADSTS70043）有专门的提示：依次说明改用应用身份、账号密码（仅限无 MFA）、把应用从策略中排除三条路；`doctor` 和账号状态里单独报告这种情况；桌面版账号卡片上增加“改用应用身份”按钮，直接打开已填好名称的自动创建向导（旧账号保存了用户名时一并带上，否则需要自己填用户的邮箱）。详见 [常见问题](docs/user/faq.md)。
- API 推荐：登录之后探测账号实际能用哪些 API 分类（只读请求：邮箱设置、OneDrive、Teams、SharePoint 根站点），给每个分类标上推荐 / 不可用（带原因）/ 可能可用。命令行：`e5extend account test <名称> --recommend`，加 `--apply` 把推荐的分类写进账号；桌面版添加账号向导的“API 分类”移到登录之后，默认勾选推荐的。`e5extend doctor` 新增 `account.<名称>.apis`：某个分类连续 3 轮都是 404 时提醒。详见 [docs/design/api-recommend.md](docs/design/api-recommend.md)。

- 异常检测和多渠道通知：新增事件 `failure_spike`（最近几轮调用失败率过高；不把 404 和“账号没有这项服务”算作异常，判断和 `doctor` 的 `account.<名称>.apis` 一致）和 `all_failing`（所有未暂停的账号同时异常，至少 2 个账号）；`failure_spike` 解除时发 `recovered` 并写明原事件。新增通知渠道：Telegram、邮件（SMTP，带 Gmail / Outlook / QQ / 163 预设）、企业微信、飞书和钉钉（可选签名）、Discord、Slack、Bark、ntfy、Server 酱和通用 Webhook；每个渠道有最低级别和事件过滤，内置中英文模板，也可以用自定义模板（保存前检查）。所有令牌、密码、Webhook 地址和签名密钥存在 vault 的 `notify/<渠道名>/<字段>` 里。新增降噪：冷却时间（默认 6 小时，严重告警减半）、免打扰时段（只发严重告警，其余结束后合并成一条）。命令行：`notify channel list|add|edit|remove`、`notify test [名称]`、`notify preview <事件>`，`notify set-webhook` 保留为别名；桌面版“设置 → 高级设置 → 告警”管理渠道（测试、预览、降噪），没有渠道时基础设置页提示。旧的单个 Webhook 继续可用，显示为名为 `webhook` 的渠道，保存一次即迁移到新格式。详见 [docs/user/notifications.md](docs/user/notifications.md) 和 [docs/design/alerts.md](docs/design/alerts.md)。

- 订阅到期检测：引擎每天按租户读一次 `GET /directory/subscriptions`（同一租户的多个账号只读一次，失败按指数退避、最长 6 小时一次，不影响普通调用轮次），记录每个订阅的状态、到期时间和续期历史（最多 20 条）。新增事件 `subscription_expiring`（到期前 30 天警告，7、1 天严重）、`subscription_status`（状态变坏严重、恢复提示）和 `subscription_renewed`（到期时间后移），走现有的渠道、过滤、冷却和免打扰，内置中英文模板、预览和示例消息。命令行：`e5extend subscription [<账号>] [--json] [--cached] [--set-expires 日期|--clear-expires]`；`doctor` 新增 `subscription.<租户>`；桌面版概览页新增“E5 订阅”卡片（状态、到期日、剩余天数、续期记录、补齐权限、手动填写到期日）。没有 `Organization.Read.All` 时降级为说明，可用 `subscription_expires` 手动填写到期日；`subscription.skus` 指定要关注的 SKU（默认以 `DEVELOPERPACK` 开头的订阅）。`Organization.Read.All` 加入委托身份的推荐权限，已有的应用注册重新运行 `e5extend app setup` 即可补齐。缺 `Organization.Read.All` 时“补齐权限”（桌面版卡片）和 `e5extend app setup --repair --account <名称>`（任何账号；`--no-new-secret` 是 Client Secret 账号的旧写法）只给账号自己的应用注册补权限，不新建账号和 Secret；用域名、租户 ID 写同一个租户的账号合并成一个租户；通用 Webhook 对订阅事件多出 `tenant`、`subscription`、`sku`、`status`、`ends` 字段。详见 [docs/design/subscription.md](docs/design/subscription.md) 和 [入门](docs/user/getting-started.md#看订阅什么时候到期)。

- 文档：README 提供英文（默认）和中文两个版本，桌面版优先；入门指南改为以桌面版为主、带截图，并新增英文版 [getting-started.en.md](docs/user/getting-started.en.md)。截图在 `docs/images/`，由 `scripts/screenshots/` 从界面的演示数据（mock 后端的 `?demo=1`）重新生成。

### 变更

- 通用 Webhook 的请求体新增 `level`（以及恢复通知的 `origin`、免打扰汇总的 `items`）字段；原有字段不变。
- Webhook 地址现在要求 `https://`（指向本机的 `http://` 除外）；已经设置的旧地址仍按原样使用。
- `e5extend notify test` 在没有任何渠道时的提示文字改了，并且在有多个渠道时逐个报告结果。
- 桌面版添加账号向导的步骤顺序改为：登录方式、账号信息、登录、API 分类、测试、完成；新账号不再默认勾选全部分类。

- 桌面版窗口左上角改为应用图标。
- **改名**：E5 Renew 改名为 **E5 Developer Extend**，命令行程序、目录、环境变量、系统服务、包名和 Docker 镜像随之改为 `e5extend`（`E5EXTEND_*`、`ghcr.io/kkazuhak/e5-developer-extend`）。不提供从 v0.1.0 的自动迁移：装过 v0.1.0 的人请先用旧版运行 `e5renew service uninstall` 并卸载，再全新安装；旧的配置目录和钥匙串条目不会被读取或删除。详见 [docs/design/rename.md](docs/design/rename.md)。

## [0.1.0] - 2026-11-16

第一个版本。

### 新增

- 命令行程序 `e5renew`：配置、加密密钥库、四种登录方式（设备码、浏览器、Client Secret、账号密码）。
- Graph 只读 API 库和调度引擎：随机抽取和顺序、工作日与工作时段、睡眠唤醒和时钟跳变处理、连续失败自动暂停。
- 后台服务：systemd 用户服务、macOS LaunchAgent、Windows 计划任务；`e5renew doctor` 健康检查；Webhook 告警；`e5renew init` 向导。
- 桌面版 E5 Renew：托盘图标和主窗口（React），中英文界面。
- 发布包：Windows 安装包、macOS dmg、Linux deb/rpm、命令行压缩包、Docker 镜像（暂未代码签名）。

[0.2.0]: https://github.com/KKazuhaK/e5-developer-extend/releases/tag/v0.2.0
[0.1.0]: https://github.com/KKazuhaK/e5-developer-extend/releases/tag/v0.1.0
