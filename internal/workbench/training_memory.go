package workbench

import (
	"context"
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
