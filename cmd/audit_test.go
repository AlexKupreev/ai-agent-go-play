package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-agent-go-play/internal/audit"

	"github.com/spf13/cobra"
)

func auditTestCmd(t *testing.T, explicitAddr string) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "audit-test"}
	c.Flags().String("addr", "127.0.0.1:8080", "")
	if explicitAddr != "" {
		if err := c.Flags().Set("addr", explicitAddr); err != nil {
			t.Fatalf("set addr: %v", err)
		}
	}
	return c
}

func useAuditTestConfig(t *testing.T) string {
	t.Helper()
	orig := configDirFlag
	t.Cleanup(func() { configDirFlag = orig })
	configDirFlag = t.TempDir()
	t.Setenv(envConfigDir, "")
	return configDirFlag
}

func seedCentralAudit(t *testing.T, events ...audit.Event) {
	t.Helper()
	path, err := auditPath()
	if err != nil {
		t.Fatal(err)
	}
	rec, err := audit.NewJSONLRecorder(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		rec.Record(event)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAuditEvents_LocalDefaultFiltersAndLimit(t *testing.T) {
	useAuditTestConfig(t)
	seedCentralAudit(t,
		audit.Event{Type: audit.EventCapabilityExercised, Run: "run-a"},
		audit.Event{Type: audit.EventToolRevoked},
		audit.Event{Type: audit.EventCapabilityFailed, Run: "run-a"},
		audit.Event{Type: audit.EventCapabilityExercised, Run: "run-b"},
	)
	cmd := auditTestCmd(t, "")

	events, err := loadAuditEvents(cmd, "127.0.0.1:1", "run-a", "", 1)
	if err != nil {
		t.Fatalf("local audit: %v", err)
	}
	if len(events) != 1 || events[0].Type != audit.EventCapabilityFailed {
		t.Fatalf("run filter + limit = %+v, want last run-a event", events)
	}

	events, err = loadAuditEvents(cmd, "127.0.0.1:1", "", audit.EventCapabilityExercised, 0)
	if err != nil {
		t.Fatalf("local type filter: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("type filter = %d events, want 2", len(events))
	}
}

func TestLoadAuditEvents_ExplicitAddrUsesRemote(t *testing.T) {
	useAuditTestConfig(t)
	seedCentralAudit(t, audit.Event{Type: audit.EventMemoryWrite, Run: "local"})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audit" || r.URL.Query().Get("run") != "remote-run" ||
			r.URL.Query().Get("type") != audit.EventToolRevoked || r.URL.Query().Get("limit") != "2" {
			t.Errorf("remote request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode([]audit.Event{{Type: audit.EventToolRevoked, Run: "remote-run"}})
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	cmd := auditTestCmd(t, addr)
	events, err := loadAuditEvents(cmd, addr, "remote-run", audit.EventToolRevoked, 2)
	if err != nil {
		t.Fatalf("remote audit: %v", err)
	}
	if len(events) != 1 || events[0].Run != "remote-run" {
		t.Fatalf("remote selection returned %+v", events)
	}
}

func TestLoadAuditEvents_MissingLocalFileIsEmptyAndUncreated(t *testing.T) {
	dir := useAuditTestConfig(t)
	cmd := auditTestCmd(t, "")
	events, err := loadAuditEvents(cmd, "127.0.0.1:1", "", "", 0)
	if err != nil {
		t.Fatalf("missing local audit: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("missing local audit returned %+v, want empty", events)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("local audit read created missing file: stat error = %v", err)
	}
}
