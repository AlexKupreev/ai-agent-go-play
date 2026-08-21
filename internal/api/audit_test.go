package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-agent-go-play/internal/audit"
)

func seededLog() *audit.MemoryRecorder {
	rec := &audit.MemoryRecorder{}
	rec.Record(audit.Event{Type: audit.EventCapabilityExercised, Run: "run-a"})
	rec.Record(audit.Event{Type: audit.EventToolRevoked, Fields: map[string]any{"name": "x"}})
	rec.Record(audit.Event{Type: audit.EventCapabilityFailed, Run: "run-a"})
	rec.Record(audit.Event{Type: audit.EventCapabilityExercised, Run: "run-b"})
	return rec
}

func TestHTTP_Audit(t *testing.T) {
	log := seededLog()
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, nil, nil, log))
	defer srv.Close()

	get := func(query string) []audit.Event {
		resp, err := http.Get(srv.URL + "/audit" + query)
		if err != nil {
			t.Fatalf("GET %s: %v", query, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d", query, resp.StatusCode)
		}
		var out []audit.Event
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	if len(get("")) != 4 {
		t.Fatalf("unfiltered = %d, want 4", len(get("")))
	}
	if evs := get("?run=run-a"); len(evs) != 2 || evs[0].Run != "run-a" {
		t.Fatalf("run filter = %+v", evs)
	}
	if evs := get("?type=capability_failed"); len(evs) != 1 || evs[0].Type != audit.EventCapabilityFailed {
		t.Fatalf("failed type filter = %+v", evs)
	}
	if evs := get("?type=tool_revoked"); len(evs) != 1 || evs[0].Type != audit.EventToolRevoked {
		t.Fatalf("type filter = %+v", evs)
	}
	if evs := get("?limit=1"); len(evs) != 1 {
		t.Fatalf("limit = %d, want 1", len(evs))
	}
	// Bad limit is a 400.
	resp, err := http.Get(srv.URL + "/audit?limit=abc")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad limit status = %d, want 400", resp.StatusCode)
	}
}

func TestHTTP_AuditAbsentWithoutReader(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/audit")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (no reader wired)", resp.StatusCode)
	}
}

func TestClient_Audit(t *testing.T) {
	log := seededLog()
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, nil, nil, log))
	defer srv.Close()
	c := NewClient(srv.URL)

	evs, err := c.Audit(context.Background(), "", "tool_revoked", 0)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != audit.EventToolRevoked {
		t.Fatalf("client audit filter = %+v", evs)
	}
}
