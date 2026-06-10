package update

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// repoRoot walks up from this test file's directory to the module root (where
// go.mod lives), so the test can read the real deploy/CI artifacts regardless of
// the cwd `go test` runs from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate module root (go.mod) above test file")
	return ""
}

// TestUpdateChecksumFilenameContract locks the contract between release.yml and
// update-bridge.sh that bug #24 broke: release.yml runs `sha256sum "$TARBALL"`,
// embedding the REAL asset filename (print-bridge-<ver>-linux-<arch>.tar.gz) in
// the .sha256 file. `sha256sum -c` re-opens whatever filename it reads FROM that
// file, so the updater MUST save the downloaded tarball under that same name.
// A previous fix downloaded it as a fixed `agent.tar.gz`, so `sha256sum -c` tried
// to open a non-existent print-bridge-*.tar.gz and EVERY legitimate update aborted
// (fail-closed checksum bricked self-update). This test would have caught that.
func TestUpdateChecksumFilenameContract(t *testing.T) {
	root := repoRoot(t)

	scriptBytes, err := os.ReadFile(filepath.Join(root, "deploy", "update-bridge.sh"))
	if err != nil {
		t.Fatalf("read update-bridge.sh: %v", err)
	}
	script := string(scriptBytes)

	relBytes, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release.yml: %v", err)
	}
	release := string(relBytes)

	// 1) CI must generate the checksum from the asset's own filename. If this line
	//    ever changes (e.g. to a fixed name), the contract assertion below must too.
	if !strings.Contains(release, `sha256sum "$TARBALL" > "$TARBALL.sha256"`) {
		t.Errorf("release.yml no longer generates checksum via `sha256sum \"$TARBALL\"`; "+
			"update-bridge.sh's download filename must stay in sync.\nrelease.yml:\n%s", release)
	}
	if !strings.Contains(release, `TARBALL="print-bridge-${VERSION}-linux-${{ matrix.arch }}.tar.gz"`) {
		t.Errorf("release.yml asset naming changed; verify update-bridge.sh $ASSET still matches")
	}

	// 2) The updater must NOT download the tarball under a fixed name that differs
	//    from the checksum's embedded filename. `agent.tar.gz` is the exact name
	//    that regressed #24 — its presence as the curl/-o target (or as the
	//    sha256sum -c argument) reintroduces the brick.
	if regexp.MustCompile(`-o\s+"\$TMP/agent\.tar\.gz"`).MatchString(script) {
		t.Error("update-bridge.sh downloads tarball as fixed agent.tar.gz; " +
			"sha256sum -c reads the REAL asset name from the .sha256 file and will fail to open it (#24 regression)")
	}
	if regexp.MustCompile(`sha256sum\s+-c\s+"?agent\.tar\.gz\.sha256"?`).MatchString(script) {
		t.Error("update-bridge.sh verifies against agent.tar.gz.sha256; the checksum file embeds the real asset name, not agent.tar.gz (#24 regression)")
	}

	// 3) The updater must save the tarball under $ASSET (the real release name) so
	//    the filename inside the .sha256 file resolves.
	if !regexp.MustCompile(`-o\s+"\$TMP/\$ASSET"`).MatchString(script) {
		t.Error("update-bridge.sh must download the tarball as \"$TMP/$ASSET\" so sha256sum -c can locate it")
	}
	if !regexp.MustCompile(`sha256sum\s+-c\s+"\$\{ASSET\}\.sha256"`).MatchString(script) {
		t.Error("update-bridge.sh must run sha256sum -c against \"${ASSET}.sha256\"")
	}

	// 4) Fail-closed guarantees: no `|| true` masking on an actual command line and
	//    strict bash mode. Scan only non-comment lines so prose mentioning `|| true`
	//    in an explanatory comment doesn't trip this.
	for _, line := range strings.Split(script, "\n") {
		code := strings.TrimSpace(line)
		if code == "" || strings.HasPrefix(code, "#") {
			continue
		}
		if strings.Contains(code, "|| true") {
			t.Errorf("update-bridge.sh masks a failure with `|| true` on a command line (fail-closed required by #24): %q", code)
		}
	}
	if !strings.Contains(script, "set -euo pipefail") {
		t.Error("update-bridge.sh must use `set -euo pipefail` so a checksum mismatch aborts before install")
	}
}

// TestUpdaterRollbackContract locks the fail-safe shape of update-bridge.sh.
// The old script ran `systemctl stop` -> `install` -> `systemctl start` with
// set -e and NO backup: any failure after the stop (failed install, new binary
// crashing on boot, health check timeout) left the box with the service DOWN
// and the old binary already overwritten — a remote self-update could brick
// the agent until someone ssh-ed in. The updater must:
//  1. back up the current binary BEFORE stopping the service,
//  2. roll back + restart via an EXIT trap on any failure after the stop,
//  3. verify health against the exact `"version":"X"` JSON field (an
//     unanchored `grep 1.0.0` matches dots as wildcards / substrings),
//  4. fast-fail the 30 s health loop when systemd already reports `failed`.
func TestUpdaterRollbackContract(t *testing.T) {
	root := repoRoot(t)
	scriptBytes, err := os.ReadFile(filepath.Join(root, "deploy", "update-bridge.sh"))
	if err != nil {
		t.Fatalf("read update-bridge.sh: %v", err)
	}
	script := string(scriptBytes)

	// 1) Backup before stop (and the backup must be the running binary).
	// The stop COMMAND is anchored at line start — the cgroup-escape comment
	// higher up mentions `systemctl stop print-bridge` in prose.
	backupIdx := strings.Index(script, `cp -f "$BIN" "$BAK"`)
	stopIdx := -1
	if loc := regexp.MustCompile(`(?m)^systemctl stop print-bridge`).FindStringIndex(script); loc != nil {
		stopIdx = loc[0]
	}
	if backupIdx == -1 {
		t.Error(`update-bridge.sh must back up the current binary: cp -f "$BIN" "$BAK"`)
	}
	if stopIdx == -1 {
		t.Error("update-bridge.sh must stop the service before replacing the binary")
	}
	if backupIdx != -1 && stopIdx != -1 && backupIdx > stopIdx {
		t.Error("the binary backup must happen BEFORE systemctl stop (a failure between stop and backup would lose the last-known-good binary)")
	}

	// 2) EXIT trap that restores the backup and restarts the service.
	if !regexp.MustCompile(`trap\s+\S*rollback\S*\s+EXIT`).MatchString(script) {
		t.Error("update-bridge.sh must register a rollback EXIT trap (any post-stop failure must restore the old binary)")
	}
	if !strings.Contains(script, `install -m 0755 "$BAK" "$BIN"`) {
		t.Error(`the rollback path must reinstall the backup: install -m 0755 "$BAK" "$BIN"`)
	}

	// 3) Health check anchored to the version JSON field, fixed-string match.
	if !strings.Contains(script, `grep -qF "\"version\":\"${TAG#v}\""`) {
		t.Error(`health verification must use grep -qF "\"version\":\"${TAG#v}\"" (unanchored grep treats dots as wildcards and matches substrings)`)
	}
	// 3a) ...and the health curl must NOT use -f: /health returns 503 when
	// DEGRADED (printer off, cupsd down) while still carrying the version in
	// the body. curl -f discards that body, the grep misses, and the rollback
	// trap reverts a perfectly good binary just because the printer was
	// offline during the update window. Update verification asserts "new
	// binary runs and reports its version", not "printer healthy".
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "api/v1/health") && regexp.MustCompile(`curl\s+-[a-z]*f`).MatchString(line) {
			t.Errorf("the /health verification curl must not use -f (503-degraded discards the body and triggers a false rollback): %q", strings.TrimSpace(line))
		}
	}

	// 4) Fast-fail when the new binary is already dead instead of polling 30 s.
	if !strings.Contains(script, "systemctl is-failed") {
		t.Error("the health loop must fast-fail on `systemctl is-failed` (new binary crashing on start)")
	}
}

// TestSha256VerificationEndToEnd reproduces the real release/update flow end to
// end: it builds a .sha256 file exactly as release.yml does (embedding the real
// asset filename), then runs `sha256sum -c` exactly as update-bridge.sh does
// (against a file saved under that asset name). Asserts PASS for an intact tarball
// and FAIL for a tampered one. This is the empirical regression the Go suite was
// missing — the previous fix passed every Go test yet bricked every update.
func TestSha256VerificationEndToEnd(t *testing.T) {
	sha, err := exec.LookPath("sha256sum")
	if err != nil {
		t.Skip("sha256sum not available; skipping end-to-end checksum test")
	}

	const asset = "print-bridge-1.2.3-linux-amd64.tar.gz"
	dir := t.TempDir()
	assetPath := filepath.Join(dir, asset)
	sumPath := assetPath + ".sha256"

	payload := []byte("pretend this is a gzip tarball\n")
	if err := os.WriteFile(assetPath, payload, 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	// Mirror release.yml: `sha256sum "$TARBALL" > "$TARBALL.sha256"`.
	sum := sha256.Sum256(payload)
	// GNU coreutils format: "<hex>  <filename>" (two spaces). The filename is the
	// bare asset name (as release.yml writes it, since it runs in the asset's dir).
	sumLine := hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	if err := os.WriteFile(sumPath, []byte(sumLine), 0o644); err != nil {
		t.Fatalf("write sha256: %v", err)
	}

	// Mirror update-bridge.sh: `(cd "$TMP" && sha256sum -c "${ASSET}.sha256")`.
	runCheck := func() *exec.Cmd {
		cmd := exec.Command(sha, "-c", asset+".sha256")
		cmd.Dir = dir
		return cmd
	}

	// Intact tarball under its real asset name -> verification MUST pass.
	if out, err := runCheck().CombinedOutput(); err != nil {
		t.Fatalf("sha256sum -c failed for intact tarball saved as $ASSET (this is the #24 brick): %v\n%s", err, out)
	}

	// Tampered tarball -> verification MUST fail (fail-closed still works).
	if err := os.WriteFile(assetPath, append(payload, 'X'), 0o644); err != nil {
		t.Fatalf("tamper asset: %v", err)
	}
	if out, err := runCheck().CombinedOutput(); err == nil {
		t.Fatalf("sha256sum -c PASSED for tampered tarball; fail-closed checksum is broken\n%s", out)
	}
}
