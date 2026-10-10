package render

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/i18n"
)

func TestRegistryWindowHeadlessRenderGolden(t *testing.T) {
	var out strings.Builder
	for _, locale := range []i18n.Locale{i18n.FallbackLocale, "ko-KR"} {
		text, err := i18n.NewLocalizer(locale).Text(i18n.KeyWindowHeadless)
		if err != nil {
			t.Fatal(err)
		}
		for _, test := range []struct {
			name   string
			status resourcegraph.Status
		}{{"virtual", registryview.StatusVirtual}, {"real", resourcegraph.StatusLive}} {
			fmt.Fprintf(&out, "%s %s: %s\n", locale, test.name, WindowStatus(test.status == registryview.StatusVirtual, string(test.status), text.String()))
		}
	}
	want, err := os.ReadFile("testdata/window-headless.golden")
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) {
		t.Fatalf("render:\n%s\nwant:\n%s", out.String(), want)
	}
	if got := WindowStatus(false, string(resourcegraph.StatusOffline), "headless"); got != "offline" {
		t.Fatalf("process Agent label = %s", got)
	}
}
