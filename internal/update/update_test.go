package update

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestValidateTag(t *testing.T) {
	valid := []string{"v1.2.3", "v0.7.44", "v1.0.0-rc1"}
	for _, tag := range valid {
		if err := ValidateTag(tag); err != nil {
			t.Errorf("ValidateTag(%q) = %v, want nil", tag, err)
		}
	}
	// U5 (od v0.8.0): bez wiodącego v tag przechodził walidację, a pobranie
	// wydania (URL z ${TAG}, tagi release to v*) padało asynchronicznie po 202.
	invalid := []string{"", "1.2.3", "0.7.0", "latest; rm -rf /", "v1", "$(whoami)", "1.2.3 && curl evil"}
	for _, tag := range invalid {
		if err := ValidateTag(tag); !errors.Is(err, ErrInvalidTag) {
			t.Errorf("ValidateTag(%q) = %v, want ErrInvalidTag", tag, err)
		}
	}
}

var hexToken = regexp.MustCompile(`^[0-9a-f]{32}$`)

// testSpawner: katalog danych instancji w TempDir i podstawka za sudo, która
// wypisuje swoje argv na stdout (SpawnUpdater przekierowuje go do logu).
func testSpawner(t *testing.T) *Spawner {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-sudo")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"FAKE-SUDO $@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	if err := os.Mkdir(data, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Spawner{
		Script:      "/usr/local/sbin/update-bridge.sh",
		LogPath:     filepath.Join(data, "update.log"),
		LockPath:    filepath.Join(data, "update.lock"),
		PendingPath: filepath.Join(data, "update.pending"),
		Sudo:        fake,
	}
}

func waitForLog(t *testing.T, path string, re *regexp.Regexp) string {
	t.Helper()
	// Start() jest asynchroniczny — czekaj aż wyjście podstawki trafi do logu.
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, _ := os.ReadFile(path)
		if m := re.FindStringSubmatch(string(b)); m != nil {
			return m[len(m)-1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("log bez %v: %q", re, string(b))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readPending(t *testing.T, s *Spawner) string {
	t.Helper()
	b, err := os.ReadFile(s.PendingPath)
	if err != nil {
		t.Fatalf("znacznik startu: %v", err)
	}
	return string(b)
}

func assertNoPending(t *testing.T, s *Spawner) {
	t.Helper()
	if _, err := os.Lstat(s.PendingPath); !os.IsNotExist(err) {
		t.Errorf("znacznik startu nie powinien istnieć (Lstat err=%v)", err)
	}
}

func assertNoLog(t *testing.T, s *Spawner) {
	t.Helper()
	if _, err := os.Stat(s.LogPath); err == nil {
		t.Error("odrzucone żądanie nie powinno nawet tworzyć logu")
	}
}

func assertStartError(t *testing.T, err error, reason string) {
	t.Helper()
	var se *StartError
	if !errors.As(err, &se) || se.Reason != reason {
		t.Fatalf("err = %v, want StartError{%s}", err, reason)
	}
}

// holdLock bierze flock z OSOBNEGO opisu pliku — tak jak update-bridge.sh.
// flock dotyczy opisu otwartego pliku, więc koliduje także w tym samym procesie.
func holdLock(t *testing.T, path string) func() {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock: %v", err)
	}
	return func() { f.Close() }
}

func lockIsFree(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}

func TestSpawnerRunsViaSudoAndLogs(t *testing.T) {
	s := testSpawner(t)
	if err := s.Start("v1.2.3"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// argv: -n <skrypt> <tag> <instancja> <token>; instancja podstawowa = "".
	token := waitForLog(t, s.LogPath, regexp.MustCompile(`FAKE-SUDO -n /usr/local/sbin/update-bridge\.sh v1\.2\.3  ([0-9a-f]{32})\n`))
	if got := readPending(t, s); got != token {
		t.Errorf("znacznik = %q, a skrypt dostał token %q", got, token)
	}
	b, _ := os.ReadFile(s.LogPath)
	if !strings.Contains(string(b), "spawn updater tag=v1.2.3") {
		t.Errorf("log bez nagłówka spawnu: %q", b)
	}
	if !lockIsFree(t, s.LockPath) {
		t.Error("po Start lock musi być wolny (bierze go skrypt)")
	}
}

func TestSpawnerAppendsInstance(t *testing.T) {
	s := testSpawner(t)
	s.Instance = "2"
	if err := s.Start("v1.2.3"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForLog(t, s.LogPath, regexp.MustCompile(`FAKE-SUDO -n /usr/local/sbin/update-bridge\.sh v1\.2\.3 2 ([0-9a-f]{32})\n`))
}

func TestSpawnerRejectsBadTagBeforeSpawning(t *testing.T) {
	for _, tag := range []string{"latest; rm -rf /", "1.2.3"} {
		s := testSpawner(t)
		if err := s.Start(tag); !errors.Is(err, ErrInvalidTag) {
			t.Fatalf("Start(%q) = %v, want ErrInvalidTag", tag, err)
		}
		assertNoLog(t, s)
		assertNoPending(t, s)
	}
}

func TestSpawnerRejectsBadInstance(t *testing.T) {
	s := testSpawner(t)
	s.Instance = "../evil"
	if err := s.Start("v1.2.3"); !errors.Is(err, ErrInvalidInstance) {
		t.Fatalf("err = %v, want ErrInvalidInstance", err)
	}
	if strings.Contains(ErrInvalidInstance.Error(), "evil") {
		t.Error("komunikat błędu instancji ma być stały (bez wartości)")
	}
	assertNoLog(t, s)
	assertNoPending(t, s)
}

// U2: świeży znacznik = poprzedni Start właśnie spawnął aktualizator, który
// jeszcze nie wziął locka.
func TestSpawnerFreshPendingIsInProgress(t *testing.T) {
	s := testSpawner(t)
	if err := os.WriteFile(s.PendingPath, []byte("0123456789abcdef0123456789abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Start("v1.2.3"); !errors.Is(err, ErrInProgress) {
		t.Fatalf("err = %v, want ErrInProgress", err)
	}
	assertNoLog(t, s)
	if got := readPending(t, s); got != "0123456789abcdef0123456789abcdef" {
		t.Errorf("cudzy znacznik zmieniony: %q", got)
	}
}

// Znacznik starszy niż TTL = aktualizator, który nigdy nie wystartował — nie
// blokuje na zawsze; nowy Start przejmuje go nowym tokenem.
func TestSpawnerStalePendingIsReplaced(t *testing.T) {
	s := testSpawner(t)
	if err := os.WriteFile(s.PendingPath, []byte("stary"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * DefaultPendingTTL)
	if err := os.Chtimes(s.PendingPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Start("v1.2.3"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := readPending(t, s); !hexToken.MatchString(got) {
		t.Errorf("znacznik = %q, want nowy token", got)
	}
}

// U2: lock trzyma update-bridge.sh przez cały przebieg — także po restarcie
// agenta w trakcie aktualizacji.
func TestSpawnerHeldLockIsInProgress(t *testing.T) {
	s := testSpawner(t)
	release := holdLock(t, s.LockPath)
	defer release()
	if err := s.Start("v1.2.3"); !errors.Is(err, ErrInProgress) {
		t.Fatalf("err = %v, want ErrInProgress", err)
	}
	assertNoLog(t, s)
	assertNoPending(t, s)
}

// startWithin: Start, który zawiesiłby się na open() (FIFO), czerwieni test
// zamiast go wieszać.
func startWithin(t *testing.T, s *Spawner, tag string) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.Start(tag) }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("Start zawisł (open bez O_NONBLOCK na FIFO?)")
		return nil
	}
}

func TestSpawnerLockMustBeRegularFile(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		s := testSpawner(t)
		target := filepath.Join(t.TempDir(), "cel")
		if err := os.WriteFile(target, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, s.LockPath); err != nil {
			t.Fatal(err)
		}
		assertStartError(t, startWithin(t, s, "v1.2.3"), ReasonLockUnavailable)
		assertNoPending(t, s)
	})
	t.Run("fifo", func(t *testing.T) {
		s := testSpawner(t)
		if err := syscall.Mkfifo(s.LockPath, 0o644); err != nil {
			t.Fatal(err)
		}
		assertStartError(t, startWithin(t, s, "v1.2.3"), ReasonLockUnavailable)
		assertNoPending(t, s)
	})
}

// Świeży znacznik, który nie jest zwykłym plikiem, to nie „aktualizacja w
// toku”, tylko problem z katalogiem danych → 500, nie 409.
func TestSpawnerPendingMustBeRegularFile(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		s := testSpawner(t)
		target := filepath.Join(t.TempDir(), "cel")
		if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, s.PendingPath); err != nil {
			t.Fatal(err)
		}
		assertStartError(t, startWithin(t, s, "v1.2.3"), ReasonLockUnavailable)
	})
	t.Run("fifo", func(t *testing.T) {
		s := testSpawner(t)
		if err := syscall.Mkfifo(s.PendingPath, 0o644); err != nil {
			t.Fatal(err)
		}
		assertStartError(t, startWithin(t, s, "v1.2.3"), ReasonLockUnavailable)
	})
}

func TestSpawnerMissingDataDirIsLockUnavailable(t *testing.T) {
	s := testSpawner(t)
	gone := filepath.Join(t.TempDir(), "brak")
	s.LockPath, s.PendingPath = filepath.Join(gone, "update.lock"), filepath.Join(gone, "update.pending")
	assertStartError(t, s.Start("v1.2.3"), ReasonLockUnavailable)
}

func TestSpawnerUnwritableLogRemovesPending(t *testing.T) {
	s := testSpawner(t)
	s.LogPath = "/nonexistent-dir/update.log"
	err := s.Start("v1.2.3")
	assertStartError(t, err, ReasonLogUnavailable)
	if !strings.Contains(err.Error(), "/nonexistent-dir/update.log") {
		t.Errorf("pełny błąd (do logu agenta) ma nieść ścieżkę: %v", err)
	}
	assertNoPending(t, s)
	if !lockIsFree(t, s.LockPath) {
		t.Error("po błędzie lock musi być wolny")
	}
}

func TestSpawnerSpawnFailureRemovesPending(t *testing.T) {
	s := testSpawner(t)
	s.Sudo = "/nonexistent-dir/sudo"
	assertStartError(t, s.Start("v1.2.3"), ReasonSpawnFailed)
	assertNoPending(t, s)
}

// U2: handler trzyma lock przez CAŁY Start (sprawdzenie, rezerwacja, spawn).
// Pierwszy Start zawisa na open() logu-FIFO bez czytelnika; wtedy lock jest
// zajęty, a drugi Start musi od razu dostać ErrInProgress.
func TestSpawnerHoldsLockDuringStart(t *testing.T) {
	s := testSpawner(t)
	if err := syscall.Mkfifo(s.LogPath, 0o644); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { first <- s.Start("v1.2.3") }()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Lstat(s.PendingPath); err == nil {
			break // pierwszy Start zarezerwował znacznik i czeka na log
		}
		if time.Now().After(deadline) {
			t.Fatal("pierwszy Start nie doszedł do rezerwacji znacznika")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if lockIsFree(t, s.LockPath) {
		t.Error("w trakcie Start lock musi być trzymany przez handler")
	}
	second := make(chan error, 1)
	go func() { second <- s.Start("v1.2.3") }()
	select {
	case err := <-second:
		if !errors.Is(err, ErrInProgress) {
			t.Errorf("drugi Start = %v, want ErrInProgress", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("drugi Start czeka na pierwszy zamiast od razu odpowiedzieć ErrInProgress")
	}

	// Odblokuj pierwszy: czytelnik FIFO; podstawka sudo też pisze do logu.
	r, err := os.Open(s.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, r); r.Close() }()
	if err := <-first; err != nil {
		t.Fatalf("pierwszy Start: %v", err)
	}
}

// Proces pomocniczy w roli update-bridge.sh: bierze flock (czekając), sprawdza
// token, kasuje znacznik, sygnalizuje i trzyma lock do pliku „release”.
func TestHelperUpdateScript(t *testing.T) {
	if os.Getenv("PB_UPDATE_HELPER") != "1" {
		t.Skip("proces pomocniczy — uruchamiany przez podstawkę sudo")
	}
	lockPath, pendingPath := os.Getenv("PB_LOCK"), os.Getenv("PB_PENDING")
	token := os.Args[len(os.Args)-1]
	f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		os.Exit(2)
	}
	deadline := time.Now().Add(10 * time.Second) // jak flock -w 10
	for syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		if time.Now().After(deadline) {
			os.Exit(3)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if b, _ := os.ReadFile(pendingPath); string(b) != token {
		os.Exit(4)
	}
	_ = os.Remove(pendingPath)
	_ = os.WriteFile(os.Getenv("PB_HELD"), nil, 0o644)
	for i := 0; i < 500; i++ {
		if _, err := os.Stat(os.Getenv("PB_RELEASE")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(0)
}

// Między procesami: lock wzięty przez „skrypt” blokuje NOWY Spawner (jak po
// restarcie agenta w trakcie aktualizacji), a po końcu skryptu update znowu
// startuje.
func TestSpawnerScriptLockSurvivesAgentRestart(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s := testSpawner(t)
	dir := filepath.Dir(s.Sudo)
	held, rel := filepath.Join(dir, "held"), filepath.Join(dir, "release")
	if err := os.WriteFile(s.Sudo, []byte("#!/bin/sh\nexec \""+bin+"\" -test.run='^TestHelperUpdateScript$' -- \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PB_UPDATE_HELPER", "1")
	t.Setenv("PB_LOCK", s.LockPath)
	t.Setenv("PB_PENDING", s.PendingPath)
	t.Setenv("PB_HELD", held)
	t.Setenv("PB_RELEASE", rel)

	if err := s.Start("v1.2.3"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, "skrypt wziął lock i skasował znacznik", func() bool { _, err := os.Stat(held); return err == nil })
	assertNoPending(t, s)

	restarted := &Spawner{Script: s.Script, LogPath: s.LogPath, LockPath: s.LockPath, PendingPath: s.PendingPath, Sudo: s.Sudo}
	if err := restarted.Start("v1.2.3"); !errors.Is(err, ErrInProgress) {
		t.Fatalf("w trakcie aktualizacji = %v, want ErrInProgress", err)
	}

	if err := os.WriteFile(rel, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "skrypt zwolnił lock", func() bool { return lockIsFree(t, s.LockPath) })
	if err := restarted.Start("v1.2.3"); err != nil {
		t.Fatalf("po zakończeniu aktualizacji: %v", err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
