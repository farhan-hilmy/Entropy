//go:build windows

package containerhost

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var inetPattern = regexp.MustCompile(`inet (\d+\.\d+\.\d+\.\d+)/`)

// wslAddresses returns the IP of the WSL VM, where Podman on Windows usually
// publishes ports. Only running distributions are asked: running a command in
// a stopped one would boot it. All WSL 2 distributions share one VM, so the
// first answer is enough.
func wslAddresses(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := wsl(ctx, "--list", "--running", "--quiet")
	if err != nil {
		return nil
	}
	for _, name := range strings.Fields(out) {
		addr, err := wsl(ctx, "--distribution", name, "--exec", "ip", "-4", "-o", "addr", "show", "eth0")
		if err != nil {
			continue
		}
		if m := inetPattern.FindStringSubmatch(addr); m != nil {
			return []string{m[1]}
		}
	}
	return nil
}

func wsl(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "wsl.exe", args...)
	// wsl.exe writes UTF-16 unless told otherwise; older versions ignore
	// WSL_UTF8, and dropping the zero bytes covers them for ASCII names.
	cmd.Env = append(os.Environ(), "WSL_UTF8=1")
	out, err := cmd.Output()
	out = bytes.ReplaceAll(out, []byte{0}, nil)
	out = bytes.TrimPrefix(out, []byte("\xff\xfe"))
	return string(out), err
}
