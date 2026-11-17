# 改名：E5 Renew → E5 Developer Extend

状态：**计划，等待所有者批准**。批准前不改代码。

## 1. 为什么、为什么是现在

- “E5 Renew”和 Microsoft365_E5_Renew_X 系列以及几十个 `e5renew` / `E5_Renew` 仓库重名，搜索时找不到本项目；
  SignPath Foundation 的申请表要求“用 Google 搜这个名字应能明确找到你的项目”（见 `docs/design/signing.md`）。
- 2026-11-16 检查：GitHub、Homebrew 和网页搜索里都没有 “E5 Extend” / `e5extend` / `e5-extend`；GitHub 上也没有 “E5 Developer Extend” / `e5-developer-extend`。
- 所有者选了完整名字 **E5 Developer Extend**（比 “E5 Extend” 更清楚是给开发者计划用的，也不涉及在名字里用 Microsoft 商标）。程序名、目录名等标识仍用短的 `e5extend`。
  搜索结果多是 TP-Link 的 “Archer Air E5” 扩展器，所以副标题要写明 “Microsoft 365”。商标检索（USPTO）由所有者做。
- **v0.1.0 才发布几个小时**，装过的人极少（已知只有所有者自己的开发机）。现在改名，迁移成本最低；
  晚一点（尤其是自动创建应用注册上线以后，会在用户租户里留下带旧名字的应用）就贵得多。

## 2. 命名表

| 用途 | 旧 | 新 |
|---|---|---|
| 产品显示名 | E5 Renew | **E5 Developer Extend**（窗口标题、安装包、菜单、文档标题） |
| 中文显示名 | E5 Renew | **待定**（候选：中英文都用 “E5 Developer Extend”，或 “E5 开发者续期”） |
| 副标题 | — | “Keep your Microsoft 365 E5 developer tenant active” / “让 Microsoft 365 E5 开发者租户保持活跃” |
| 命令行程序 | `e5renew` | `e5extend` |
| 桌面程序 / Windows 后台程序 | `e5renew-gui` / `e5renew-bg` | `e5extend-gui` / `e5extend-bg` |
| Go 模块 | `github.com/KKazuhaK/e5-renew` | `github.com/KKazuhaK/e5-developer-extend` |
| GitHub 仓库 | `KKazuhaK/e5-renew` | `KKazuhaK/e5-developer-extend`（所有者操作；GitHub 会自动重定向旧地址） |
| 反向域名标识 | `io.github.kkazuhak.e5renew`（`.tray`） | `io.github.kkazuhak.e5extend`（`.tray`） |
| 配置 / 数据目录名 | `e5renew` | `e5extend` |
| 环境变量 | `E5RENEW_*` | `E5EXTEND_*`（旧名字继续读一个版本，见 §4.5） |
| 钥匙串服务名 / Windows 凭据名 | `e5renew` / `e5renew-master-key` | `e5extend` / `e5extend-master-key` |
| systemd 单元 / launchd 标签 / 计划任务 | `e5renew.service` / `io.github.kkazuhak.e5renew` / `e5renew` | `e5extend.service` / `io.github.kkazuhak.e5extend` / `e5extend` |
| Windows 安装目录 / 开始菜单 | `%LOCALAPPDATA%\Programs\E5 Renew` | `%LOCALAPPDATA%\Programs\E5 Developer Extend` |
| macOS 应用 | `E5 Renew.app` | `E5 Developer Extend.app` |
| Linux 包 | `e5renew`、`e5renew-desktop` | `e5extend`、`e5extend-desktop` |
| Docker 镜像 | `ghcr.io/kkazuhak/e5-renew` | `ghcr.io/kkazuhak/e5-developer-extend`（跟仓库名一致；旧镜像保留不删，不再更新） |
| 发布文件名 | `e5renew_*`、`E5-Renew_*` | `e5extend_*`（命令行）、`E5-Developer-Extend_*`（桌面版） |
| 备份文件 | `.e5backup`，头部 `E5RNWBAK` | 扩展名不变（不含产品名）；头部改为 `E5XTDBAK`。备份功能还没发布，**不需要兼容旧头部** |
| Entra 应用注册（自动创建功能，未发布） | `E5 Renew`，标签 `e5renew` | `E5 Developer Extend`，标签 `e5extend`；自动创建时**也识别**旧名字 `e5renew` / `E5 Renew` 的应用并提供“采用并改名” |

**不改的**：调用记录、配置文件的格式和字段；`config.toml` 里的 `version = 1`；Graph 调用本身。

## 3. 改动清单（约 120 个文件、930 处）

按类型分批，每批一个提交，便于审查：

1. **标识常量**（真正影响行为的部分，集中在少数几个文件）：
   `internal/paths/paths.go`（`appDir`、环境变量）、`internal/vault/masterkey.go`（钥匙串服务名、凭据名、环境变量）、
   `internal/service/service.go`（`UnitName`、`Label`、`TaskName`）、`internal/service/launchd.go`（日志目录）、
   `internal/service/schtasks.go`（任务 XML 文件名）、`gui/shared.go`（`autostartID`、CLI 程序名）、`gui/main.go`。
2. **Go 模块路径**：`go.mod` 和所有 import（机械替换）；`cmd/e5renew` → `cmd/e5extend`。
3. **打包**：`packaging/windows/installer.nsi`、`packaging/linux/*`（包名、`.desktop`、图标文件名、Replaces/Conflicts）、
   `packaging/darwin/Info.plist`、`scripts/release/*`、`Dockerfile`、`.github/workflows/*`、`packaging/release-notes.md`。
4. **界面文字**：`gui/frontend/src/config.ts`（`AppName`）、`i18n`、`api/mock.ts`、托盘菜单。
5. **测试和 golden 文件**：`internal/service/testdata/*.golden` 等，随常量更新。
6. **文档**：README（与“中英双语 README”一起做时不重复劳动，这里只改名字）、`docs/**`、CHANGELOG。
   CHANGELOG 里 v0.1.0 一节保持原样（那时就叫 E5 Renew），新一节写明改名和迁移。

## 4. v0.1.0 的安装怎么升级

核心是一个**可重复执行**的迁移，入口是 `e5extend migrate`，并且会被自动调用（§4.4）。每一步执行前先检查，做过的不再做；中途失败的下次继续。

### 4.1 配置和数据目录

| 系统 | 旧 → 新 |
|---|---|
| Linux | `~/.config/e5renew` → `~/.config/e5extend`；`~/.local/share/e5renew` → `~/.local/share/e5extend`（遵守 `XDG_*`） |
| macOS | `~/Library/Application Support/e5renew` → `…/e5extend`；`~/Library/Logs/e5renew` → `…/e5extend` |
| Windows | `%APPDATA%\e5renew` → `%APPDATA%\e5extend`；`%LOCALAPPDATA%\e5renew` → `%LOCALAPPDATA%\e5extend` |

- 新目录**不存在**、旧目录存在 → 整个目录**改名**（同一文件系统上是原子操作），旧位置留一个说明文件 `MOVED-TO-e5extend.txt`。
- 新旧都存在 → 不动，`doctor` 报警并说明怎么处理（通常是已经迁移过，旧目录是残留）。
- 移动前先停引擎（拿引擎锁；拿不到就先让旧服务停下，见 §4.3）。
- 用 `E5RENEW_CONFIG_DIR` / `E5RENEW_DATA_DIR` 指定过自定义目录的（例如 Docker）：**不移动**，只是继续读这个变量（§4.5）。

### 4.2 主密钥

- 钥匙串 / 凭据管理器：读出旧条目 → 写入新条目 → 用新条目解密一次 vault 验证 → 删除旧条目。验证失败就保留旧条目、报错。
- key file（`master.key`）跟着配置目录一起移动，不需要单独处理。
- 远程会话读不到钥匙串时（`ErrRemoteSession`）跳过这一步，提示“在桌面会话里打开一次 E5 Developer Extend”。新版本在新条目不存在时会**回退读取旧条目**，所以即使这一步还没做，程序也能用。

### 4.3 后台服务

新程序发现旧服务已安装（`e5renew.service` / 旧 launchd 标签 / 计划任务 `e5renew`）时：

1. 记下旧服务的选项（Linux 的 `--key-file`、linger 状态）。
2. 用同样的选项安装新服务（指向新程序），确认它起来了（心跳）。
3. 停止并删除旧服务。

顺序是“先装新、再删旧”，任何一步失败都不会让用户一个服务都没有。

### 4.4 谁来触发迁移

| 系统 | 触发方式 |
|---|---|
| Linux（deb/rpm） | 新包 `e5extend` 声明 `Replaces/Conflicts/Provides: e5renew (<< 0.2.0)`，升级时自动替换旧包。**新包里带一个过渡用的 `/usr/bin/e5renew`**（一行脚本：`exec /usr/bin/e5extend "$@"`）。旧的 systemd 单元执行的是 `/usr/bin/e5renew run`，它会通过过渡脚本启动新程序，新程序启动时执行迁移，安装新单元、删除旧单元，然后由新单元接手。这样服务器升级包以后**什么都不用做**。过渡脚本在 0.3.0 删除。 |
| Linux（tar 包） | 用户替换程序后第一次运行任何 `e5extend` 命令时迁移；旧单元指向的旧程序还在，所以在那之前旧版照常运行。发布说明里写一行。 |
| Windows | 新安装包检测到旧的卸载项（`…\Uninstall\io.github.kkazuhak.e5renew`）时：结束旧进程、记下旧计划任务是否存在、静默运行旧的卸载程序（它会删除旧任务；用户数据本来就保留），安装新版本后运行 `e5extend.exe migrate`（移动数据目录、迁移凭据、旧任务存在过就安装新任务），最后启动托盘。 |
| macOS | 新的 `E5 Developer Extend.app` 第一次启动时迁移：卸载旧 LaunchAgent、移动目录、迁移钥匙串条目、安装新 LaunchAgent；并提示“可以把 Applications 里的 E5 Renew 删掉”。旧应用的开机启动项（`io.github.kkazuhak.e5renew.tray`）一并删除。 |
| Docker | 新镜像名不同，用户自己改 compose；数据卷挂载在 `/data`，用环境变量指定目录，**数据不需要移动**（§4.5）。文档给出改 compose 的一行。 |
| 所有系统 | 每个命令启动时都做一次廉价检查（只看目录是否存在）；需要迁移就执行目录和密钥部分。服务部分只在 `migrate`、托盘启动、引擎启动时执行。 |

### 4.5 兼容期（0.2.x）

- 环境变量：`E5EXTEND_*` 优先；没有时读 `E5RENEW_*`，`doctor` 提示改名。Docker 镜像里两个都设。
- 钥匙串：新条目不存在时回退读旧条目（§4.2）。
- 0.3.0 删除所有兼容代码和 `/usr/bin/e5renew` 过渡脚本。

## 5. 测试

- 单元测试：迁移的每一步（全新、只有旧目录、新旧都有、迁移到一半中断后再次运行、自定义目录、钥匙串迁移及验证失败、旧服务存在 / 不存在、先装新再删旧中任一步失败）。
- **真实升级测试**：
  - Linux：在这台机器上，从 v0.1.0 的 deb 升级到新 deb，确认服务不停、账号还在、不用重新登录、旧单元被删除。
    （先把当前在跑的 v0.1.0 安装用备份功能导出一份，作为保险。）
  - Windows：在测试虚拟机里装 v0.1.0、注册任务，再装新版本，确认任务换成新名字、数据在、凭据迁移成功、旧卸载项消失。
  - macOS：只有 CI 冒烟测试可用；真机升级流程在发布说明里标为“未在真机验证”。
- CI 的 `service-e2e` 加一个“从旧名字升级”的场景（Linux、macOS、Windows runner 都有）。

## 6. 和其他工作的顺序

1. `feat/backup` 合并（备份头部在本次改名里一起改）。
2. **改名**（本计划）。PR 合并前，所有者改 GitHub 仓库名；合并后发布 0.2.0 时推送新镜像名。
3. 设置页、自动创建应用注册、API 推荐、订阅到期检测、双语 README 和截图，全部直接用新名字。
4. 签名文档 PR #8 里的产品名在它合并前或改名 PR 里一起更新。

## 7. 需要所有者决定

1. **中文显示名**：中英文都用 “E5 Developer Extend”（推荐，搜索和签名申请时名字唯一），还是 “E5 开发者续期”？
2. **命令行程序名**：`e5extend`（推荐，和包名、目录名一致），还是更短的名字？
3. **Linux 过渡脚本** `/usr/bin/e5renew`：保留一个版本（推荐），还是不要？
4. **仓库改名时间**：建议在改名 PR 合并前改（Go 模块路径要和仓库地址一致）。
5. 商标检索（USPTO 等）由你来做；有问题的话在实现开始前告诉我。
