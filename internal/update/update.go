// Package update validates a release tag and spawns a detached updater script.
// The updater must NOT be a child of the service: `systemctl restart` would kill
// the whole process tree. Setpgid detaches it (mirrors subiekt-bridge PS -Detached).
package update

import (
	"errors"
	"os/exec"
	"regexp"
	"syscall"
)

var tagRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+([-.][0-9A-Za-z]+)*$`)

func ValidateTag(tag string) error {
	if !tagRe.MatchString(tag) {
		return errors.New("invalid release tag (expected semver like v1.2.3)")
	}
	return nil
}

// SpawnUpdater validates the tag then launches scriptPath detached.
func SpawnUpdater(scriptPath, tag string) error {
	if err := ValidateTag(tag); err != nil {
		return err
	}
	cmd := exec.Command("/bin/sh", scriptPath, tag)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}
