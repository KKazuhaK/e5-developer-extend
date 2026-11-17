//go:build !linux && !windows

package ops

import (
	"os"
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
	// A fixed locale and time zone, so the text is the same whoever reads it
	// (the restart helper in gui/shared.go runs ps the same way).
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(out)), " ")
}
