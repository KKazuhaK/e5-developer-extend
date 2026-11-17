package services

import (
	"context"
	"errors"
	"time"

	"github.com/KKazuhaK/e5-developer-extend/internal/doctor"
	"github.com/KKazuhaK/e5-developer-extend/internal/service"
)

// HealthService runs the health checks and manages the background service.
type HealthService struct{ b *Backend }

// NewHealthService returns the service.
func NewHealthService(b *Backend) *HealthService { return &HealthService{b} }

// Doctor runs the health checks. offline skips the ones that need the network.
func (s *HealthService) Doctor(offline bool) ([]DoctorCheck, error) {
	checks, err := s.b.Ops.Doctor(context.Background(), offline)
	if err != nil {
		return nil, err
	}
	out := make([]DoctorCheck, 0, len(checks))
	for _, c := range checks {
		out = append(out, DoctorCheck{
			ID: c.ID, Title: c.Title, Level: string(c.Level), Detail: c.Detail, Fix: c.Fix,
			Parts:    doctorParts(c.Code, c.Params, c.Parts),
			FixParts: doctorParts(c.FixCode, c.FixParams, c.FixParts),
		})
	}
	return out, nil
}

// doctorParts turns a check's single message or its list of messages into one
// list, never nil, with every Params map set.
func doctorParts(code string, params map[string]string, parts []doctor.Msg) []DoctorMsg {
	if code != "" {
		parts = []doctor.Msg{{Code: code, Params: params}}
	}
	out := make([]DoctorMsg, 0, len(parts))
	for _, m := range parts {
		p := make(map[string]string, len(m.Params))
		for k, v := range m.Params {
			p[k] = v
		}
		out = append(out, DoctorMsg{Code: m.Code, Params: p})
	}
	return out
}

// ServiceInfo tells whether the background service is installed and running.
func (s *HealthService) ServiceInfo() (ServiceInfo, error) {
	st, err := s.b.Ops.ServiceStatus()
	if err != nil {
		if errors.Is(err, service.ErrUnsupported) {
			return ServiceInfo{Platform: "unsupported", Enabled: "unknown", Running: "unknown", Details: []string{}}, nil
		}
		return ServiceInfo{}, err
	}
	return ServiceInfo{
		Platform: st.Platform, Installed: st.Installed, Enabled: st.Enabled.String(),
		Running: st.Running.String(), Details: append([]string{}, st.Details...),
	}, nil
}

// InstallService installs and starts the background service. When the tray
// app runs the engine, it is asked to hand it over first, but only after the
// checks that need no side effects have passed; if the install still fails,
// the handover is withdrawn so the tray takes the engine back at once.
func (s *HealthService) InstallService() (ServiceResult, error) {
	b := s.b
	defer b.begin()()
	if err := b.Ops.CheckServiceInstall(); err != nil {
		return ServiceResult{}, err
	}
	handed := false
	if b.RequestHandover != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		err := b.RequestHandover(ctx)
		handed = true
		if err != nil {
			s.undoHandover()
			return ServiceResult{}, err
		}
	}
	res, err := b.Ops.ServiceInstall(false, false)
	if err != nil && handed {
		s.undoHandover()
	}
	return toResult(res), err
}

func (s *HealthService) undoHandover() {
	if s.b.CancelHandover != nil {
		s.b.CancelHandover()
	}
}

// StartService starts the installed background service when it is stopped.
// Like an install it first asks the tray to hand the engine over, so the two
// do not run it at once; if the start fails the handover is withdrawn.
func (s *HealthService) StartService() (ServiceResult, error) {
	b := s.b
	defer b.begin()()
	// Nothing to start: say so before the tray is asked to give anything up.
	st, err := b.Ops.ServiceStatus()
	if err != nil {
		return ServiceResult{}, err
	}
	if !st.Installed {
		return ServiceResult{}, service.ErrNotInstalled
	}
	handed := false
	if b.RequestHandover != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		err := b.RequestHandover(ctx)
		handed = true
		if err != nil {
			s.undoHandover()
			return ServiceResult{}, err
		}
	}
	res, err := b.Ops.ServiceStart()
	if err != nil && handed {
		s.undoHandover()
	}
	return toResult(res), err
}

// UninstallService stops and removes the background service.
func (s *HealthService) UninstallService() (ServiceResult, error) {
	defer s.b.begin()()
	res, err := s.b.Ops.ServiceUninstall()
	if err == nil {
		// Same signal as a withdrawn handover: the tray drops its cached
		// "installed" answer at once instead of within 5 minutes.
		s.undoHandover()
	}
	return toResult(res), err
}

func toResult(r service.Result) ServiceResult {
	return ServiceResult{
		Actions:  append([]string{}, r.Actions...),
		Warnings: append([]string{}, r.Warnings...),
		Manual:   append([]string{}, r.Manual...),
	}
}
