# 技术选型

目标：做一个在 **Windows、macOS、Linux** 上都能运行的 Microsoft 365 E5 开发者订阅续期工具。
提供 GUI 和 CLI 两种形态，能**长期在后台稳定运行**。

## 1. 功能范围

| 模块 | 内容 |
|---|---|
| 认证 | 设备码、浏览器交互（PKCE）、Client Secret（应用身份）、账号密码（ROPC，仅适用于没开 MFA 的账号，不推荐） |
| API 库 | 按分类勾选的 Graph 只读接口，按令牌里实际有的权限自动筛选，见 [graph-api-catalog.md](graph-api-catalog.md) |
| 调度 | 每账号独立循环；随机抽取、随机顺序、随机参数；工作日和工作时段窗口；运行期限 |
| 自动暂停 | 连续失败时自动暂停并记录原因；断网不算失败 |
| 多账号 | 每个账号单独的状态、日志、统计 |
| 其他 | 代理、开机自启、托盘、通知、自动更新、健康检查 |

## 2. 负载特征

这是一个 **I/O 密集、调用频率很低** 的程序：每个账号每隔几十分钟到几小时发十来个 HTTP 请求。
CPU 性能基本无关，真正有区别的是：

- **分发体积**（下载、安装是否方便）
- **常驻内存**（要在后台长期运行）
- **是否有官方 MSAL**（认证正确性、令牌缓存、各种登录方式）
- **跨平台构建难度**（三个平台，另外考虑 x64 和 arm64）

## 3. 方案对比

下表的体积和内存是选型时的经验估算；Go + Wails 一行后来实测过，实测结果见 [dev-setup.md](dev-setup.md)。

| 方案 | CLI 体积 | GUI 体积 | 常驻内存 | 官方 MSAL | 备注 |
|---|---|---|---|---|---|
| Python + PySide6 + PyInstaller | ~20–30 MB | ~80–150 MB（Qt） | CLI ~30–50 MB / GUI ~80–150 MB | 有（`msal`） | 开发最快；GUI 包大；macOS 签名和公证较麻烦 |
| **Go + Wails v3（系统 WebView）** | **实测 9.4 MB**（含认证、配置、钥匙串；只有 msal-go 时 4.8–5.0 MB） | **实测 11 MB** | **实测托盘 60 MB**，开窗约 320–350 MB | **有**（`msal-go`） | 单一静态二进制，交叉编译方便；Linux 依赖 WebKitGTK |
| Go + Fyne | 同上 | ~25–40 MB | ~50–80 MB | 有 | 纯 Go GUI，不依赖 WebView；外观不够原生 |
| Rust + Tauri | ~5–10 MB | ~8–15 MB | 与 Wails 相近 | **无**（需自己实现 OAuth） | 体积最小，但认证得自己写，开发量最大 |
| .NET 8 + Avalonia | ~30–70 MB（自包含 + 裁剪） | ~50–90 MB | ~60–120 MB | 有 | 体积和内存都偏大 |

## 4. 决定（2026-11-16）

- **Go + Wails v3**。Wails v3.0.0 已于 2026-10-26 发布正式版（当前 v3.1.0），托盘、开机自启、单实例、应用内更新都是内置功能。
  `msal-go` 是微软官方库，四种登录方式都支持。
- **前端用 React + TypeScript + Vite**（Wails 官方模板 `react`）。维护者熟悉 React；
  和 Svelte 相比只多几十 KB，相对 11 MB 的可执行文件和约 200 MB 的 WebKit 进程可以忽略。
  组件库倾向 shadcn/ui（Tailwind），写界面时再定。
- **常驻进程不碰 WebView，窗口单独开一个进程**：实测数据和原因见 [architecture.md](architecture.md) 第 2 节。
- 仓库公开，MIT 许可证。

## 5. 待定问题

- [x] GUI 框架：Wails v3
- [x] 前端框架：React + TypeScript
- [x] API 库只读部分：已对照官方文档核实（2026-11-16），部分条目待实测
- [x] 后台运行方式：见 [architecture.md](architecture.md) 第 9 节
- [ ] API 库写操作：默认关闭，是否提供待定，权限还没核实
- [ ] 凭据存储库：`99designs/keyring` 还是 `zalando/go-keyring`，以及 Windows 服务模式下的密钥方案
- [ ] Windows、macOS 上的 GUI 内存和关窗行为（要在对应平台或 CI 上实测）
- [ ] GUI 的显示名称（要和其他同类产品明显区分）
