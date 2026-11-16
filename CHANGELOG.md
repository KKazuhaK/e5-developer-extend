# 变更记录

格式参照 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## [0.1.0] - 2026-11-16

第一个版本。

### 新增

- 命令行程序 `e5renew`：配置、加密密钥库、四种登录方式（设备码、浏览器、Client Secret、账号密码）。
- Graph 只读 API 库和调度引擎：随机抽取和顺序、工作日与工作时段、睡眠唤醒和时钟跳变处理、连续失败自动暂停。
- 后台服务：systemd 用户服务、macOS LaunchAgent、Windows 计划任务；`e5renew doctor` 健康检查；Webhook 告警；`e5renew init` 向导。
- 桌面版 E5 Renew：托盘图标和主窗口（React），中英文界面。
- 发布包：Windows 安装包、macOS dmg、Linux deb/rpm、命令行压缩包、Docker 镜像（暂未代码签名）。

[0.1.0]: https://github.com/KKazuhaK/e5-renew/releases/tag/v0.1.0
