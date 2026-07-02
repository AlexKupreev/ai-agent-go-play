// Package hoststat takes a best-effort snapshot of host and process resources, so the
// agent can be aware of the machine it runs on (CPU load, memory, disk, its own RSS,
// uptime) — apt for the low-resource-box deployment this project targets. It is not a
// new capability (the agent can already shell out to df/free/uptime) but a structured,
// reliable convenience. Most fields come from Linux /proc; on other platforms only the
// Go-runtime-derived fields are filled and the rest stay zero.
package hoststat

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const miB = 1024 * 1024

// Stats is a resource snapshot. A zero field means "unavailable on this platform".
type Stats struct {
	NumCPU               int
	Load1, Load5, Load15 float64
	MemTotalMB           uint64
	MemAvailMB           uint64
	DiskTotalMB          uint64
	DiskFreeMB           uint64
	ProcRSSMB            uint64
	Goroutines           int
	GoHeapMB             uint64
	HostUptime           time.Duration
}

// Read snapshots resources now. diskPath selects the filesystem to report free space
// for (e.g. the agent's working directory); "" falls back to "/". Every source is
// best-effort — a failed read leaves its fields zero rather than erroring.
func Read(diskPath string) Stats {
	if diskPath == "" {
		diskPath = "/"
	}
	s := Stats{NumCPU: runtime.NumCPU(), Goroutines: runtime.NumGoroutine()}

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	s.GoHeapMB = m.HeapAlloc / miB

	s.Load1, s.Load5, s.Load15 = readLoadAvg()
	if total, avail := readMeminfo(); total > 0 {
		s.MemTotalMB, s.MemAvailMB = total/miB, avail/miB
	}
	if total, free := statfs(diskPath); total > 0 {
		s.DiskTotalMB, s.DiskFreeMB = total/miB, free/miB
	}
	s.ProcRSSMB = readProcRSS() / miB
	s.HostUptime = readUptime()
	return s
}

// readLoadAvg parses /proc/loadavg ("0.42 0.38 0.30 ..."). Zeros if unavailable.
func readLoadAvg() (l1, l5, l15 float64) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return 0, 0, 0
	}
	l1, _ = strconv.ParseFloat(f[0], 64)
	l5, _ = strconv.ParseFloat(f[1], 64)
	l15, _ = strconv.ParseFloat(f[2], 64)
	return l1, l5, l15
}

// readMeminfo returns total and available memory in bytes from /proc/meminfo.
func readMeminfo() (total, avail uint64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		kb, _ := strconv.ParseUint(f[1], 10, 64) // values are in kB
		switch f[0] {
		case "MemTotal:":
			total = kb * 1024
		case "MemAvailable:":
			avail = kb * 1024
		}
	}
	return total, avail
}

// readUptime returns host uptime from /proc/uptime. Zero if unavailable.
func readUptime() time.Duration {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	secs, _ := strconv.ParseFloat(f[0], 64)
	return time.Duration(secs) * time.Second
}

// readProcRSS returns this process's resident set size in bytes from /proc/self/statm
// (field 2 = resident pages). Zero if unavailable.
func readProcRSS() uint64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, _ := strconv.ParseUint(f[1], 10, 64)
	return pages * uint64(os.Getpagesize())
}

// statfs returns total and available bytes of the filesystem containing path.
func statfs(path string) (total, free uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, st.Bavail * bs
}
