package workbench

import (
	"strconv"
	"strings"
	"testing"
)

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

func TestElementLimitScalesWithMemory(t *testing.T) {
	if got := ElementLimit(0); got != 8_000_000 {
		t.Fatalf("unknown memory must keep the conservative cap, got %d", got)
	}
	if got := ElementLimit(512 << 20); got != 8_000_000 {
		t.Fatalf("a small machine never drops below the old cap, got %d", got)
	}
	mid := ElementLimit(8 << 30)
	if mid <= 8_000_000 || mid >= 128_000_000 {
		t.Fatalf("8 GiB free should land between the floor and ceiling, got %d", mid)
	}
	if ElementLimit(16<<30) < mid {
		t.Fatal("more free memory must not lower the cap")
	}
	if got := ElementLimit(1 << 50); got != 128_000_000 {
		t.Fatalf("the cap is bounded by the hard ceiling, got %d", got)
	}
}

func TestWorkerEnvCarriesElementLimit(t *testing.T) {
	env := workerEnv(8 << 30)
	want := "UMCODE_MAX_ELEMENTS=" + strconv.Itoa(ElementLimit(8<<30))
	found := false
	for _, kv := range env {
		if kv == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing %s", want)
	}
	for _, kv := range workerEnv(0) {
		if strings.HasPrefix(kv, "UMCODE_MAX_ELEMENTS=") {
			t.Fatalf("unknown memory must not pass a raised cap: %s", kv)
		}
	}
}

func TestWorkAndParameterLimitsScaleWithMachine(t *testing.T) {
	if WorkLimit(0, 10) != 500_000_000 || ParameterLimit(0) != 16_000_000 {
		t.Fatal("unknown memory keeps the conservative defaults")
	}
	if got := WorkLimit(16<<30, 10); got != 100_000_000_000 {
		t.Fatalf("10 cores should allow 100G multiply-adds per sample, got %d", got)
	}
	if got := WorkLimit(16<<30, 64); got != 160_000_000_000 {
		t.Fatalf("work is capped at 16 cores, got %d", got)
	}
	if got := WorkLimit(16<<30, 0); got != 10_000_000_000 {
		t.Fatalf("a bad core count counts as one core, got %d", got)
	}
	if got := ParameterLimit(16 << 30); got != 256_000_000 {
		t.Fatalf("16 GiB free allows the 256M ceiling, got %d", got)
	}
	if got := ParameterLimit(512 << 20); got != 16_000_000 {
		t.Fatalf("a small machine never drops below 16M, got %d", got)
	}
}

func TestWorkerEnvCarriesWorkAndParameterLimits(t *testing.T) {
	joined := strings.Join(workerEnv(16<<30), "\n")
	for _, name := range []string{"UMCODE_MAX_WORK=", "UMCODE_MAX_PARAMETERS=256000000"} {
		if !strings.Contains(joined, name) {
			t.Fatalf("missing %s in %s", name, joined)
		}
	}
	if strings.Contains(strings.Join(workerEnv(0), "\n"), "UMCODE_MAX_WORK") {
		t.Fatal("unknown memory must not raise the work cap")
	}
}
