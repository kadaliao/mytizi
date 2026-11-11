//go:build !windows

package rotlog

import "os"

func openShared(path string) (*os.File, error) { return os.Open(path) }
