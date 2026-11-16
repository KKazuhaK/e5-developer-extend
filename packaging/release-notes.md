<!-- Filled in by .github/workflows/release.yml (@VERSION@). Edit the draft before publishing. -->

## 下载 / Downloads

| 系统 / OS | 桌面版 / Desktop | 命令行 / CLI |
|---|---|---|
| Windows | `E5-Renew_@VERSION@_windows_amd64_setup.exe` (`arm64`) | `e5renew_@VERSION@_windows_amd64.zip` (`arm64`) |
| macOS 12+ | `E5-Renew_@VERSION@_macos_universal.dmg` | `e5renew_@VERSION@_darwin_universal.tar.gz` |
| Ubuntu 24.04+ / Debian 13+ | `e5renew_…deb` + `e5renew-desktop_…deb` | `e5renew_…deb` |
| Fedora 39+ | `e5renew-…rpm` + `e5renew-desktop-…rpm` | `e5renew-…rpm` |
| Linux (any) | — | `e5renew_@VERSION@_linux_amd64.tar.gz` (`arm64`) |
| Docker | — | `ghcr.io/kkazuhak/e5-renew:@VERSION@` |

安装说明（含未签名包的打开方法）：[docs/user/install.md](https://github.com/KKazuhaK/e5-renew/blob/v@VERSION@/docs/user/install.md)
Install guide (including how to open the unsigned packages): same link.

> 这些包**没有代码签名**。macOS 需要在“隐私与安全性”里点“仍要打开”；Windows SmartScreen 需要点“更多信息 → 仍要运行”。
> The packages are **not code-signed** yet.

校验 / Verify: `sha256sum --ignore-missing -c SHA256SUMS`

## 变更 / Changes

见 [CHANGELOG.md](https://github.com/KKazuhaK/e5-renew/blob/v@VERSION@/CHANGELOG.md)。

不保证订阅一定续期；与 Microsoft 无关。 / Renewal is not guaranteed; not affiliated with Microsoft.
