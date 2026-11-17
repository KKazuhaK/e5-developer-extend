package update

import (
	"context"
	"fmt"
	"strings"
)

// Kind is how this copy of the program was installed, which decides what an
// upgrade has to do (docs/design/update.md, section 4).
type Kind string

// Kinds.
const (
	KindDocker          Kind = "docker"
	KindWindowsSetup    Kind = "windows-installer" // the NSIS installer: per-user, with uninstall.exe next to the programs
	KindWindowsCLI      Kind = "windows-cli"       // the zip of the command line
	KindMacApp          Kind = "macos-app"         // E5 Developer Extend.app, from the dmg
	KindMacCLI          Kind = "macos-cli"         // the tar.gz of the command line
	KindLinuxDeb        Kind = "linux-deb"         // owned by dpkg
	KindLinuxRPM        Kind = "linux-rpm"         // owned by rpm
	KindLinuxStandalone Kind = "linux-standalone"  // the tar.gz, or built from source
	KindUnknown         Kind = "unknown"
)

// Env is everything Detect looks at, so tests can describe a machine.
type Env struct {
	GOOS, GOARCH string
	// Exe is the path of the running program, symbolic links resolved.
	Exe    string
	Getenv func(string) string
	// Exists reports whether a file or folder exists.
	Exists func(path string) bool
	// Run runs a command and returns its output; an error means a non-zero exit or no such command.
	Run func(ctx context.Context, name string, args ...string) (string, error)
}

// Installation is what Detect found.
type Installation struct {
	Kind Kind
	// Exe is the running program; Dir is the folder it is in.
	Exe, Dir string
	// AppPath is the .app bundle (KindMacApp).
	AppPath string
	// Desktop is true when the desktop app is installed next to the command
	// line (Linux packages: both packages are upgraded together).
	Desktop bool
}

// pathDir and pathJoin treat both separators as separators, so a Windows path
// can be examined on any system (tests do).
func pathDir(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[:i]
	}
	return "."
}

func pathBase(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func pathJoin(dir, name string) string {
	sep := "/"
	if strings.Contains(dir, `\`) {
		sep = `\`
	}
	return strings.TrimRight(dir, `\/`) + sep + name
}

// Detect works out how the running program was installed.
func Detect(ctx context.Context, env Env) Installation {
	in := Installation{Kind: KindUnknown, Exe: env.Exe, Dir: pathDir(env.Exe)}
	exists := func(p string) bool { return env.Exists != nil && env.Exists(p) }
	getenv := func(k string) string {
		if env.Getenv == nil {
			return ""
		}
		return env.Getenv(k)
	}
	if env.Exe == "" {
		return in
	}
	switch env.GOOS {
	case "windows":
		prog := strings.ToLower(getenv("LOCALAPPDATA")) + `\programs\e5 developer extend`
		switch {
		case exists(pathJoin(in.Dir, "uninstall.exe")),
			getenv("LOCALAPPDATA") != "" && strings.EqualFold(strings.TrimRight(in.Dir, `\/`), strings.ReplaceAll(prog, "/", `\`)):
			in.Kind = KindWindowsSetup
		default:
			in.Kind = KindWindowsCLI
		}
	case "darwin":
		if i := strings.Index(env.Exe, ".app/Contents/MacOS/"); i >= 0 {
			in.Kind, in.AppPath = KindMacApp, env.Exe[:i+len(".app")]
		} else {
			in.Kind = KindMacCLI
		}
	case "linux":
		switch {
		case exists("/.dockerenv") || getenv("E5EXTEND_DOCKER") != "":
			in.Kind = KindDocker
		case ownedBy(ctx, env, "dpkg", "-S", env.Exe):
			in.Kind = KindLinuxDeb
		case ownedBy(ctx, env, "rpm", "-qf", env.Exe):
			in.Kind = KindLinuxRPM
		default:
			in.Kind = KindLinuxStandalone
		}
		in.Desktop = (in.Kind == KindLinuxDeb || in.Kind == KindLinuxRPM) && exists(pathJoin(in.Dir, "e5extend-gui"))
	}
	return in
}

// ownedBy reports whether a package manager claims the file.
func ownedBy(ctx context.Context, env Env, name string, args ...string) bool {
	if env.Run == nil {
		return false
	}
	out, err := env.Run(ctx, name, args...)
	return err == nil && strings.TrimSpace(out) != ""
}

// archNames maps GOARCH to the architecture names of the package files.
func archNames(goarch string) (deb, rpm string, ok bool) {
	switch goarch {
	case "amd64":
		return "amd64", "x86_64", true
	case "arm64":
		return "arm64", "aarch64", true
	}
	return "", "", false
}

// Assets lists the release files an upgrade of this installation needs, apart
// from SHA256SUMS and its signature. version is without the "v". The names are
// the ones docs/design/release.md lists; none is built for a Docker image.
func Assets(in Installation, goos, goarch, version string) ([]string, error) {
	deb, rpm, archOK := archNames(goarch)
	switch in.Kind {
	case KindWindowsSetup:
		if !archOK {
			break
		}
		return []string{fmt.Sprintf("E5-Developer-Extend_%s_windows_%s_setup.exe", version, goarch)}, nil
	case KindWindowsCLI:
		if !archOK {
			break
		}
		return []string{fmt.Sprintf("e5extend_%s_windows_%s.zip", version, goarch)}, nil
	case KindMacApp:
		return []string{fmt.Sprintf("E5-Developer-Extend_%s_macos_universal.dmg", version)}, nil
	case KindMacCLI:
		return []string{fmt.Sprintf("e5extend_%s_darwin_universal.tar.gz", version)}, nil
	case KindLinuxStandalone:
		if !archOK {
			break
		}
		return []string{fmt.Sprintf("e5extend_%s_linux_%s.tar.gz", version, goarch)}, nil
	case KindLinuxDeb:
		if !archOK {
			break
		}
		a := []string{fmt.Sprintf("e5extend_%s-1_%s.deb", version, deb)}
		if in.Desktop {
			a = append(a, fmt.Sprintf("e5extend-desktop_%s-1_%s.deb", version, deb))
		}
		return a, nil
	case KindLinuxRPM:
		if !archOK {
			break
		}
		a := []string{fmt.Sprintf("e5extend-%s-1.%s.rpm", version, rpm)}
		if in.Desktop {
			a = append(a, fmt.Sprintf("e5extend-desktop-%s-1.%s.rpm", version, rpm))
		}
		return a, nil
	case KindDocker:
		return nil, fmt.Errorf("a Docker image is upgraded by pulling a new image")
	}
	return nil, fmt.Errorf("no release file for %s on %s/%s", in.Kind, goos, goarch)
}

// SumsName and SigName are the checksum list of a release and its signature.
const (
	SumsName = "SHA256SUMS"
	SigName  = "SHA256SUMS.minisig"
)
