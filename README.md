# e5-renew

跨平台的 Microsoft 365 E5 开发者订阅活跃工具：定时、随机地调用 Microsoft Graph 只读接口，
让开发者租户保持有开发活动。支持 **Windows、macOS、Linux**，提供 **GUI** 和 **CLI** 两种形态，
设计目标是**长期在后台稳定运行**。

> **状态：开发中**，即将发布第一版。设计文档见 [`docs/`](docs/)。

*A cross-platform (Windows / macOS / Linux) tool that keeps a Microsoft 365 E5 developer tenant active by
calling read-only Microsoft Graph APIs on a randomized schedule. GUI and CLI. Work in progress.*

## 计划功能

- **登录方式**：浏览器登录、设备码（适合服务器）、Client Secret（应用身份），以及账号密码登录（仅适用于没开 MFA 的账号，不推荐）
- **API 库**：按分类勾选的 Graph 只读接口，按令牌里实际有的权限自动筛选
- **调度**：随机抽取、随机顺序、随机参数；工作日和工作时段；断网、睡眠唤醒后自动恢复
- **多账号**：每个账号单独的状态、日志和统计；连续失败自动暂停（auto-pause）
- **长期运行**：开机自启、后台服务（systemd / launchd / Windows 服务 / Docker）、健康检查（`e5renew doctor`）、
  令牌和 Secret 到期提醒、桌面通知和 Webhook 告警
- **体积小**：CLI 约 10 MB，单个可执行文件，无运行时依赖

## 快速开始

从 [Releases](https://github.com/KKazuhaK/e5-renew/releases) 下载：Windows 安装包、macOS dmg、Linux deb/rpm（桌面版），
或者各平台的命令行压缩包和 Docker 镜像。选哪个文件、未签名的包怎么打开，见 [docs/user/install.md](docs/user/install.md)。

桌面版装好后从开始菜单 / 应用程序 / 应用菜单打开 **E5 Renew**；命令行版运行向导：

```bash
e5renew init                    # 交互式向导：配置、账号、登录、试跑、告警、后台服务、体检
```

或者分步：`config init` → `account add` → `account login` → `run-now` → `service install` → `doctor`。
详见 [docs/user/getting-started.md](docs/user/getting-started.md)。

## 文档

| 文档 | 内容 |
|---|---|
| [docs/user/install.md](docs/user/install.md) | 下载和安装（各平台、升级、卸载） |
| [docs/user/getting-started.md](docs/user/getting-started.md) | 五分钟上手 |
| [docs/user/app-registration.md](docs/user/app-registration.md) | Entra 应用注册（委托身份和应用身份） |
| [docs/user/background.md](docs/user/background.md) | 各平台后台运行：Linux、macOS、Windows、Docker |
| [docs/user/docker.md](docs/user/docker.md) | Docker 和 compose |
| [docs/user/faq.md](docs/user/faq.md) | 常见问题 |
| [docs/user/troubleshooting.md](docs/user/troubleshooting.md) | 按 `doctor` 检查项排错 |
| [docs/research.md](docs/research.md) | 技术选型与决定 |
| [docs/architecture.md](docs/architecture.md) | 架构、模块、长期后台运行设计 |
| [docs/graph-api-catalog.md](docs/graph-api-catalog.md) | Graph API 库与权限（只读部分已对照官方文档核实，部分条目待实测） |
| [docs/dev-setup.md](docs/dev-setup.md) | 开发环境搭建 |

## 构建（CLI）

需要 Go 1.26 或更新版本。

```bash
go build -o bin/e5renew ./cmd/e5renew
./bin/e5renew version
```

## 说明

- 微软没有公开开发者订阅续期的判定标准。本工具**不保证**订阅一定能续期。
- 电脑关机或睡眠期间不会发出调用。需要长期使用的话，建议部署在常开的机器或服务器上。
- 本项目与 Microsoft 没有关联。Microsoft 365、Microsoft Graph、Entra 是 Microsoft 的商标。

## 许可证

[MIT](LICENSE)
