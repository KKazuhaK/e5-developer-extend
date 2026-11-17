# 变更记录

格式参照 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## 未发布

### 新增

- 备份与迁移：`e5renew backup export` / `backup import` 把配置、secret、登录状态、告警地址和运行状态导出成一个用备份密码加密的文件，在另一台机器（可以是另一个系统）上导入，不用重新登录、不用重新输入 secret；支持合并和替换。桌面版设置页新增“备份与迁移”。详见 [docs/user/backup.md](docs/user/backup.md)。

### 变更

- 桌面版窗口左上角改为应用图标。

## [0.1.0] - 2026-11-16

第一个版本。

### 新增

- 命令行程序 `e5renew`：配置、加密密钥库、四种登录方式（设备码、浏览器、Client Secret、账号密码）。
- Graph 只读 API 库和调度引擎：随机抽取和顺序、工作日与工作时段、睡眠唤醒和时钟跳变处理、连续失败自动暂停。
- 后台服务：systemd 用户服务、macOS LaunchAgent、Windows 计划任务；`e5renew doctor` 健康检查；Webhook 告警；`e5renew init` 向导。
- 桌面版 E5 Renew：托盘图标和主窗口（React），中英文界面。
- 发布包：Windows 安装包、macOS dmg、Linux deb/rpm、命令行压缩包、Docker 镜像（暂未代码签名）。

[0.1.0]: https://github.com/KKazuhaK/e5-renew/releases/tag/v0.1.0
