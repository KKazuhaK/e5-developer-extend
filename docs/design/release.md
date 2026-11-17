# 打包与发布

本文定下第一版（v0.1.0）的发布物、构建流程和安装后的长期运行细节。
已经定下的前提：

- 产品显示名 **E5 Renew**；命令行程序名不变，仍是 `e5renew`。
- 第一版**不做代码签名**。流水线里只预留签名的位置，文档写清楚未签名包怎么打开。
- 发布只在推送 `v*` 标签时进行，生成的是**草稿（draft）Release**，由仓库所有者检查后手动发布。

## 1. 发布物

每个版本的 GitHub Release 里有下面这些文件（`<v>` 是不带 `v` 的版本号）：

| 文件 | 内容 | 适用 |
|---|---|---|
| `e5renew_<v>_linux_{amd64,arm64}.tar.gz` | `e5renew` | Linux 服务器、任何发行版 |
| `e5renew_<v>_darwin_universal.tar.gz` | `e5renew`（通用二进制） | macOS 命令行 |
| `e5renew_<v>_windows_{amd64,arm64}.zip` | `e5renew.exe`、`e5renew-bg.exe` | Windows 命令行 |
| `e5renew_<v>-1_{amd64,arm64}.deb` / `e5renew-<v>-1.{x86_64,aarch64}.rpm` | 命令行，装到 `/usr/bin/e5renew`，没有图形库依赖 | Debian/Ubuntu、Fedora/RHEL 服务器 |
| `e5renew-desktop_<v>-1_{amd64,arm64}.deb` / `e5renew-desktop-<v>-1.{x86_64,aarch64}.rpm` | 桌面版 `e5renew-gui`，依赖上一行的 `e5renew` 包和 GTK4、WebKitGTK 6.0 | Ubuntu 24.04+、Debian 13+、Fedora 39+ |
| `E5-Renew_<v>_macos_universal.dmg` | `E5 Renew.app`（内含 `e5renew-gui` 和 `e5renew`） | macOS 12+ |
| `E5-Renew_<v>_windows_{amd64,arm64}_setup.exe` | NSIS 安装包，装 `e5renew-gui.exe`、`e5renew.exe`、`e5renew-bg.exe` | Windows 10/11 |
| `SHA256SUMS` | 以上所有文件的校验和 | |

Docker 镜像 `ghcr.io/kkazuhak/e5-renew:<v>`、`:<主>.<次>` 和 `:latest`（linux/amd64、linux/arm64）。
镜像在草稿 Release **被发布之后**才推送（`.github/workflows/docker.yml`），预发布版本不更新 `:latest` 和 `:<主>.<次>`；
这样构建失败或被否决的标签不会通过 `:latest` 到达自动拉取镜像的服务器。

### 为什么桌面版必须带上命令行程序

后台服务运行的是命令行程序（`e5renew run`），不是桌面程序（`gui/shared.go` 的 `cliPath`）。
桌面程序在**自己所在的目录**里找 `e5renew`，所以每种桌面包都把两者放在同一目录：
Linux 是 `/usr/bin`，macOS 是 `E5 Renew.app/Contents/MacOS/`，Windows 是安装目录。

## 2. 与架构文档（§7）不同的地方

- **不用 GoReleaser**。桌面版必须在三种系统的原生 runner 上分别构建（Linux 要 CGO 和 GTK，macOS 要 CGO），
  命令行版和桌面版又要进同一个 Release、同一份 `SHA256SUMS`。用 GoReleaser 只能管命令行那一半，
  另一半还得自己拼。所以改成：每个平台一个构建任务，产物作为 artifact 上传，最后一个任务汇总、算校验和、建草稿 Release。
  构建逻辑写在 `scripts/release/*.sh` 里，本地和 CI 跑的是同一份。
- **第一版不做 AppImage**。AppImage 运行时把程序挂在 `/tmp/.mount_*` 下，每次路径都不同：
  - 后台服务注册的是这个临时路径，`service install` 会拒绝（`CheckExecutable`），拒绝不了的话重启后也会失效；
  - Wails 的开机启动用 `os.Executable()` 写 `.desktop`，同样指向临时路径。
  这两点都和“长期后台运行”冲突。Linux 桌面用户用 deb/rpm；其他发行版用命令行 tar 包加 `systemd --user` 服务。
- **Homebrew tap、Scoop、winget 暂不做**。它们都需要另建仓库或给外部仓库提 PR，放到第一版发布之后。
- **Windows 加 arm64**：Windows 上的 Wails 不需要 CGO，在任何机器上都能交叉编译，成本只是多一个包。

## 3. 各平台细节

### 3.1 版本号

- 标签 `v0.1.0` → 版本 `0.1.0`。非标签构建（PR 上的试构建）用 `0.0.0-dev.g<短提交号>`（加 `g` 是因为全数字且以 0 开头的提交号不是合法的 semver，nfpm 会因此换一种版本写法，桌面包的精确依赖就对不上了）。
- Go 程序通过 `-ldflags -X internal/version.{Version,Commit,Date}` 注入；前端通过 `VITE_APP_VERSION` 注入。
- 所有发布构建都带 `-trimpath -ldflags "-s -w"`，并且带 `-tags frontend`（否则桌面版只有占位页）。

### 3.2 图标

`packaging/icons/appicon.png`（1024×1024）和 Linux 用的 `appicon-256.png` 由 `go run ./scripts/genicon [-size 256]` 生成
（只用标准库，按尺寸直接绘制而不是缩放）；`.ico` 和 `.icns` 用
`wails3 generate icons -input packaging/icons/appicon.png -windowsfilename packaging/icons/icon.ico -macfilename packaging/icons/icons.icns` 生成。
这些文件都提交进仓库，CI 不再重新生成。

预发布版本（如 `0.2.0-rc.1`）在 deb/rpm 里写成 `0.2.0~rc.1`，排序才会低于正式版；桌面包对命令行包的精确版本依赖用同样的写法（`PKG_VERSION`）。
Windows 文件版本只能是数字，用去掉预发布部分的 `NUM_VERSION`。

### 3.3 Linux（deb、rpm）

- 用 nfpm 配置（`packaging/linux/nfpm-cli.yaml`、`nfpm-desktop.yaml`），用独立的 nfpm（固定 v2.49.0）打包。`wails3 tool package` 也内置 nfpm，但输出文件名固定为 `<名字>.<格式>`，不带版本和架构。
- 预发布版本的包文件名里的 `~` 换成 `-`（`plain_names`），因为 GitHub 会改写带特殊字符的附件名，`SHA256SUMS` 会对不上；包内的版本号仍然带 `~`。
- 桌面包精确依赖同版本的命令行包，所以升级时两个包要一起装（`apt install` 只给新的命令行包会让 apt 删掉桌面包）。
- 桌面包：`/usr/bin/e5renew-gui`、`/usr/share/applications/io.github.kkazuhak.e5renew.desktop`、
  `/usr/share/icons/hicolor/256x256/apps/io.github.kkazuhak.e5renew.png`。
- 桌面版在 `ubuntu-24.04` / `ubuntu-24.04-arm` 上构建，最低要求是 glibc 2.39，正好和 WebKitGTK 6.0 的可用范围一致。
- **长期运行相关**：
  - 安装包脚本**不碰**用户的 `systemd --user` 服务（包脚本以 root 运行，管不到每个用户的服务）。
  - 升级后，已经在跑的服务继续用旧的进程，直到重启；文档写明升级后运行 `systemctl --user restart e5renew`。
  - 卸载前要先 `e5renew service uninstall`，否则服务会因为找不到程序反复失败。文档写明；卸载脚本只打印一行提醒。

### 3.4 macOS（.app、dmg）

- 在 `macos-latest`（arm64）上分别编译 arm64 和 amd64（CGO 用 clang 的 `-arch`），再用 `lipo` 合成通用二进制。
- `.app` 结构：`Contents/MacOS/e5renew-gui`（`CFBundleExecutable`）、`Contents/MacOS/e5renew`、`Contents/Resources/icons.icns`、`Contents/Info.plist`。
- `Info.plist` 设 `LSUIElement = true`：默认启动的是托盘进程，本来就不该在 Dock 里出现图标；
  窗口进程用 Wails 默认的 `Regular` 激活策略，Wails 启动时会显式设置它（不受 `LSUIElement` 影响），所以打开窗口时仍然有 Dock 图标和菜单栏。
- 没有签名证书，就做 **ad-hoc 签名**（`codesign --force -s -`，先签包里的 `e5renew`，再签整个 `.app`；不用已不推荐的 `--deep`）。Apple Silicon 不允许运行完全没签名的程序，
  ad-hoc 签名能让程序跑起来，但 Gatekeeper 仍然会拦第一次打开。
- dmg 用 `hdiutil create` 生成，里面是 `.app` 和一个指向 `/Applications` 的链接。
- **长期运行相关**：
  - 从 dmg 直接运行，或在“下载”文件夹里带着隔离属性运行时，macOS 会把程序挪到一个随机的只读路径（App Translocation，
    在 `/private/var/folders` 下）。`service install` 已经会拒绝这种路径；提示里改成说明“先把 E5 Renew 拖进 Applications”。
  - LaunchAgent 写入的是 `/Applications/E5 Renew.app/Contents/MacOS/e5renew` 这个固定路径；升级时直接替换 `.app`，
    launchd 下次重启进程就用新版本。

### 3.5 Windows（NSIS）

- 按**当前用户**安装到 `%LOCALAPPDATA%\Programs\E5 Renew\`，不需要管理员权限。后台任务本来就是按用户注册的，两者一致。
- 可执行文件带资源（`wails3 generate syso`）：图标、版本信息、清单（DPI 感知、Common Controls v6）。
  `e5renew-bg.exe` 和 `e5renew-gui.exe` 用 `-H=windowsgui`，`e5renew.exe` 保持控制台程序。
- 开始菜单快捷方式 “E5 Renew”（启动托盘）。不建桌面快捷方式（这是后台工具）。
- 缺少 WebView2 运行时时，安装包下载微软的引导程序安装（Windows 11 自带，Windows 10 绝大多数已有）。
- **长期运行相关**：
  - 安装/升级前：结束正在运行的 `e5renew-gui.exe`；如果后台任务 `e5renew` 存在，先 `schtasks /End`，
    复制完文件后再 `schtasks /Run`。否则文件被占用，升级会失败。
  - 安装/升级时先 `schtasks /Change /DISABLE` 再 `/End`：任务每 5 分钟触发一次、失败后自动重启，不先禁用的话可能在复制文件时被重新拉起；
    复制完再 `/ENABLE`、`/Run`；安装失败或取消时（`.onInstFailed`）也会重新启用。
  - 卸载时：先运行 `e5renew.exe service uninstall`（删掉后台任务），再删除程序文件和开机启动项。
    **用户的配置、密钥、日志保留**（`%APPDATA%\e5renew`、`%LOCALAPPDATA%\e5renew`），卸载完成页说明它们的位置。
  - SmartScreen 会提示“Windows 已保护你的电脑”，点“更多信息”→“仍要运行”。

### 3.6 Docker

- `Dockerfile` 的构建阶段改用 `--platform=$BUILDPLATFORM`，用 `GOOS/GOARCH=$TARGETOS/$TARGETARCH` 交叉编译，
  不在 QEMU 里跑 arm64 的 Go 编译器。
- 标签构建推送到 GHCR；PR 构建只构建 amd64，不推送。

## 4. 代码改动

- 托盘的开机启动项改用独立的标识 `io.github.kkazuhak.e5renew.tray`（`gui/shared.go` 的 `autostartID`）。
  默认标识在 macOS 上是包的 Bundle ID，和后台服务的 launchd 标签 `io.github.kkazuhak.e5renew` 相同：
  在 macOS 12（Wails 用 LaunchAgent 实现开机启动）上，打开“开机启动”会覆盖后台服务的 plist。
  这个标识在 Windows 上也是 Run 键里的值名，卸载程序按它删除。
- 打开“开机启动”前用 `CheckExecutable` 检查程序路径，拒绝 dmg 里或 App Translocation 的临时路径。

- `gui/main.go`、`gui/frontend/src/config.ts`：`AppName = "E5 Renew"`。
- `service.CheckExecutable`：路径在 macOS App Translocation 下时，提示改成“把 E5 Renew 移到 Applications 再安装”。

## 5. 工作流（`.github/workflows/release.yml`）

触发：推送 `v*` 标签；或者 PR 改动了 `packaging/**`、`scripts/release/**`、`release.yml`（只构建、不发布，产物保留 7 天）。

| 任务 | runner | 产出 |
|---|---|---|
| `cli` | ubuntu-latest | 命令行的 5 个压缩包（darwin 两个架构用 `wails3 tool lipo` 合并）、命令行 deb/rpm |
| `linux-desktop` | ubuntu-24.04、ubuntu-24.04-arm | 桌面 deb/rpm |
| `macos` | macos-latest | dmg |
| `windows` | windows-latest | 两个安装包 |
| `docker` | ubuntu-latest | 只构建镜像（标签时两个架构），不推送 |
| `publish` | ubuntu-latest | 汇总、`SHA256SUMS`、**草稿** Release（只在标签时） |

每个打包任务在上传前做**冒烟测试**：

- Linux：在 runner 上 `apt install` 两个 deb，运行 `e5renew version`，`ldd /usr/bin/e5renew-gui` 不能有 `not found`。
- macOS：挂载 dmg，`codesign --verify --deep --strict`，`plutil -lint`，运行包内的 `e5renew version`，`lipo -archs` 必须同时有两个架构。
- Windows：静默安装（`/S`），检查三个文件，运行 `e5renew.exe version`，静默卸载，检查目录已删除。

权限：默认 `contents: read`；`publish` 任务 `contents: write`；推送镜像的 `docker.yml` 是 `packages: write`。
第三方 Action 只用 GitHub 官方的和 Docker 官方的。

## 6. 发布步骤（给仓库所有者）

1. 确认 main 上 CI 全绿，更新 `CHANGELOG.md`。
2. `git tag v0.1.0 && git push origin v0.1.0`。
3. 等 Release 工作流完成，在 GitHub 上检查草稿 Release 里的文件和说明，点 Publish。
4. 发布会触发 `docker.yml` 推送镜像。（必须由人在网页上或用自己的令牌发布：用工作流自带的 `GITHUB_TOKEN` 发布不会触发其他工作流。）

## 7. 以后再做

- 代码签名：macOS（Developer ID + 公证）、Windows（SignPath Foundation 或 Azure Artifact Signing）。方案比较和建议见 [signing.md](signing.md)。
- Homebrew tap、Scoop bucket、winget 清单。
- 应用内更新（Wails 自带，基于 GitHub Releases），前提是先有签名。

## 8. 验证记录（2026-11-16）

- 命令行：`scripts/release/cli.sh` 在开发机上 1 分钟产出 9 个文件；Windows 程序带图标、版本信息和清单，`e5renew-bg.exe` 是 GUI 子系统。
- Linux 桌面：`linux-desktop.sh` 产出 deb/rpm；在 `ubuntu:24.04` 容器里同时装两个 deb 成功，`ldd` 没有缺失库，
  卸载 `e5renew` 会连带卸载依赖它的 `e5renew-desktop`，并打印“先 `service uninstall`”的提醒。
- Windows（`makensis` 3.11 在 Linux 上构建；在 Windows 11 IoT LTSC 2024 虚拟机里测试）：
  - 静默安装：三个程序、开始菜单快捷方式、“应用”里的卸载项和版本号都正确；
  - 升级：后台任务正在运行时装新版本，旧进程被结束、文件被替换、任务用新版本重新启动；托盘正在运行时，静默升级后托盘也被重新启动；
  - 卸载：后台任务、程序目录、快捷方式、卸载项都被删除，`%APPDATA%\e5renew`、`%LOCALAPPDATA%\e5renew` 保留。
  - 注意：在 PowerShell 里用 `Start-Process -Wait` 静默升级会一直等待，因为它也会等安装包重新启动的托盘进程；这是测试方式的问题，不影响安装。
- macOS：只能在 CI 的 macOS runner 上构建和检查（第 5 节的冒烟测试），还没有在真机上试过 Gatekeeper 的放行流程。
