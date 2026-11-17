# 变更记录

格式参照 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## 未发布

### 新增

- 自动创建应用注册：`e5extend app setup`、`e5extend account add --auto`，以及 `e5extend init` 里的“自动创建（推荐）”。用租户管理员账号登录一次（通过微软官方的 Microsoft Graph Command Line Tools，令牌只放在内存里），程序在租户里建好应用、加好只读权限并授予管理员同意；可重复执行，只补缺的，不删除任何东西；可以采用手工建的同名应用。应用身份还会创建一个以本机命名的 Client Secret，直接存进 vault。详见 [docs/user/app-registration.md](docs/user/app-registration.md) 和 [docs/design/app-setup.md](docs/design/app-setup.md)。
- 备份与迁移：`e5extend backup export` / `backup import` 把配置、secret、登录状态、告警地址和运行状态导出成一个用备份密码加密的文件，在另一台机器（可以是另一个系统）上导入，不用重新登录、不用重新输入 secret；支持合并和替换。桌面版设置页新增“备份与迁移”。详见 [docs/user/backup.md](docs/user/backup.md)。

- API 推荐：登录之后探测账号实际能用哪些 API 分类（只读请求：邮箱设置、OneDrive、Teams、SharePoint 根站点），给每个分类标上推荐 / 不可用（带原因）/ 可能可用。命令行：`e5extend account test <名称> --recommend`，加 `--apply` 把推荐的分类写进账号；桌面版添加账号向导的“API 分类”移到登录之后，默认勾选推荐的。`e5extend doctor` 新增 `account.<名称>.apis`：某个分类连续 3 轮都是 404 时提醒。详见 [docs/design/api-recommend.md](docs/design/api-recommend.md)。

### 变更

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

[0.1.0]: https://github.com/KKazuhaK/e5-developer-extend/releases/tag/v0.1.0
