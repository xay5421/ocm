package core

import "testing"

func TestParsePidLine(t *testing.T) {
	cases := []struct {
		in      string
		pid     int
		cmdline string
		ok      bool
	}{
		{"  1234 ssh -N -L 4096:127.0.0.1:4096 host\n", 1234, "ssh -N -L 4096:127.0.0.1:4096 host", true},
		{"500032 \"C:\\Windows\\System32\\OpenSSH\\ssh.exe\" -T -D 59440 \"dev\" sh", 500032,
			"\"C:\\Windows\\System32\\OpenSSH\\ssh.exe\" -T -D 59440 \"dev\" sh", true},
		{"999", 999, "", true}, // pid without command line (Windows: CommandLine may be null)
		{"", 0, "", false},
		{"abc ssh", 0, "", false},
	}
	for _, c := range cases {
		p, ok := parsePidLine(c.in)
		if ok != c.ok || p.pid != c.pid || p.cmdline != c.cmdline {
			t.Errorf("parsePidLine(%q) = %+v, %v; want pid=%d cmdline=%q ok=%v",
				c.in, p, ok, c.pid, c.cmdline, c.ok)
		}
	}
}

func TestMatchTunnel(t *testing.T) {
	procs := []procEntry{
		{pid: 10, cmdline: "ssh -N -o BatchMode=yes -L 4096:127.0.0.1:4096 alpha"},
		{pid: 11, cmdline: "ssh -N -o BatchMode=yes -L 4097:127.0.0.1:4096 beta"},
		{pid: 12, cmdline: "ssh -T -D 59440 gamma sh"},
	}
	if pid, ok := matchTunnel(procs, "4097:127.0.0.1:4096 beta"); !ok || pid != 11 {
		t.Errorf("matchTunnel beta = %d, %v; want 11, true", pid, ok)
	}
	if pid, ok := matchTunnel(procs, "4096:127.0.0.1:4096 alpha"); !ok || pid != 10 {
		t.Errorf("matchTunnel alpha = %d, %v; want 10, true", pid, ok)
	}
	if _, ok := matchTunnel(procs, "5000:127.0.0.1:5000 delta"); ok {
		t.Error("matchTunnel delta matched unexpectedly")
	}
	if _, ok := matchTunnel(nil, "4096:127.0.0.1:4096 alpha"); ok {
		t.Error("matchTunnel on empty list matched unexpectedly")
	}
}
