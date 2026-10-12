package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestImplementationAllowlist(t *testing.T) {
	for _, test := range []struct {
		path     string
		standard bool
		want     bool
	}{
		{"context", true, true},
		{module + "/internal/services/mutations", false, true},
		{module + "/internal/services/mutations/escape", false, false},
		{module + "/internal/core/receipt", false, true},
		{module + "/internal/core/receipt/escape", false, false},
		{module + "/internal/core/sessions", false, true},
		{module + "/internal/core/operatorclient", false, true},
		{module + "/internal/services/approvals", false, true},
		{module + "/internal/integrations/agentcontrol", false, true},
		{module + "/internal/integrations/agents/agentapproval", false, true},
		{module + "/internal/core/codexgeneration", false, true},
		{module + "/internal/services/approvals/escape", false, false},
		{module + "/internal/integrations/agentcontrol/escape", false, false},
		{module + "/internal/services/settings", false, true},
		{module + "/internal/services/usage", false, true},
		{module + "/internal/services/notifications", false, true},
		{module + "/internal/core/usage/adapters/claude", false, false},
		{module + "/internal/core/usage/adapters/codex", false, false},
		{module + "/pkg/projmux", false, true},
		{module + "/internal/services/instructions", false, true},
		{module + "/internal/services/projectlinks", false, true},
		{module + "/internal/services/projectlinks/escape", false, false},
		{module + "/internal/services/runtime", false, true},
		{module + "/internal/services/agentcatalog", false, true},
		{module + "/internal/services/agentcatalog/escape", false, false},
		{module + "/internal/integrations/agents/claude", false, true},
		{module + "/internal/core/runtimediag", false, true},
		{module + "/internal/services/runtime/escape", false, false},
		{"golang.org/x/sys/unix", false, true},
		{module + "/internal/app", false, false},
		{module + "/internal/cli", false, false},
		{module + "/internal/diagnostics", false, false},
		{"github.com/spf13/cobra", false, false},
		{"github.com/spf13/pflag", false, false},
		{"golang.org/x/term", false, true},
		{module + "/internal/app/personacmd", false, false},
		{module + "/internal/web/fixture", false, false},
		{module + "/internal/core/future", false, false},
		{module + "/internal/services/instructions/escape", false, false},
		{"example.com/other", false, false},
	} {
		if got := allowedImplementation(packageData{ImportPath: test.path, Standard: test.standard}); got != test.want {
			t.Errorf("allow %s = %t, want %t", test.path, got, test.want)
		}
	}
}

func TestBoundaryFixtures(t *testing.T) {
	root, err := filepath.Abs("../../../test/sdk/testdata/boundary")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("GOWORK", "off")
	for _, test := range []struct {
		name string
		want string
	}{
		{"good", ""},
		{"cli", `SDK implementation import is not allowed: "github.com/crevissepartners/projmux/internal/cli"`},
		{"cobra", `SDK implementation import is not allowed: "github.com/spf13/cobra"`},
		{"pflag", `SDK implementation import is not allowed: "github.com/spf13/pflag"`},
		{"app", `SDK implementation import is not allowed: "github.com/crevissepartners/projmux/internal/app/personacmd"`},
		{"web", `SDK implementation import is not allowed: "github.com/crevissepartners/projmux/internal/web/fixture"`},
		{"signature", "internal type leaks from"},
		{"function", "internal type leaks from"},
		{"field", "internal type leaks from"},
		{"method", "internal type leaks from"},
		{"generic", "internal type leaks from"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := check(module + "/sdk-boundary-fixture/" + test.name)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			// A failed fixture compilation is not proof that the boundary rejected it.
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected boundary diagnostic %q, got %v", test.want, err)
			}
		})
	}
}

func TestPureCoreBoundary(t *testing.T) {
	root, err := filepath.Abs("../../../test/sdk/testdata/boundary")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("GOWORK", "off")
	for _, name := range []string{"signature", "field", "method", "generic"} {
		if err := checkPackage(module+"/sdk-boundary-fixture/"+name, true); err == nil || !strings.Contains(err.Error(), "SDK implementation import is not allowed") {
			t.Fatalf("pure core accepted %s: %v", name, err)
		}
	}
}
