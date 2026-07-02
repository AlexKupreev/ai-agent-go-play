package hoststat

import (
	"runtime"
	"testing"
)

func TestRead(t *testing.T) {
	s := Read(".")

	// Runtime-derived fields are available on every platform.
	if s.NumCPU <= 0 {
		t.Errorf("NumCPU = %d, want > 0", s.NumCPU)
	}
	if s.Goroutines <= 0 {
		t.Errorf("Goroutines = %d, want > 0", s.Goroutines)
	}

	// statfs works on any Unix; the current dir's filesystem has a size.
	if s.DiskTotalMB == 0 {
		t.Errorf("DiskTotalMB = 0, want the working filesystem's size")
	}
	if s.DiskFreeMB > s.DiskTotalMB {
		t.Errorf("DiskFreeMB %d > DiskTotalMB %d", s.DiskFreeMB, s.DiskTotalMB)
	}

	// /proc-sourced fields are Linux-only.
	if runtime.GOOS == "linux" {
		if s.MemTotalMB == 0 {
			t.Errorf("MemTotalMB = 0 on linux, want /proc/meminfo value")
		}
		if s.MemAvailMB > s.MemTotalMB {
			t.Errorf("MemAvailMB %d > MemTotalMB %d", s.MemAvailMB, s.MemTotalMB)
		}
		if s.HostUptime <= 0 {
			t.Errorf("HostUptime = %v on linux, want > 0", s.HostUptime)
		}
	}
}

func TestRead_EmptyPathFallsBackToRoot(t *testing.T) {
	// "" must not error/panic; it reports the root filesystem.
	if s := Read(""); s.DiskTotalMB == 0 {
		t.Errorf("Read(\"\") DiskTotalMB = 0, want root filesystem size")
	}
}
