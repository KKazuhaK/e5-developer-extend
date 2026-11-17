<!-- Filled in by .github/workflows/release.yml (@VERSION@). Edit the draft before publishing. -->

## 下载 / Downloads

点链接直接下载。不知道选哪个：Windows 和 Mac 选**桌面版**，服务器选**命令行**。
Click a link to download. Not sure? Desktop for Windows and Mac, CLI for servers.

| 系统 / OS | 桌面版 / Desktop | 命令行 / CLI |
|---|---|---|
| Windows 10/11 | [x64 安装包](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/E5-Developer-Extend_@VERSION@_windows_amd64_setup.exe) · [ARM64](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/E5-Developer-Extend_@VERSION@_windows_arm64_setup.exe) | [x64 zip](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend_@VERSION@_windows_amd64.zip) · [ARM64](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend_@VERSION@_windows_arm64.zip) |
| macOS 12+ | [dmg（Intel 和 Apple 芯片通用）](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/E5-Developer-Extend_@VERSION@_macos_universal.dmg) | [tar.gz（通用）](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend_@VERSION@_darwin_universal.tar.gz) |
| Ubuntu 24.04+ / Debian 13+ | 两个都装 / install both: [e5extend](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend_@VERSION@-1_amd64.deb) + [e5extend-desktop](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend-desktop_@VERSION@-1_amd64.deb)（arm64: [1](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend_@VERSION@-1_arm64.deb) + [2](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend-desktop_@VERSION@-1_arm64.deb)） | [x64 deb](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend_@VERSION@-1_amd64.deb) · [ARM64](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend_@VERSION@-1_arm64.deb) |
| Fedora 39+ | 两个都装 / install both: [e5extend](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend-@VERSION@-1.x86_64.rpm) + [e5extend-desktop](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend-desktop-@VERSION@-1.x86_64.rpm)（aarch64: [1](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend-@VERSION@-1.aarch64.rpm) + [2](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend-desktop-@VERSION@-1.aarch64.rpm)） | [x64 rpm](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend-@VERSION@-1.x86_64.rpm) · [ARM64](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend-@VERSION@-1.aarch64.rpm) |
| Linux（任意发行版 / any） | — | [x64 tar.gz](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend_@VERSION@_linux_amd64.tar.gz) · [ARM64](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/e5extend_@VERSION@_linux_arm64.tar.gz) |
| Docker | — | `docker pull ghcr.io/kkazuhak/e5-developer-extend:@VERSION@` |

校验和 / Checksums: [SHA256SUMS](https://github.com/KKazuhaK/e5-developer-extend/releases/download/v@VERSION@/SHA256SUMS)

安装说明（含未签名包的打开方法）：[docs/user/install.md](https://github.com/KKazuhaK/e5-developer-extend/blob/v@VERSION@/docs/user/install.md)
Install guide (including how to open the unsigned packages): same link.

> 这些包**没有代码签名**。macOS 需要在“隐私与安全性”里点“仍要打开”；Windows SmartScreen 需要点“更多信息 → 仍要运行”。
> The packages are **not code-signed** yet.

校验 / Verify: `sha256sum --ignore-missing -c SHA256SUMS`（下载到同一个文件夹后运行 / run in the download folder）

## 变更 / Changes

见 [CHANGELOG.md](https://github.com/KKazuhaK/e5-developer-extend/blob/v@VERSION@/CHANGELOG.md)。

不保证订阅一定续期；与 Microsoft 无关。 / Renewal is not guaranteed; not affiliated with Microsoft.
