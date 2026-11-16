# 开发环境

## Linux（以 Ubuntu / Debian 为例）

### 1. Go（装在用户目录，不需要 sudo）

```bash
curl -fsSLO https://go.dev/dl/go1.27.4.linux-amd64.tar.gz
echo "7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840  go1.27.4.linux-amd64.tar.gz" | sha256sum -c -
tar -C ~/.local -xzf go1.27.4.linux-amd64.tar.gz
echo 'export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"' >> ~/.bashrc
```

### 2. Wails CLI

```bash
CGO_ENABLED=0 go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0
wails3 doctor
```

要加 `CGO_ENABLED=0`：默认开 CGO 时，编译 CLI 本身也会去找 GTK4 的头文件，没装开发包就会失败。

`wails3 doctor` 的结论有可能写“系统已就绪”，但依赖表里同时列着缺的包。以依赖表为准。

### 3. GUI 系统依赖（需要 sudo）

```bash
sudo apt install libgtk-4-dev libwebkitgtk-6.0-dev
```

只开发 CLI 的话不需要装这一步。

### 4. 前端

需要 Node.js（LTS 即可）和 npm。推荐用 [fnm](https://github.com/Schniz/fnm) 安装：

```bash
fnm install --lts
```

Wails 生成的前端绑定（`gui/frontend/bindings/`）不进 git，构建前要先运行 `wails3 generate bindings`（`wails3 build` / `wails3 dev` 会自动执行）。

## macOS

```bash
xcode-select --install
brew install go node
CGO_ENABLED=0 go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0
```

## Windows

```powershell
winget install GoLang.Go OpenJS.NodeJS.LTS
$env:CGO_ENABLED=0; go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0
wails3 doctor
```

需要 WebView2 运行时（Windows 10/11 一般已预装）。CLI 可以在 Linux 上直接交叉编译成 Windows 版本。

## 已验证（2026-11-16）

- `msal-go v1.10.1`：`CGO_ENABLED=0`、`-ldflags="-s -w"` 编译出的测试程序 4.8 MB。
  交叉编译到 linux/amd64、linux/arm64、darwin/amd64、darwin/arm64、windows/amd64（5.0 MB）、windows/arm64（4.6 MB）均通过。
- GUI 示例（`wails3 init -t svelte`，Svelte + TS）：`wails3 build` 通过，可执行文件 **11 MB**（已含前端），
  链接 `libgtk-4`、`libwebkitgtk-6.0`。在 XFCE（X11）桌面上能正常打开窗口。
  - 编译时会有 `gdk_x11_*` 被弃用的警告，属于 Wails 的问题，不影响使用。
  - 运行时会打印 GTK 主题解析警告（`-gtk-icon-effect` 等），是 GTK3 主题被 GTK4 加载造成的，同样不影响使用。
- GUI 内存（单窗口测试：打开一个窗口，静置 10 秒，统计进程树的 PSS）：**约 350 MB**。
  托盘测试里开窗阶段测得约 320 MB，两次测试的程序和时长不同，所以数字有差别。
  其中主进程 120 MB、WebKitWebProcess 212 MB、NetworkProcess 17 MB。
  测试机没有 GPU，WebKit 走的是 CPU 渲染，数字偏高；有 GPU 的普通桌面应该会低一些（还没实测）。
- 托盘和窗口生命周期的内存测试（纯托盘 60 MB；GTK4 下关窗口不释放 WebKit 进程）：见 [architecture.md](architecture.md) 第 2 节。
  测试程序只开托盘，然后按“开窗 15 s → `Close()` → 等 20 s → 再开 15 s → `Close()`”的顺序运行，每 2.5 s 统计一次进程树 PSS。
- `wails3 init` 会在 `go mod tidy` 时访问 proxy.golang.org，网络超时会导致生成到一半中断（`build/` 目录不完整），重试即可。

## 发布构建（可选）

发布包用 `scripts/release/` 里的脚本构建，CI（`.github/workflows/release.yml`）跑的是同一套脚本，产物在 `dist/release/`：

```bash
CGO_ENABLED=0 go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.49.0
scripts/release/cli.sh            # 各平台命令行压缩包 + 命令行 deb/rpm（Linux 上）
scripts/release/linux-desktop.sh  # 本机架构的桌面 deb/rpm（Linux 上）
scripts/release/windows.sh        # Windows 安装包（需要 makensis；Linux 上装 nsis 包即可）
scripts/release/macos.sh          # dmg（只能在 macOS 上）
```

版本号默认是 `0.0.0-dev.<提交号>`，可以用 `VERSION=0.1.0` 覆盖。详见 [design/release.md](design/release.md)。
