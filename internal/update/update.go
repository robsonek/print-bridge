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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"syscall"
	"time"
)

// U5 (od v0.8.0): wymagany wiodący v — URL pobrania w update-bridge.sh używa
// ${TAG}, a tagi wydań to v*; bez v walidacja przechodziła (202), a pobranie
// padało asynchronicznie.
var tagRe = regexp.MustCompile(`^v\d+\.\d+\.\d+([-.][0-9A-Za-z]+)*$`)
var instanceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Pliki blokady w katalogu danych instancji (względem katalogu binarki — jak
// data/update.log). update-bridge.sh używa tych samych ścieżek
// (lock_contract_test.go).
const (
	LockFile    = "data/update.lock"
	PendingFile = "data/update.pending"
)

// DefaultPendingTTL: po tym czasie znacznik startu uznaje się za porzucony
// (aktualizator nie wystartował, np. sudo odmówiło) i nie blokuje kolejnych
// prób. Okno spawn → flock skryptu trwa normalnie ~0,5–2 s.
const DefaultPendingTTL = 60 * time.Second

// Błędy WEJŚCIA — klient może poprawić żądanie (handler: 422 INVALID_REQUEST).
// Teksty stałe, bez ścieżek i wartości.
var (
	ErrInvalidTag      = errors.New("invalid release tag (expected v-prefixed semver like v1.2.3)")
	ErrInvalidInstance = errors.New("invalid instance (expected slug [a-z0-9-])")
)

// ErrInProgress — aktualizacja tej instancji już trwa albo właśnie startuje
// (handler: 409 UPDATE_IN_PROGRESS).
var ErrInProgress = errors.New("update already in progress")

// Powody StartError (details.reason w UPDATE_FAILED).
const (
	ReasonLogUnavailable  = "log_unavailable"
	ReasonLockUnavailable = "lock_unavailable"
	ReasonSpawnFailed     = "spawn_failed"
)

// StartError — aktualizator nie wystartował z winy agenta (handler: 500
// UPDATE_FAILED). Err niesie szczegół ze ścieżkami — tylko do logu agenta.
type StartError struct {
	Reason string
	Err    error
}

func (e *StartError) Error() string {
	return "updater not started (" + e.Reason + "): " + e.Err.Error()
}
func (e *StartError) Unwrap() error { return e.Err }

func ValidateTag(tag string) error {
	if !tagRe.MatchString(tag) {
		return ErrInvalidTag
	}
	return nil
}

// Spawner uruchamia aktualizator JEDNEJ instancji agenta i odmawia drugiej
// równoległej aktualizacji (U2, od v0.8.0).
//
// Jeden punkt synchronizacji: flock na LockPath. update-bridge.sh trzyma go
// przez cały przebieg (przeżywa restart agenta). Start trzyma go przez
// sprawdzenie, rezerwację znacznika i spawn; znacznik PendingPath (z tokenem
// właściciela) pokrywa okno między zwolnieniem locka przez Start a wzięciem go
// przez skrypt (sudo zamyka FD ≥ 3, a systemd-run nie przekazuje FD do
// transient unitu — locka nie da się skryptowi przekazać). Każda operacja na
// znaczniku — tu i w skrypcie — dzieje się pod lockiem. Osobne otwarcie pliku
// per Start sprawia, że równoległe wywołania w jednym procesie też się
// wykluczają (flock dotyczy opisu otwartego pliku).
type Spawner struct {
	Script      string // update-bridge.sh (ścieżka z sudoers)
	LogPath     string // data/update.log
	LockPath    string // data/update.lock
	PendingPath string // data/update.pending
	Instance    string // slug; "" = instancja podstawowa
	// Sudo: binarka sudo; "" = "sudo" (testy podstawiają atrapę).
	Sudo string
	// PendingTTL: 0 = DefaultPendingTTL.
	PendingTTL time.Duration
}

// Start waliduje wejście, rezerwuje start pod lockiem i uruchamia
// update-bridge.sh odłączony, przez `sudo -n`, z wyjściem dopisywanym do logu.
// argv skryptu: <tag> <instancja albo ""> <token>.
func (s *Spawner) Start(tag string) error {
	if err := ValidateTag(tag); err != nil {
		return err
	}
	if s.Instance != "" && !instanceRe.MatchString(s.Instance) {
		return ErrInvalidInstance
	}

	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()

	token, err := s.reserve()
	if err != nil {
		return err
	}

	logf, err := os.OpenFile(s.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		s.dropPending()
		return &StartError{ReasonLogUnavailable, fmt.Errorf("updater log %s: %w", s.LogPath, err)}
	}
	// The child inherits a dup of the fd at Start(); the parent's copy can be
	// closed right after.
	defer logf.Close()
	fmt.Fprintf(logf, "=== %s spawn updater tag=%s instance=%q script=%s\n",
		time.Now().Format(time.RFC3339), tag, s.Instance, s.Script)

	sudo := s.Sudo
	if sudo == "" {
		sudo = "sudo"
	}
	cmd := exec.Command(sudo, "-n", s.Script, tag, s.Instance, token)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		s.dropPending()
		return &StartError{ReasonSpawnFailed, fmt.Errorf("start %s: %w", sudo, err)}
	}
	return nil
}

// lock bierze flock na LockPath bez czekania. Otwarcie z O_NONBLOCK (FIFO nie
// zawiesza open) i O_NOFOLLOW; tylko zwykły plik. os.OpenFile dodaje
// O_CLOEXEC, więc sudo nie dziedziczy locka.
func (s *Spawner) lock() (func(), error) {
	f, err := os.OpenFile(s.LockPath, os.O_RDONLY|os.O_CREATE|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return nil, &StartError{ReasonLockUnavailable, fmt.Errorf("open %s: %w", s.LockPath, err)}
	}
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, &StartError{ReasonLockUnavailable, fmt.Errorf("%s: not a regular file (stat err=%v)", s.LockPath, err)}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrInProgress
		}
		return nil, &StartError{ReasonLockUnavailable, fmt.Errorf("flock %s: %w", s.LockPath, err)}
	}
	return func() { f.Close() }, nil // zamknięcie zwalnia flock
}

// reserve (pod lockiem): świeży znacznik = aktualizator właśnie startuje;
// przeterminowany jest zastępowany. Zwraca token zapisany w nowym znaczniku.
func (s *Spawner) reserve() (string, error) {
	ttl := s.PendingTTL
	if ttl <= 0 {
		ttl = DefaultPendingTTL
	}
	switch st, err := os.Lstat(s.PendingPath); {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return "", &StartError{ReasonLockUnavailable, fmt.Errorf("lstat %s: %w", s.PendingPath, err)}
	case !st.Mode().IsRegular():
		return "", &StartError{ReasonLockUnavailable, fmt.Errorf("%s: not a regular file", s.PendingPath)}
	case time.Since(st.ModTime()) < ttl:
		return "", ErrInProgress
	default:
		if err := os.Remove(s.PendingPath); err != nil {
			return "", &StartError{ReasonLockUnavailable, fmt.Errorf("remove stale %s: %w", s.PendingPath, err)}
		}
	}

	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", &StartError{ReasonLockUnavailable, fmt.Errorf("token: %w", err)}
	}
	token := hex.EncodeToString(raw)
	f, err := os.OpenFile(s.PendingPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return "", &StartError{ReasonLockUnavailable, fmt.Errorf("create %s: %w", s.PendingPath, err)}
	}
	_, werr := f.WriteString(token)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		s.dropPending()
		return "", &StartError{ReasonLockUnavailable, fmt.Errorf("write %s: %w", s.PendingPath, werr)}
	}
	return token, nil
}

// dropPending (pod lockiem — znacznik na pewno nasz) sprząta po nieudanym
// starcie, żeby nie blokował kolejnej próby przez TTL.
func (s *Spawner) dropPending() { _ = os.Remove(s.PendingPath) }
