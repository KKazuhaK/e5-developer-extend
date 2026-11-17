# 改名：E5 Renew → E5 Developer Extend

状态：**已批准并实施**（2026-11-16）。§7 的决定已定；v0.1.0 不做自动迁移（见 §4）。

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
| 中文显示名 | E5 Renew | **E5 Developer Extend**（不另起中文名） |
| 副标题 | — | “Keep your Microsoft 365 E5 developer tenant active” / “让 Microsoft 365 E5 开发者租户保持活跃” |
| 命令行程序 | `e5renew` | `e5extend` |
| 桌面程序 / Windows 后台程序 | `e5renew-gui` / `e5renew-bg` | `e5extend-gui` / `e5extend-bg` |
| Go 模块 | `github.com/KKazuhaK/e5-renew` | `github.com/KKazuhaK/e5-developer-extend` |
| GitHub 仓库 | `KKazuhaK/e5-renew` | `KKazuhaK/e5-developer-extend`（所有者操作；GitHub 会自动重定向旧地址） |
| 反向域名标识 | `io.github.kkazuhak.e5renew`（`.tray`） | `io.github.kkazuhak.e5extend`（`.tray`） |
| 配置 / 数据目录名 | `e5renew` | `e5extend` |
| 环境变量 | `E5RENEW_*` | `E5EXTEND_*`（不读旧名字） |
| 钥匙串服务名 / Windows 凭据名 | `e5renew` / `e5renew-master-key` | `e5extend` / `e5extend-master-key` |
| systemd 单元 / launchd 标签 / 计划任务 | `e5renew.service` / `io.github.kkazuhak.e5renew` / `e5renew` | `e5extend.service` / `io.github.kkazuhak.e5extend` / `e5extend` |
| Windows 安装目录 / 开始菜单 | `%LOCALAPPDATA%\Programs\E5 Renew` | `%LOCALAPPDATA%\Programs\E5 Developer Extend` |
| macOS 应用 | `E5 Renew.app` | `E5 Developer Extend.app` |
| Linux 包 | `e5renew`、`e5renew-desktop` | `e5extend`、`e5extend-desktop` |
| Docker 镜像 | `ghcr.io/kkazuhak/e5-renew` | `ghcr.io/kkazuhak/e5-developer-extend`（跟仓库名一致；旧镜像保留不删，不再更新） |
| 发布文件名 | `e5renew_*`、`E5-Renew_*` | `e5extend_*`（命令行）、`E5-Developer-Extend_*`（桌面版） |
| 备份文件 | `.e5backup`，头部 `E5RNWBAK` | 扩展名不变（不含产品名）；头部改为 `E5XTDBAK`。备份功能还没发布，**不需要兼容旧头部** |
| Entra 应用注册（自动创建功能，未发布） | `E5 Renew`，标签 `e5renew` | `E5 Developer Extend`，标签 `e5extend`；不识别旧名字（没有迁移，见 §4） |

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
   CHANGELOG 里 v0.1.0 一节保持原样（那时就叫 E5 Renew），新一节写明改名，并说明不做自动迁移。

## 4. v0.1.0 的安装怎么处理

**不做自动迁移，也没有兼容层。** v0.1.0 只公开了几个小时，没有已知用户。装过的人（目前只有所有者自己）按下面处理，然后全新安装 E5 Developer Extend：

1. 用旧版本运行 `e5renew service uninstall`，删除后台服务。
2. 卸载旧的包 / 应用（`e5renew`、`e5renew-desktop` 包；Windows 的 “E5 Renew”；macOS 的 `E5 Renew.app`），并关掉旧的开机启动项。
3. 安装新版本，重新添加账号（或用 v0.1.0 之前已有的方式重新配置）。

旧版本留下的配置和数据目录（`e5renew`）、钥匙串条目（服务名 `e5renew`）**保持原样，新版本不读也不删**；需要时由用户自己删除。
新版本只认 `E5EXTEND_*` 环境变量，不再读 `E5RENEW_*`。Docker 用户换新镜像名，并重新指定环境变量。

## 5. 测试

不需要升级测试。改名后跑一遍常规检查：gofmt、`go vet`、`go test`、前端类型检查和测试、发布脚本（包名、文件名）。

## 6. 和其他工作的顺序

1. `feat/backup` 合并（备份头部在本次改名里一起改）。
2. **改名**（本计划）。PR 合并前，所有者改 GitHub 仓库名；合并后发布 0.2.0 时推送新镜像名。
3. 设置页、自动创建应用注册、API 推荐、订阅到期检测、双语 README 和截图，全部直接用新名字。
4. 签名文档 PR #8 里的产品名在它合并前或改名 PR 里一起更新。

## 7. 所有者的决定（已定）

1. **中文显示名**：中英文都用 “E5 Developer Extend”（推荐，搜索和签名申请时名字唯一），还是 “E5 开发者续期”？
2. **命令行程序名**：`e5extend`（推荐，和包名、目录名一致），还是更短的名字？
3. **Linux 过渡脚本** `/usr/bin/e5renew`：不要（没有迁移，见 §4）。
4. **仓库改名时间**：建议在改名 PR 合并前改（Go 模块路径要和仓库地址一致）。
5. 商标检索（USPTO 等）由你来做；有问题的话在实现开始前告诉我。
