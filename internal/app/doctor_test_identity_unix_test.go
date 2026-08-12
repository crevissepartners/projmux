//go:build !windows

package app

import "os"

func doctorTestIsRoot() bool { return os.Geteuid() == 0 }
