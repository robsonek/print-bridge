// Package update validates a release tag and spawns a detached updater script.
// The updater must NOT be a child of the service: `systemctl restart` would kill
// the whole process tree. Setpgid detaches it (mirrors subiekt-bridge PS -Detached).
//
// The updater runs via `sudo -n`: the service runs as the unprivileged
// print-bridge user, while update-bridge.sh needs root (systemctl stop/start,
// installs into /opt and /usr/lib/cups/backend). install-debian.sh provisions
// a sudoers drop-in scoped to the ROOT-OWNED script path
// /usr/local/sbin/update-bridge.sh — the script lives outside /opt precisely
// so the print-bridge user cannot rewrite a file it is allowed to sudo
// (privilege escalation). Without the sudoers entry `sudo -n` fails fast and
// the failure lands in the update log instead of vanishing (the original
// silent-death mode, found on hardware 2026-06-07).
package update

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"syscall"
	"time"
)

var tagRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+([-.][0-9A-Za-z]+)*$`)
var instanceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// sudoBin is a var so tests can substitute a fake sudo.
var sudoBin = "sudo"

func ValidateTag(tag string) error {
	if !tagRe.MatchString(tag) {
		return errors.New("invalid release tag (expected semver like v1.2.3)")
	}
	return nil
}

// SpawnUpdater validates tag (and instance, when set), then launches scriptPath
// detached via `sudo -n`, appending the updater's combined output to logPath. The
// instance slug is forwarded as the script's 2nd arg ONLY when non-empty, so a
// primary-instance update is byte-for-byte the pre-multi-instance invocation.
func SpawnUpdater(scriptPath, logPath, tag, instance string) error {
	if err := ValidateTag(tag); err != nil {
		return err
	}
	if instance != "" && !instanceRe.MatchString(instance) {
		return fmt.Errorf("invalid instance %q (expected slug [a-z0-9-])", instance)
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("updater log %s: %w", logPath, err)
	}
	// The child inherits a dup of the fd at Start(); the parent's copy can be
	// closed right after.
	defer logf.Close()
	fmt.Fprintf(logf, "=== %s spawn updater tag=%s instance=%q script=%s\n",
		time.Now().Format(time.RFC3339), tag, instance, scriptPath)

	args := []string{"-n", scriptPath, tag}
	if instance != "" {
		args = append(args, instance)
	}
	cmd := exec.Command(sudoBin, args...)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}
