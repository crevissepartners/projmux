package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestAgentMessageSourceAnchorCatalogHelpAndGeneratedDocsParity(t *testing.T) {
	t.Parallel()
	const usage = "projmux agent message send <agent-ref> [--source <agent-ref>] [--message-ref <ref>] [--reply-to <ref>] [--ttl <duration>] -- <text>"
	const meaning = "--source selects a source Agent anchor, not caller authentication (default: active Pane)"
	reference := readGeneratedReference(t)
	for _, path := range [][]string{{"agent"}, {"agent", "message"}, {"agent", "message", "send"}} {
		t.Run(strings.Join(path, "/"), func(t *testing.T) {
			resolved, route, ok := Resolve(path)
			if !ok || len(resolved) != len(path) {
				t.Fatalf("route %v missing", path)
			}
			var catalog strings.Builder
			catalog.WriteString(strings.Join(route.Usage, "\n") + "\n" + route.Summary)
			// Agent help exposes the message summary in its child listing.
			for _, child := range route.Children {
				catalog.WriteString("\n" + child.Summary)
			}
			target, ok := RequestedHelp(append(append([]string{}, path...), "--help"))
			if !ok {
				t.Fatalf("help not resolved for %v", path)
			}
			var help bytes.Buffer
			if err := RenderHelp(&help, target); err != nil {
				t.Fatal(err)
			}
			heading := strings.Repeat("#", len(path)+1) + " `projmux " + strings.Join(path, " ") + "`\n"
			_, section, found := strings.Cut(reference, heading)
			if !found {
				t.Fatalf("generated reference lacks %q", heading)
			}
			// Only this route's section counts; a descendant cannot mask a gap.
			section, _, _ = strings.Cut(section, "\n##")
			for surface, text := range map[string]string{"catalog": catalog.String(), "help": help.String(), "generated docs": section} {
				for _, want := range []string{usage, meaning} {
					if !strings.Contains(text, want) {
						t.Errorf("%s for %v lacks %q", surface, path, want)
					}
				}
				if strings.Contains(text, "from the current exact Agent") {
					t.Errorf("%s for %v still asserts current Agent attribution", surface, path)
				}
			}
		})
	}
}
