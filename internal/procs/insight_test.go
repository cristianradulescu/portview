package procs

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInsightFakeTree(t *testing.T) {
	root := buildProc(t, []fakeProcess{
		{pid: 1, status: "Name:\tsystemd\nPPid:\t0\nUid:\t0\t0\t0\t0\n", comm: "systemd"},
		{pid: 700, status: "Name:\tcontainerd-shim\nPPid:\t1\nUid:\t0\t0\t0\t0\n", comm: "containerd-shim"},
		{pid: 812, status: "Name:\tnginx\nPPid:\t700\nUid:\t0\t0\t0\t0\nVmRSS:\t   12288 kB\nCapEff:\t000001ffffffffff\n", comm: "nginx"},
		{pid: 813, status: "Name:\tnginx\nPPid:\t812\nUid:\t33\t33\t33\t33\n", comm: "nginx"},
		{pid: 814, status: "Name:\tnginx\nPPid:\t812\nUid:\t33\t33\t33\t33\n", comm: "nginx"},
	})
	pdir := filepath.Join(root, "812")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// utime 150, stime 50 (fields 14, 15)
	must(os.WriteFile(filepath.Join(pdir, "stat"), []byte("812 (nginx) S 700 812 812 0 -1 4194560 0 0 0 0 150 50 0 0 20 0 1 0 250 1000 100\n"), 0o644))
	must(os.WriteFile(filepath.Join(pdir, "limits"), []byte("Limit                     Soft Limit           Hard Limit           Units\nMax open files            1024                 524288               files\n"), 0o644))
	must(os.MkdirAll(filepath.Join(pdir, "task", "812"), 0o755))
	must(os.MkdirAll(filepath.Join(pdir, "task", "820"), 0o755))
	must(os.WriteFile(filepath.Join(pdir, "task", "812", "children"), []byte("814 813 "), 0o644))
	must(os.WriteFile(filepath.Join(pdir, "task", "820", "children"), []byte("813 "), 0o644)) // seen twice
	must(os.MkdirAll(filepath.Join(pdir, "fd"), 0o755))
	must(os.Symlink("socket:[801]", filepath.Join(pdir, "fd", "3")))
	must(os.Symlink("/dev/null", filepath.Join(pdir, "fd", "4")))
	must(os.MkdirAll(filepath.Join(pdir, "ns"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "self", "ns"), 0o755))
	for t, ino := range map[string][2]string{"net": {"4026532000", "4026531840"}, "mnt": {"4026532001", "4026531841"}, "user": {"4026531837", "4026531837"}} {
		must(os.Symlink(t+":["+ino[0]+"]", filepath.Join(pdir, "ns", t)))
		must(os.Symlink(t+":["+ino[1]+"]", filepath.Join(root, "self", "ns", t)))
	}

	d, err := Scanner{Root: root}.Details(812)
	if err != nil {
		t.Fatal(err)
	}
	in := d.Insight
	if in.RSS != 12288*1024 || in.CPUTicks != 200 || !in.CapsKnown || !AllCaps(in.CapEff) || in.MaxFDs != 1024 {
		t.Errorf("insight = %+v", in)
	}
	if in.FDs != 2 {
		t.Errorf("FDs = %d, want 2", in.FDs)
	}
	var anc []string
	for _, a := range in.Ancestors {
		anc = append(anc, a.Comm)
	}
	if strings.Join(anc, ",") != "containerd-shim,systemd" {
		t.Errorf("ancestors = %v", in.Ancestors)
	}
	if len(in.Children) != 2 || in.Children[0].PID != 813 || in.Children[1].PID != 814 || in.Children[0].Comm != "nginx" {
		t.Errorf("children = %+v", in.Children)
	}
	got := map[string]bool{}
	for _, ns := range in.Namespaces {
		got[ns.Type] = ns.Shared
	}
	if len(got) != 3 || got["net"] || got["mnt"] || !got["user"] || in.NSErr != nil {
		t.Errorf("namespaces = %+v, err %v", in.Namespaces, in.NSErr)
	}
}

func TestInsightSelf(t *testing.T) {
	d, err := Scanner{}.Details(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	in := d.Insight
	if in.RSS == 0 || in.FDs <= 0 || in.MaxFDs == 0 || !in.CapsKnown || len(in.Ancestors) == 0 {
		t.Errorf("insight of self = %+v", in)
	}
	if in.NSErr != nil || len(in.Namespaces) == 0 {
		t.Fatalf("namespaces: %v %v", in.Namespaces, in.NSErr)
	}
	for _, ns := range in.Namespaces {
		if !ns.Shared {
			t.Errorf("own %s namespace differs from /proc/self's", ns.Type)
		}
	}
}

func TestCapNames(t *testing.T) {
	if got := strings.Join(CapNames(1<<CapNetBindService|1<<13), ","); got != "net_bind_service,net_raw" {
		t.Errorf("CapNames = %s", got)
	}
	if got := CapNames(1 << 50); len(got) != 1 || got[0] != "cap50" {
		t.Errorf("unknown bit = %v", got)
	}
	if !AllCaps(0x1ffffffffff) || AllCaps(0x400) {
		t.Error("AllCaps")
	}
	if parseMaxOpenFiles([]byte("Max open files            unlimited            unlimited            files\n")) != math.MaxUint64 {
		t.Error("unlimited")
	}
}
