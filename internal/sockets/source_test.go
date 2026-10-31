package sockets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeProc builds <root>/<pid>/net/<table> from the testdata fixtures.
func fakeProc(t *testing.T, pid string, tables ...string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, pid, "net")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range tables {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestProcSourceCollect(t *testing.T) {
	requireLittleEndian(t)
	// tcp6 missing: IPv6 disabled must not be an error.
	root := fakeProc(t, "42", "tcp", "udp", "udp6")
	src := ProcSource{Root: root}

	// PID 7 does not exist (exited): the source must fall back to PID 42.
	socks, err := src.Collect(context.Background(), []Target{{NetNS: 99, PIDs: []int{7, 42}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := 4 + 3 + 1; len(socks) != want {
		t.Fatalf("got %d sockets, want %d", len(socks), want)
	}
	for _, s := range socks {
		if s.NetNS != 99 {
			t.Fatalf("socket %+v: NetNS = %d, want 99", s, s.NetNS)
		}
	}
}

func TestProcSourceSkipsVanishedNamespace(t *testing.T) {
	requireLittleEndian(t)
	root := fakeProc(t, "42", "tcp")
	socks, err := ProcSource{Root: root}.Collect(context.Background(), []Target{
		{NetNS: 1, PIDs: []int{42}},
		{NetNS: 2, PIDs: []int{500, 501}}, // all gone
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(socks) != 4 {
		t.Fatalf("got %d sockets, want 4", len(socks))
	}
}

func TestProcSourceNothingReadable(t *testing.T) {
	_, err := ProcSource{Root: t.TempDir()}.Collect(context.Background(), []Target{{NetNS: 1, PIDs: []int{1}}})
	if !errors.Is(err, ErrNoSockets) {
		t.Fatalf("err = %v, want ErrNoSockets", err)
	}
}

func TestProcSourceCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ProcSource{Root: t.TempDir()}.Collect(ctx, []Target{{NetNS: 1, PIDs: []int{1}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
