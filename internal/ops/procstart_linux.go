//go:build linux

package ops

import (
	"os"
	"strconv"
	"strings"
)

// procStart is the start time of the process (clock ticks since boot, field 22
// of /proc/<pid>/stat), "" when it cannot be read.
func procStart(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	s := string(b)
	// The command name is in parentheses and may hold spaces: go past the last ")".
	i := strings.LastIndex(s, ") ")
	if i < 0 {
		return ""
	}
	f := strings.Fields(s[i+2:])
	if len(f) < 20 {
		return ""
	}
	return f[19]
}
