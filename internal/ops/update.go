package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gofrs/flock"

	"github.com/KKazuhaK/e5-developer-extend/internal/auth"
	"github.com/KKazuhaK/e5-developer-extend/internal/config"
	"github.com/KKazuhaK/e5-developer-extend/internal/engine"
	"github.com/KKazuhaK/e5-developer-extend/internal/paths"
	"github.com/KKazuhaK/e5-developer-extend/internal/service"
	"github.com/KKazuhaK/e5-developer-extend/internal/update"
	"github.com/KKazuhaK/e5-developer-extend/internal/version"
)

// UpdateHooks replace the outside world of the update operations; tests only.
// The zero value means the real thing.
type UpdateHooks struct {
	// APIBase replaces the GitHub API address.
	APIBase string
	// PublicKey replaces the compiled-in release signing key.
	PublicKey string
	// AllowDownload replaces the rule for where files may be downloaded from.
	AllowDownload func(url string) bool
	// Version replaces version.Version.
	Version string
	// GOOS and GOARCH replace the platform of the asset choice.
	GOOS, GOARCH string
	// Env replaces the description of this machine used to find out how the
	// program was installed.
	Env func() update.Env
	// Run replaces running commands (hdiutil, apt-get, ...).
	Run func(ctx context.Context, name string, args ...string) (string, error)
	// StartDetached replaces starting the Windows installer.
	StartDetached func(name string, args ...string) error
	// TempRoot replaces the folder downloads are put in.
	TempRoot string
	// Geteuid and LookPath replace the checks for administrator rights and for pkexec.
	Geteuid  func() int
	LookPath func(string) (string, error)
}

// ReleasesURL is the page of the releases, for when no specific release is known.
const ReleasesURL = "https://github.com/" + update.Repo + "/releases/latest"

func (o *Ops) updateCurrent() string { return FirstNonEmpty(o.Update.Version, version.Version) }

func (o *Ops) updateService(cfg *config.Config, dataDir string) *update.Service {
	client := auth.NewHTTPClient(cfg.Network.Proxy)
	client.Timeout = 20 * time.Second
	cur := o.updateCurrent()
	return &update.Service{
		DataDir: dataDir, Current: cur, Channel: cfg.Update.Channel,
		Checker: &update.Checker{HTTP: client, Base: o.Update.APIBase, Version: cur},
	}
}

func (o *Ops) updatePublicKey() string {
	return FirstNonEmpty(o.Update.PublicKey, update.PublicKeyText)
}

func execRun(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

func (o *Ops) updateRun() func(context.Context, string, ...string) (string, error) {
	if o.Update.Run != nil {
		return o.Update.Run
	}
	return execRun
}

// updateEnv describes this machine for update.Detect.
func (o *Ops) updateEnv() update.Env {
	if o.Update.Env != nil {
		return o.Update.Env()
	}
	exe, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	run := o.updateRun()
	return update.Env{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Exe: exe, Getenv: os.Getenv,
		Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
		Run: func(ctx context.Context, name string, args ...string) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return run(ctx, name, args...)
		},
	}
}

func (o *Ops) installer(env update.Env, cfg *config.Config, usePkexec bool, progress func(update.Progress)) *update.Installer {
	client := auth.NewHTTPClient(cfg.Network.Proxy)
	i := &update.Installer{
		PublicKey: o.updatePublicKey(), GOOS: env.GOOS, GOARCH: env.GOARCH,
		Downloader:    &update.Downloader{HTTP: client, Allow: o.Update.AllowDownload},
		TempRoot:      o.Update.TempRoot,
		Run:           o.updateRun(),
		StartDetached: o.Update.StartDetached,
		LookPath:      exec.LookPath,
		Geteuid:       os.Geteuid,
		UsePkexec:     usePkexec,
		Progress:      progress,
	}
	if i.StartDetached == nil {
		i.StartDetached = update.StartDetached
	}
	if o.Update.LookPath != nil {
		i.LookPath = o.Update.LookPath
	}
	if o.Update.Geteuid != nil {
		i.Geteuid = o.Update.Geteuid
	}
	if o.Update.GOOS != "" {
		i.GOOS = o.Update.GOOS
	}
	if o.Update.GOARCH != "" {
		i.GOARCH = o.Update.GOARCH
	}
	return i
}

// UpdateInfo is what is known about the newest release and what this
// installation can do about it.
type UpdateInfo struct {
	Current string `json:"current"`
	// Latest is the newest version the channel offers ("" when none is known yet).
	Latest     string `json:"latest"`
	Available  bool   `json:"available"`
	Prerelease bool   `json:"prerelease"`
	// URL is the page of that release, or the releases page.
	URL     string `json:"url"`
	Channel string `json:"channel"`
	// Dev is true for a development build, which is never offered an upgrade.
	Dev bool `json:"dev"`
	// CheckEnabled is config [update] check.
	CheckEnabled bool `json:"checkEnabled"`
	// CheckedAt is when GitHub last answered (RFC 3339), nil when never.
	CheckedAt *string `json:"checkedAt"`
	// Kind, Mode and Reason say how this program was installed and whether it
	// can upgrade itself (Mode "auto") or only point to the release page.
	Kind   string `json:"kind"`
	Mode   string `json:"mode"`
	Reason string `json:"reason"`
}

func (o *Ops) updateInfoFrom(cfg *config.Config, info update.Info) UpdateInfo {
	u := UpdateInfo{
		Current: info.Current, Channel: info.Channel, Dev: info.Dev, CheckEnabled: cfg.Update.Check,
		Available: info.Available, URL: ReleasesURL, CheckedAt: rfc3339(info.CheckedAt),
	}
	if info.Latest != nil {
		u.Latest, u.Prerelease = info.Latest.Version, info.Latest.Prerelease
		if info.Latest.URL != "" {
			u.URL = info.Latest.URL
		}
	}
	env := o.updateEnv()
	plan := o.installer(env, cfg, false, nil).PlanFor(update.Detect(context.Background(), env), info.Current)
	u.Kind, u.Mode, u.Reason = string(plan.Kind), plan.Mode, plan.Reason
	return u
}

// UpdateInfo reads what the last check found. It makes no request.
func (o *Ops) UpdateInfo() (UpdateInfo, error) {
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil {
		return UpdateInfo{}, err
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return UpdateInfo{}, err
	}
	return o.updateInfoFrom(cfg, o.updateService(cfg, dataDir).Cached()), nil
}

// UpdateCheck asks GitHub now. force is false for the check a window makes
// when it opens: that one only goes out when the daily check is due and is
// skipped when the check is switched off. A failed check still returns what
// was known, together with the error.
func (o *Ops) UpdateCheck(ctx context.Context, force bool) (UpdateInfo, error) {
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil {
		return UpdateInfo{}, err
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return UpdateInfo{}, err
	}
	svc := o.updateService(cfg, dataDir)
	if !force && !cfg.Update.Check {
		return o.updateInfoFrom(cfg, svc.Cached()), nil
	}
	info, err := svc.Check(ctx, force)
	return o.updateInfoFrom(cfg, info), err
}

// UpdateInstallOptions say how an upgrade may ask for what it needs.
type UpdateInstallOptions struct {
	// UsePkexec lets a Linux package upgrade ask for rights with pkexec (the
	// desktop app); the command line prints the sudo command instead.
	UsePkexec bool
	// Progress sees the stages and the download progress.
	Progress func(update.Progress)
	// Version, when not empty, is the version the person agreed to; the
	// upgrade is refused when the newest release has become another one.
	Version string
}

// UpdateResult is what an upgrade did.
type UpdateResult struct {
	// UpToDate is true when there was nothing newer to install.
	UpToDate bool `json:"upToDate"`
	update.Outcome
	// Service says what happened to the background service: "" (nothing to do),
	// "restarted", "not_installed", "not_running" (stopped on purpose, left
	// alone) or "failed" (ServiceNote says why).
	Service     string `json:"service"`
	ServiceNote string `json:"serviceNote"`
}

// UpdateInstall upgrades this installation to the newest release: download,
// verify, install, restart the background service. An installation that cannot
// upgrade itself is refused with an *update.ManualError.
func (o *Ops) UpdateInstall(ctx context.Context, opts UpdateInstallOptions) (UpdateResult, error) {
	cfg, _, err := o.LoadConfigOrDefault()
	if err != nil {
		return UpdateResult{}, err
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return UpdateResult{}, err
	}
	svc := o.updateService(cfg, dataDir)
	env := o.updateEnv()
	in := update.Detect(ctx, env)
	inst := o.installer(env, cfg, opts.UsePkexec, opts.Progress)
	if p := inst.PlanFor(in, svc.Current); p.Mode != update.ModeAuto {
		return UpdateResult{}, &update.ManualError{Plan: p}
	}
	if in.Kind == update.KindWindowsCLI || in.Kind == update.KindWindowsSetup {
		update.CleanupAside(in.Dir)
	}
	rel, err := svc.Release(ctx)
	if err != nil {
		return UpdateResult{}, err
	}
	if v, err := rel.Version(); err != nil || !update.Newer(svc.Current, v.String()) {
		return UpdateResult{UpToDate: true}, nil
	} else if opts.Version != "" && opts.Version != v.String() {
		return UpdateResult{}, fmt.Errorf("version %s was announced but the newest release is now %s; check again and confirm that one", opts.Version, v)
	}
	unlock, err := lockUpdate(dataDir)
	if err != nil {
		return UpdateResult{}, err
	}
	defer unlock()
	out, err := inst.Install(ctx, rel, in, svc.Current)
	res := UpdateResult{Outcome: out}
	if err != nil {
		return res, err
	}
	if out.Done && out.RestartService {
		res.Service, res.ServiceNote = o.restartService()
	}
	return res, nil
}

// CleanupUpdateLeftovers removes the .old programs a Windows upgrade left next
// to the running program. Best effort and cheap; call it at start-up.
func (o *Ops) CleanupUpdateLeftovers() {
	if runtime.GOOS != "windows" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	update.CleanupAside(filepath.Dir(exe))
}

// lockUpdate takes the lock that keeps two upgrades (the command line and a
// window, say) from replacing the same files at once.
func lockUpdate(dataDir string) (func(), error) {
	if err := paths.EnsureDir(dataDir); err != nil {
		return nil, err
	}
	fl := flock.New(filepath.Join(dataDir, "update.lock"))
	ok, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock the upgrade: %w", err)
	}
	if !ok {
		return nil, errors.New("another upgrade is already running")
	}
	return func() { _ = fl.Unlock() }, nil
}

// restartService restarts the background service after its program was replaced.
func (o *Ops) restartService() (state, note string) {
	m, err := o.ServiceManager()
	if err != nil {
		if errors.Is(err, service.ErrUnsupported) {
			return "not_installed", ""
		}
		return "failed", err.Error()
	}
	return restartManaged(m)
}

// restartManaged restarts the service of m unless it is stopped or its state is unknown.
func restartManaged(m service.Manager) (state, note string) {
	// A service that was stopped on purpose stays stopped: it runs the new
	// program whenever it is started next.
	st, serr := m.Status()
	if serr != nil {
		// Without the state a stopped service might be started by mistake.
		return "failed", fmt.Sprintf("cannot read the state of the service (%v); restart it yourself if it is running", serr)
	}
	if st.Installed && st.Running == service.No {
		return "not_running", ""
	}
	if _, err := m.Restart(); err != nil {
		if errors.Is(err, service.ErrNotInstalled) {
			return "not_installed", ""
		}
		return "failed", fmt.Sprintf("%v; restart it with: e5extend service install", err)
	}
	return "restarted", ""
}

// EngineUpdater is the update check of the engine: once a day, quietly.
func (o *Ops) EngineUpdater(cfg *config.Config) engine.UpdateChecker {
	dataDir, err := paths.DataDir()
	if err != nil {
		return nil
	}
	return &engineUpdater{svc: o.updateService(cfg, dataDir)}
}

type engineUpdater struct{ svc *update.Service }

func (u *engineUpdater) Check(ctx context.Context, channel string) (engine.UpdateStatus, error) {
	svc := *u.svc
	svc.Channel = channel // the configuration may have changed since
	st := engine.UpdateStatus{}
	if update.IsDevBuild(svc.Current) {
		st.Next = time.Now().Add(update.CheckEvery)
		return st, nil
	}
	info, err := svc.Check(ctx, false)
	st.Next = svc.Next()
	if info.Available && info.Latest != nil {
		st.Version, st.URL = info.Latest.Version, info.Latest.URL
	}
	return st, err
}
