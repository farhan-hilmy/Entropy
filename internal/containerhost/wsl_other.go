//go:build !windows

package containerhost

import "context"

// wslAddresses: WSL exists only on Windows.
func wslAddresses(context.Context) []string { return nil }
