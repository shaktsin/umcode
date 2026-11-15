//go:build unix

package workbench

import (
	"os"
	"syscall"
)

func linkCount(info os.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Nlink), true
}
