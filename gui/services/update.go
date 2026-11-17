package services

import (
	"errors"
	"sync"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/ops"
	"github.com/KKazuhaK/e5-developer-extend/internal/update"
)

// UpdateInfo is what is known about the newest release and what this
// installation can do about it.
type UpdateInfo = ops.UpdateInfo

// UpdateResult is what an upgrade did.
type UpdateResult = ops.UpdateResult

// UpdateProgressEvent is the event an upgrade reports on; its data is an UpdateProgress.
const UpdateProgressEvent = "update:progress"

// UpdateProgress is one report of a running upgrade. Stage is "download",
// "verify", "install", "done" or "error".
// "done" carries Result; "error" carries Code and Error. Code is stable:
// no_signature, no_key, bad_signature, checksum, manual_docker, manual_dev,
// manual_no_key, manual_unsupported or other (Error is English text).
type UpdateProgress struct {
	Stage  string        `json:"stage"`
	File   string        `json:"file"`
	Done   int64         `json:"done"`
	Total  int64         `json:"total"`
	Result *UpdateResult `json:"result"`
	Code   string        `json:"code"`
	Error  string        `json:"error"`
}

// exitDelay is how long a window stays after the Windows installer was
// started, so the last report reaches the screen before the app goes.
var exitDelay = 1500 * time.Millisecond

// progressEvery limits how often download progress is reported.
const progressEvery = 100 * time.Millisecond

// UpdateService reports on new versions and upgrades the app.
type UpdateService struct{ b *Backend }

// NewUpdateService returns the service.
func NewUpdateService(b *Backend) *UpdateService { return &UpdateService{b} }

// Info reads what the last check found; it makes no request.
func (s *UpdateService) Info() (UpdateInfo, error) { return s.b.Ops.UpdateInfo() }

// Check asks GitHub. With force false (what a window does when it opens) the
// request only goes out when the daily check is due and the check is on; with
// force true (the "check now" button) it always does. A failed check rejects
// with the reason; the last known answer stays available through Info.
func (s *UpdateService) Check(force bool) (UpdateInfo, error) {
	return s.b.Ops.UpdateCheck(s.b.bgCtx, force)
}

// Install upgrades the app and returns at once; the upgrade reports on the
// event "update:progress". Only one upgrade runs at a time.
func (s *UpdateService) Install() error {
	b := s.b
	b.mu.Lock()
	if b.updateBusy {
		b.mu.Unlock()
		return errors.New("an upgrade is already running")
	}
	b.updateBusy = true
	b.mu.Unlock()
	end := b.begin() // the window waits for an upgrade in progress before it exits
	go func() {
		res, err := s.run()
		b.mu.Lock()
		b.updateBusy = false
		b.mu.Unlock()
		end()
		if err != nil {
			code, msg := updateErrorCode(err)
			b.Emit(UpdateProgressEvent, UpdateProgress{Stage: "error", Code: code, Error: msg, Result: &res})
			return
		}
		b.Emit(UpdateProgressEvent, UpdateProgress{Stage: "done", Result: &res})
		if res.ExitNow && b.Exit != nil {
			// A setup program is replacing this app's files.
			time.Sleep(exitDelay)
			b.Exit()
		}
	}()
	return nil
}

func (s *UpdateService) run() (UpdateResult, error) {
	b := s.b
	var mu sync.Mutex
	var last time.Time
	lastStage := ""
	progress := func(p update.Progress) {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		// Always report a new stage and the end of a file; throttle the rest.
		if p.Stage == lastStage && p.Done != p.Total && now.Sub(last) < progressEvery {
			return
		}
		lastStage, last = p.Stage, now
		b.Emit(UpdateProgressEvent, UpdateProgress{Stage: p.Stage, File: p.File, Done: p.Done, Total: p.Total})
	}
	return b.Ops.UpdateInstall(b.bgCtx, ops.UpdateInstallOptions{UsePkexec: true, Progress: progress})
}

// Restart closes the app and starts it again, so the upgraded program runs.
func (s *UpdateService) Restart() error {
	if s.b.RestartApp == nil {
		return errors.New("restarting is not available here")
	}
	return s.b.RestartApp()
}

// updateErrorCode names a failed upgrade for the interface and gives the
// English text.
func updateErrorCode(err error) (code, msg string) {
	var me *update.ManualError
	switch {
	case errors.As(err, &me):
		return "manual_" + me.Plan.Reason, me.Error()
	case errors.Is(err, update.ErrNoSignature):
		return "no_signature", err.Error()
	case errors.Is(err, update.ErrNoPublicKey):
		return "no_key", err.Error()
	case errors.Is(err, update.ErrBadSignature):
		return "bad_signature", err.Error()
	case errors.Is(err, update.ErrChecksum):
		return "checksum", err.Error()
	}
	return "other", err.Error()
}
