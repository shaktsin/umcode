package workbench

import (
	"context"
	"math"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var vmPageSize = regexp.MustCompile(`page size of (\d+) bytes`)

func parseVMStatAvailable(output string) uint64 {
	match := vmPageSize.FindStringSubmatch(output)
	if len(match) != 2 {
		return 0
	}
	pageSize, err := strconv.ParseUint(match[1], 10, 64)
	if err != nil || pageSize == 0 {
		return 0
	}
	var pages uint64
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		switch strings.TrimSpace(parts[0]) {
		case "Pages free", "Pages inactive", "Pages speculative":
			count, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(parts[1]), "."), 10, 64)
			if err != nil {
				return 0
			}
			pages += count
		}
	}
	if pages > ^uint64(0)/pageSize {
		return 0
	}
	return pages * pageSize
}

func parseProcMemAvailable(output string) uint64 {
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[2] != "kB" {
			return 0
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || value > ^uint64(0)/1024 {
			return 0
		}
		return value * 1024
	}
	return 0
}

// TrainingAvailableMemory returns reclaimable system memory at the moment of
// preflight. Zero means measurement failed and triggers a conservative fallback.
func TrainingAvailableMemory() uint64 {
	switch runtime.GOOS {
	case "darwin":
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "/usr/bin/vm_stat").Output()
		if err == nil {
			return parseVMStatAvailable(string(output))
		}
	case "linux":
		output, err := os.ReadFile("/proc/meminfo")
		if err == nil {
			return parseProcMemAvailable(string(output))
		}
	}
	return 0
}

const (
	minElementLimit = 8_000_000
	maxElementLimit = 128_000_000
)

// ElementLimit is the per-tensor activation element cap handed to the Python
// workers. With unknown memory it stays at the conservative 8 million. Otherwise
// it scales with free memory (a quarter of it, minus the 256 MiB PyTorch base,
// at 16 bytes per element for forward/backward copies), never below 8 million
// or above 128 million. Batch size is still checked against memory separately.
func ElementLimit(availableBytes uint64) int {
	if availableBytes == 0 {
		return minElementLimit
	}
	budget := float64(availableBytes)*.25 - float64(256<<20)
	if budget <= 0 {
		return minElementLimit
	}
	return int(math.Max(minElementLimit, math.Min(maxElementLimit, math.Floor(budget/16))))
}

const (
	minWorkLimit      = 500_000_000
	workPerCore       = 20
	maxWorkCores      = 16
	minParameterLimit = 16_000_000
	maxParameterLimit = 256_000_000
)

// WorkLimit is the per-sample multiply-add cap for training and inference.
// With unknown memory it stays at 500 million. Otherwise it grows with CPU
// cores (20x the base per core, up to 16 cores): training is slower with a
// higher cap, but no longer refused.
func WorkLimit(availableBytes uint64, cpus int) int {
	if availableBytes == 0 {
		return minWorkLimit
	}
	return minWorkLimit * workPerCore * min(max(cpus, 1), maxWorkCores)
}

// ParameterLimit is the parameter cap: a quarter of free memory at 16 bytes per
// parameter (weights, gradients, Adam state), between 16 million and 256 million.
func ParameterLimit(availableBytes uint64) int {
	if availableBytes == 0 {
		return minParameterLimit
	}
	return int(math.Max(minParameterLimit, math.Min(maxParameterLimit, math.Floor(float64(availableBytes)*.25/16))))
}

// workerEnv is the environment for the Python training and inference workers.
// The raised element cap is only passed when free memory is actually known.
func workerEnv(availableBytes uint64) []string {
	env := append(os.Environ(), "OMP_NUM_THREADS=1", "MKL_NUM_THREADS=1", "TOKENIZERS_PARALLELISM=false")
	if availableBytes > 0 {
		env = append(env,
			"UMCODE_MAX_ELEMENTS="+strconv.Itoa(ElementLimit(availableBytes)),
			"UMCODE_MAX_WORK="+strconv.Itoa(WorkLimit(availableBytes, runtime.NumCPU())),
			"UMCODE_MAX_PARAMETERS="+strconv.Itoa(ParameterLimit(availableBytes)))
	}
	return env
}
