package tlsgen

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// Nieudany zapis klucza nie może zostawić na dysku samego certa: EnsureCert
// sprawdza tylko ISTNIENIE plików, więc osierocony cert + brakujący/ucięty
// klucz przechodzi check, ListenAndServeTLS pada i serwis wpada w pętlę
// restartów bez samonaprawy.
func TestEnsureCertKeyWriteFailureLeavesNoOrphanCert(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	key := filepath.Join(dir, "key.pem")

	// Katalog pod ścieżką klucza wymusza błąd zapisu klucza.
	if err := os.Mkdir(key, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCert(cert, key, []string{"localhost"}); err == nil {
		t.Fatal("EnsureCert musi zwrócić błąd, gdy zapis klucza się nie uda")
	}
	if _, err := os.Stat(cert); err == nil {
		t.Fatal("po nieudanym zapisie klucza cert.pem nie może zostać na dysku (kolejny start nigdy nie zregeneruje pary)")
	}

	// Po usunięciu przeszkody kolejny start regeneruje kompletną, działającą parę.
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCert(cert, key, []string{"localhost"}); err != nil {
		t.Fatalf("EnsureCert po naprawieniu ścieżki: %v", err)
	}
	if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
		t.Fatalf("zregenerowana para nie laduje się: %v", err)
	}
}

func TestEnsureCertCreatesAndReuses(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	key := filepath.Join(dir, "key.pem")

	if err := EnsureCert(cert, key, []string{"localhost", "127.0.0.1"}); err != nil {
		t.Fatalf("EnsureCert: %v", err)
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	hasLocalhost := false
	for _, d := range leaf.DNSNames {
		if d == "localhost" {
			hasLocalhost = true
		}
	}
	if !hasLocalhost {
		t.Errorf("SAN DNS missing 'localhost': %v", leaf.DNSNames)
	}
	hasLoopback := false
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.ParseIP("127.0.0.1")) {
			hasLoopback = true
		}
	}
	if !hasLoopback {
		t.Errorf("SAN IP missing 127.0.0.1: %v", leaf.IPAddresses)
	}

	info, _ := os.Stat(key)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key perms = %v, want 0600", info.Mode().Perm())
	}

	// Reuse: second call must not regenerate (same serial).
	before := leaf.SerialNumber.String()
	if err := EnsureCert(cert, key, []string{"localhost", "127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	pair2, _ := tls.LoadX509KeyPair(cert, key)
	leaf2, _ := x509.ParseCertificate(pair2.Certificate[0])
	if leaf2.SerialNumber.String() != before {
		t.Error("EnsureCert regenerated an existing cert (should reuse)")
	}
}
