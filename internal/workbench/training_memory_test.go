package workbench

import "testing"

func TestParseAvailableMemory(t *testing.T) {
	mac := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 1000.\nPages inactive: 2000.\nPages speculative: 100.\nPages wired down: 500.\n"
	if got := parseVMStatAvailable(mac); got != 3100*16384 {
		t.Fatalf("mac available=%d", got)
	}
	linux := "MemTotal: 8192000 kB\nMemAvailable: 2048000 kB\n"
	if got := parseProcMemAvailable(linux); got != 2048000*1024 {
		t.Fatalf("linux available=%d", got)
	}
}
