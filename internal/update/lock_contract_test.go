package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Kontrakt blokady aktualizacji (U2, od v0.8.0) między Spawner a
// deploy/update-bridge.sh. Skryptu nie da się uruchomić w teście (root,
// systemctl), więc test przypina jego tekst: te same ścieżki co w Go i
// kolejność kroków, od której zależy poprawność locka.
func TestUpdateScriptLockContract(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "update-bridge.sh"))
	if err != nil {
		t.Fatalf("read update-bridge.sh: %v", err)
	}
	script := string(b)

	mustContain := func(what, needle string) int {
		t.Helper()
		i := strings.Index(script, needle)
		if i < 0 {
			t.Fatalf("update-bridge.sh: brak %s: %s", what, needle)
		}
		return i
	}

	// Te same pliki co w handlerze (main.go składa je z LockFile/PendingFile).
	mustContain("ścieżki locka instancji", `LOCK="$INSTALL_DIR/`+LockFile+`"`)
	mustContain("ścieżki znacznika startu", `PENDING="$INSTALL_DIR/`+PendingFile+`"`)

	// U5: tag wymaga wiodącego v także przy ręcznym/bezpośrednim wywołaniu.
	mustContain("regexu tagu z wymaganym v", `=~ ^v[0-9]+\.[0-9]+\.[0-9]+`)
	if strings.Contains(script, `=~ ^v?[0-9]`) {
		t.Error("update-bridge.sh nadal przyjmuje tag bez v (^v?)")
	}

	// Token właściciela: walidowany na górze i przekazany przez re-exec do
	// transient unitu (inaczej etap 2 nie wie, którego zlecenia pilnuje).
	tokenCheck := mustContain("walidacji tokenu", `=~ ^[0-9a-f]{32}$`)
	reexec := mustContain("re-execu z tokenem", `"$SELF" "$TAG" "$INSTANCE" ${TOKEN:+"$TOKEN"}`)
	if tokenCheck > reexec {
		t.Error("token musi być walidowany przed re-execem")
	}

	// Kolejność: re-exec → odmowa dla symlinku/nie-pliku → lock instancji
	// (czekając, bo handler trzyma go przez spawn) → token i skasowanie
	// znacznika pod lockiem → lock hosta → dopiero potem cokolwiek zmienia
	// system (systemctl stop).
	notRegular := mustContain("odmowy dla symlinku/nie-pliku", `[ -L "$LOCK" ] || [ ! -f "$LOCK" ]`)
	open9 := mustContain("otwarcia locka bez obcinania", `exec 9>>"$LOCK"`)
	lock9 := mustContain("czekającego locka instancji", `flock -w 10 9`)
	stale := mustContain("odmowy dla nieaktualnego zlecenia", `!= "$TOKEN"`)
	rmPending := mustContain("skasowania znacznika", `rm -f "$PENDING"`)
	open8 := mustContain("locka hosta w /run", `exec 8>>/run/print-bridge-update.lock`)
	lock8 := mustContain("czekającego locka hosta", `flock -w 600 8`)
	stop := mustContain("zatrzymania serwisu", `systemctl stop "$SERVICE"`)
	order := []struct {
		name string
		at   int
	}{
		{"re-exec", reexec}, {"odmowa symlink/nie-plik", notRegular}, {"exec 9>>", open9},
		{"flock -w 10 9", lock9}, {"sprawdzenie tokenu", stale}, {"rm znacznika", rmPending},
		{"exec 8>>", open8}, {"flock -w 600 8", lock8}, {"systemctl stop", stop},
	}
	for i := 1; i < len(order); i++ {
		if order[i-1].at > order[i].at {
			t.Errorf("update-bridge.sh: %q musi być przed %q", order[i-1].name, order[i].name)
		}
	}
	if strings.Contains(script, "flock -n 9") {
		t.Error("lock instancji musi czekać (flock -w): handler trzyma go przez spawn")
	}
}

func TestUpdateScriptBashSyntax(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("brak bash")
	}
	out, err := exec.Command(bash, "-n", filepath.Join(repoRoot(t), "deploy", "update-bridge.sh")).CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n update-bridge.sh: %v\n%s", err, out)
	}
}

// lockBlock wycina z update-bridge.sh blok U2 (od LOCK= do nagłówka „start”)
// i podmienia ścieżkę locka hosta oraz timeouty na testowe.
func lockBlock(t *testing.T, hostLock string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "update-bridge.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	from := strings.Index(script, `LOCK="$INSTALL_DIR/`)
	to := strings.Index(script, `echo "=== $(date -Is) update-bridge.sh start`)
	if from < 0 || to < from {
		t.Fatal("update-bridge.sh: nie znaleziono bloku locka")
	}
	return strings.NewReplacer(
		"/run/print-bridge-update.lock", hostLock,
		"flock -w 10 9", "flock -w 1 9",
		"flock -w 600 8", "flock -w 1 8",
	).Replace(script[from:to])
}

// Zachowanie bloku locka z update-bridge.sh na prawdziwym bash + flock
// (util-linux; jest na Debianie i w CI, nie ma go na macOS — wtedy skip).
func TestUpdateScriptLockBlockBehaviour(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("brak bash")
	}
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("brak flock (util-linux)")
	}
	const tok = "0123456789abcdef0123456789abcdef"
	type env struct{ dir, data, pending, lock, hostLock string }
	setup := func(t *testing.T) env {
		dir := t.TempDir()
		data := filepath.Join(dir, "data")
		if err := os.Mkdir(data, 0o755); err != nil {
			t.Fatal(err)
		}
		return env{dir, data, filepath.Join(data, "update.pending"), filepath.Join(data, "update.lock"), filepath.Join(dir, "host.lock")}
	}
	run := func(t *testing.T, e env, token string) (string, error) {
		prog := "set -euo pipefail\nINSTALL_DIR=" + e.dir + "\nTOKEN=" + token + "\n" + lockBlock(t, e.hostLock) + "echo REACHED\n"
		out, err := exec.Command(bash, "-c", prog).CombinedOutput()
		return string(out), err
	}

	t.Run("token zgodny: znacznik skasowany, przebieg idzie dalej", func(t *testing.T) {
		e := setup(t)
		if err := os.WriteFile(e.pending, []byte(tok), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := run(t, e, tok)
		if err != nil || !strings.Contains(out, "REACHED") {
			t.Fatalf("err=%v out=%q", err, out)
		}
		if _, err := os.Lstat(e.pending); !os.IsNotExist(err) {
			t.Error("znacznik musi zniknąć po wzięciu locka")
		}
	})
	t.Run("token cudzy: koniec bez zmian, znacznik zostaje", func(t *testing.T) {
		e := setup(t)
		other := "ffffffffffffffffffffffffffffffff"
		if err := os.WriteFile(e.pending, []byte(other), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := run(t, e, tok)
		if err == nil || strings.Contains(out, "REACHED") || !strings.Contains(out, "zlecenie nieaktualne") {
			t.Fatalf("err=%v out=%q", err, out)
		}
		if b, _ := os.ReadFile(e.pending); string(b) != other {
			t.Errorf("cudzy znacznik zmieniony: %q", b)
		}
	})
	t.Run("bez tokenu: znacznika nie dotyka", func(t *testing.T) {
		e := setup(t)
		if err := os.WriteFile(e.pending, []byte(tok), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := run(t, e, ""); err != nil || !strings.Contains(out, "REACHED") {
			t.Fatalf("err=%v out=%q", err, out)
		}
		if _, err := os.Lstat(e.pending); err != nil {
			t.Error("ręczny przebieg nie może kasować znacznika agenta")
		}
	})
	t.Run("lock instancji zajęty: koniec po timeoucie", func(t *testing.T) {
		e := setup(t)
		release := holdLock(t, e.lock)
		defer release()
		out, err := run(t, e, "")
		if err == nil || strings.Contains(out, "REACHED") || !strings.Contains(out, "inna aktualizacja tej instancji w toku") {
			t.Fatalf("err=%v out=%q", err, out)
		}
	})
	t.Run("lock hosta zajęty: czeka, potem koniec bez zmian", func(t *testing.T) {
		e := setup(t)
		release := holdLock(t, e.hostLock)
		defer release()
		out, err := run(t, e, "")
		if err == nil || strings.Contains(out, "REACHED") || !strings.Contains(out, "czekam na aktualizację innej instancji") {
			t.Fatalf("err=%v out=%q", err, out)
		}
	})
	t.Run("lock jako symlink albo FIFO: odmowa", func(t *testing.T) {
		e := setup(t)
		if err := os.Symlink(filepath.Join(e.dir, "cel"), e.lock); err != nil {
			t.Fatal(err)
		}
		if out, err := run(t, e, ""); err == nil || !strings.Contains(out, "nie jest zwykłym plikiem") {
			t.Fatalf("symlink: err=%v out=%q", err, out)
		}
		e2 := setup(t)
		if err := syscall.Mkfifo(e2.lock, 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := run(t, e2, ""); err == nil || !strings.Contains(out, "nie jest zwykłym plikiem") {
			t.Fatalf("fifo: err=%v out=%q", err, out)
		}
	})
}
