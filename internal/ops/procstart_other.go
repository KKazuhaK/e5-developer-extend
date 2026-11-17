//go:build !linux && !windows

package ops

import (
	"os/exec"
	"strconv"
	"strings"
)

// procStart is the start time of the process as ps prints it (with the blanks
// squeezed), "" when it cannot be read.
func procStart(pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(out)), " ")
}
