package printer

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestSocketReachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	b := &SocketReachability{Addr: ln.Addr().String(), Timeout: time.Second}
	ok, err := b.Reachable(context.Background())
	if err != nil || !ok {
		t.Errorf("Reachable on live listener = %v, %v; want true,nil", ok, err)
	}
}

func TestSocketUnreachable(t *testing.T) {
	b := &SocketReachability{Addr: "127.0.0.1:1", Timeout: 200 * time.Millisecond}
	ok, _ := b.Reachable(context.Background())
	if ok {
		t.Error("Reachable on dead port should be false")
	}
}
