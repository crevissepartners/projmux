package main

import (
	"strings"
	"testing"
)

func TestProfileInspectFlagErrorsUseCatalogUsage(t *testing.T) {
	flagParseShapeIsolate(t)
	for _, verb := range []string{"parse", "preview"} {
		for _, suffix := range [][]string{{"--zz-probe"}, {"--file"}} {
			argv := append([]string{"profile", verb}, suffix...)
			code, out, stderr, usage := flagParseShapeRun(argv)
			if code != 2 || out != "" || !usage {
				t.Fatalf("%v: code=%d out=%q err=%q usage=%t", argv, code, out, stderr, usage)
			}
			clause := flagParseShapeUsageClause(t, []string{"profile", verb})
			for _, problem := range flagParseShapeProblems(stderr, clause) {
				t.Errorf("%s: %s; stderr=%q", strings.Join(argv, " "), problem, stderr)
			}
		}
	}
}
