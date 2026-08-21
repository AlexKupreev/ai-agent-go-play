package api

import (
	"errors"
	"fmt"
	"net/http"

	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/guidance"
	"ai-agent-go-play/internal/hoststat"
	"ai-agent-go-play/internal/session"
	"ai-agent-go-play/internal/tools"
)

// StatusRuntime supplies process-local facts that do not belong to EffectiveConfig.
// The cmd layer owns the filesystem layout and space registry, so it resolves both
// through this seam without teaching the transport core either layout.
type StatusRuntime struct {
	Version      string
	WorkDir      string
	StateDirs    []tools.StateDir
	ResolveSpace func(id string) (StatusSpace, error)
}

type StatusValue struct {
	Requested string `json:"requested"`
	Effective string `json:"effective"`
}

type StatusSpace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type StatusSession struct {
	ID            string       `json:"id"`
	Model         StatusValue  `json:"model"`
	Tier          StatusValue  `json:"tier"`
	GuidanceChars int          `json:"guidance_chars"`
	ActiveSpace   *StatusSpace `json:"active_space"`
}

type StatusHost struct {
	CPUCount          int     `json:"cpu_count"`
	Load1             float64 `json:"load_1"`
	Load5             float64 `json:"load_5"`
	Load15            float64 `json:"load_15"`
	MemoryTotalMB     uint64  `json:"memory_total_mb"`
	MemoryAvailableMB uint64  `json:"memory_available_mb"`
	DiskTotalMB       uint64  `json:"disk_total_mb"`
	DiskFreeMB        uint64  `json:"disk_free_mb"`
	ProcessRSSMB      uint64  `json:"process_rss_mb"`
	Goroutines        int     `json:"goroutines"`
	GoHeapMB          uint64  `json:"go_heap_mb"`
	HostUptimeSeconds int64   `json:"host_uptime_seconds"`
}

type StatusResponse struct {
	Version string             `json:"version"`
	Config  EffectiveConfig    `json:"config"`
	Session *StatusSession     `json:"session,omitempty"`
	Host    StatusHost         `json:"host"`
	State   []tools.StateUsage `json:"state"`
}

// SetStatusRuntime installs the host/state/space-name side of GET /status. It is
// optional for embedders; zero values still produce a valid engine-only snapshot.
func (e *Engine) SetStatusRuntime(runtime StatusRuntime) {
	runtime.StateDirs = append([]tools.StateDir(nil), runtime.StateDirs...)
	e.statusRuntime = runtime
}

func (e *Engine) status(sessionID *string) (StatusResponse, error) {
	var config EffectiveConfig
	if e.effectiveConfig != nil {
		config = e.effectiveConfig.EffectiveConfig()
	}
	workDir := e.statusRuntime.WorkDir
	if workDir == "" {
		workDir = config.Workspace
	}
	host := hoststat.Read(workDir)
	response := StatusResponse{
		Version: e.statusRuntime.Version,
		Config:  config,
		Host: StatusHost{
			CPUCount: host.NumCPU, Load1: host.Load1, Load5: host.Load5, Load15: host.Load15,
			MemoryTotalMB: host.MemTotalMB, MemoryAvailableMB: host.MemAvailMB,
			DiskTotalMB: host.DiskTotalMB, DiskFreeMB: host.DiskFreeMB,
			ProcessRSSMB: host.ProcRSSMB, Goroutines: host.Goroutines, GoHeapMB: host.GoHeapMB,
			HostUptimeSeconds: int64(host.HostUptime.Seconds()),
		},
		State: tools.SnapshotState(e.statusRuntime.StateDirs),
	}
	if sessionID == nil {
		return response, nil
	}
	if !e.SessionsEnabled() {
		return StatusResponse{}, session.ErrNotFound
	}
	sess, err := e.sessions.Get(*sessionID)
	if err != nil {
		return StatusResponse{}, err
	}
	model := config.Model.Value
	if sess.Model != "" {
		model = sess.Model
	}
	tier := config.TierCeiling.Value
	if sess.Tier != "" {
		requested, parseErr := capability.ParseTier(sess.Tier)
		if parseErr != nil {
			return StatusResponse{}, fmt.Errorf("session %s tier: %w", sess.ID, parseErr)
		}
		ceiling, parseErr := capability.ParseTier(config.TierCeiling.Value)
		if parseErr != nil {
			return StatusResponse{}, fmt.Errorf("effective tier ceiling: %w", parseErr)
		}
		tier = string(capability.ClampTier(requested, ceiling))
	}
	statusSession := &StatusSession{
		ID:            sess.ID,
		Model:         StatusValue{Requested: sess.Model, Effective: model},
		Tier:          StatusValue{Requested: sess.Tier, Effective: tier},
		GuidanceChars: guidance.CharCount(sess.Guidance),
	}
	if sess.Space != "" {
		if e.statusRuntime.ResolveSpace == nil {
			return StatusResponse{}, fmt.Errorf("resolve active space %q: resolver is not configured", sess.Space)
		}
		active, resolveErr := e.statusRuntime.ResolveSpace(sess.Space)
		if resolveErr != nil {
			return StatusResponse{}, fmt.Errorf("resolve active space %q: %w", sess.Space, resolveErr)
		}
		statusSession.ActiveSpace = &active
	}
	response.Session = statusSession
	return response, nil
}

func handleStatus(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sessionID *string
		if r.URL.Query().Has("session_id") {
			id := r.URL.Query().Get("session_id")
			if id == "" {
				http.Error(w, "session_id must not be empty", http.StatusBadRequest)
				return
			}
			sessionID = &id
		}
		response, err := e.status(sessionID)
		if err != nil {
			switch {
			case errors.Is(err, session.ErrNotFound), errors.Is(err, ErrSessionsDisabled):
				http.Error(w, err.Error(), http.StatusNotFound)
			default:
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		writeJSON(w, response)
	}
}
