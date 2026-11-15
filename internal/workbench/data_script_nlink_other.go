//go:build !unix

package workbench

import "os"

func linkCount(os.FileInfo) (uint64, bool) { return 0, false }
