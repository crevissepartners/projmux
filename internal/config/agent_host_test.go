package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentDefaultHostStorage(t *testing.T) {
	p := Paths{ConfigDir: t.TempDir()}
	path := p.AgentDefaultHostFile()
	for _, path := range []string{"", path} {
		v, err := LoadAgentDefaultHostFile(path)
		if v != AgentHostTmux || err != nil {
			t.Fatalf("unset: %s %v", v, err)
		}
	}
	for _, tc := range []struct {
		raw  string
		want AgentHost
	}{
		{"", AgentHostTmux}, {"invalid", AgentHostTmux}, {"tmux\n", AgentHostTmux}, {" Process \n", AgentHostProcess},
	} {
		if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
			t.Fatal(err)
		}
		v, err := LoadAgentDefaultHostFile(path)
		if err != nil || v != tc.want {
			t.Fatalf("%q: %s %v", tc.raw, v, err)
		}
	}
	for _, host := range []AgentHost{AgentHostTmux, AgentHostProcess} {
		if err := SaveAgentDefaultHostFile(path, host); err != nil {
			t.Fatal(err)
		}
		v, err := LoadAgentDefaultHostFile(path)
		if err != nil || v != host {
			t.Fatalf("roundtrip: %s %v", v, err)
		}
	}
	b, _ := os.ReadFile(path)
	if err := SaveAgentDefaultHostFile(path, "bad"); err == nil {
		t.Fatal("invalid saved")
	}
	after, _ := os.ReadFile(path)
	if string(b) != string(after) {
		t.Fatal("invalid changed file")
	}
	if err := SaveAgentDefaultHostFile(filepath.Join(path, "child"), AgentHostTmux); err == nil {
		t.Fatal("write failure hidden")
	}
	v, err := LoadAgentDefaultHostFile(p.ConfigDir)
	if v != AgentHostTmux || err == nil {
		t.Fatalf("read error: %s %v", v, err)
	}
	item, ok := LookupSetting(AgentDefaultHostFileName)
	if !ok || item.Layer != LayerCentral {
		t.Fatal("not central")
	}
}
