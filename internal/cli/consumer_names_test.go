package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// privateConsumerName matches the ways a tracked public file can call a private
// consumer of this repository by name: the private web server and client, the
// web-dev build, the numbered private plan markers, and the private files and
// import paths that went with them. The match is on phrases, not on the bare
// word, so ordinary uses such as a `web search` setting or an example `./web`
// directory are not names of a consumer.
var privateConsumerName = regexp.MustCompile(`(?i:private web|web[ -](?:server|client|backend|consumer|ui|api|transcript|confirmation|persona|hosted|request|input|recovery|owner)|web-dev|web/dev|projmux-web|\bweb_[a-z_]*\.go|internal/web|fakeweb|"web")|\bWEB\b|\bWEB[0-9]+`)

// consumerNameAllowance lets one file, or the lines of one file that match
// line, keep a match of privateConsumerName. A path ending in "/" covers a
// directory. A nil line covers the whole file. Every entry says why, so a new
// entry is a reviewed decision and not a way around the check.
type consumerNameAllowance struct {
	path   string
	line   *regexp.Regexp
	reason string
}

var consumerNameAllowances = []consumerNameAllowance{
	{path: "CHANGELOG.md", reason: "release history written by release-please"},
	{path: ".security/", reason: "reviewed security baseline history"},
	{path: "scripts/security-baseline.py", reason: "reviewed security baseline history"},
	{path: "docs/assets/", reason: "binary documentation assets"},
	{path: "internal/cli/web_absence_test.go", reason: "the test that keeps a web surface out of this repository"},
	{path: "internal/cli/consumer_names_test.go", reason: "this test names the patterns it rejects"},
	{path: "internal/core/agentguidance/migration.go", line: regexp.MustCompile(`projmux-web:artifacts`), reason: "compatibility marker already written to disk"},
	{path: "internal/core/agentguidance/migration_test.go", line: regexp.MustCompile(`projmux-web:artifacts`), reason: "compatibility marker already written to disk"},
	{path: "docs/configuration.md", line: regexp.MustCompile(`projmux-web:artifacts`), reason: "compatibility marker already written to disk"},
	{path: "internal/app/doctor.go", line: regexp.MustCompile(`a web-dev build`), reason: "doctor output text kept byte for byte; tracked as a follow-up"},
	{path: "internal/core/agentmessage/origin_test.go", line: regexp.MustCompile(`\{"web", true\}`), reason: "client name earlier builds recorded; their records stay operator input"},
	{path: "internal/integrations/agents/agentapproval/store_test.go", line: regexp.MustCompile(`\{"web", true\}`), reason: "client name earlier builds recorded; their records stay operator input"},
	{path: "internal/app/agent_turn_claude_test.go", line: regexp.MustCompile(`\{"web", true\}`), reason: "client name earlier builds recorded; their records stay operator input"},
	{path: "internal/integrations/agents/agentmessage/origin_store_test.go", line: regexp.MustCompile(`previousBuildOperatorClient = "web"`), reason: "client name earlier builds recorded"},
	{path: "internal/integrations/agents/agentmessage/read_test.go", line: regexp.MustCompile(`"client":"web"`), reason: "stored record written by an earlier build"},
	{path: "internal/integrations/agents/agentmessage/testdata/history-full-route.jsonl", line: regexp.MustCompile(`"client":"web"`), reason: "stored record written by an earlier build"},
	{path: "internal/core/operatorclient/audit_test.go", line: regexp.MustCompile(`namedClientsNeverInProduct = \[\]string\{"web"\}`), reason: "keeps product code from spelling the client name earlier builds recorded"},
	{path: "internal/app/operator_origin_test.go", line: regexp.MustCompile(`projmux web client|, "web"\}|client == "web"`), reason: "notice earlier builds rendered for the client name they recorded"},
	{path: "internal/ui/picker/backend_test.go", reason: "ordinary item name in picker fixtures"},
	{path: "internal/app/switch_test.go", line: regexp.MustCompile(`Title: "web"`), reason: "ordinary window title in a picker fixture"},
	{path: "internal/app/ai_context_dir_test.go", line: regexp.MustCompile(`"web", "api"`), reason: "ordinary example directory"},
	{path: "internal/integrations/agents/aisessions/depth_test.go", line: regexp.MustCompile(`-workspace-app-web-api`), reason: "ordinary example directory"},
}

func (a consumerNameAllowance) allows(path, line string) bool {
	if strings.HasSuffix(a.path, "/") {
		if !strings.HasPrefix(path, a.path) {
			return false
		}
	} else if path != a.path {
		return false
	}
	return a.line == nil || a.line.MatchString(line)
}

// consumerNameFindings returns "path:line: text" for every line of the given
// files that names a private consumer and is not allowed.
func consumerNameFindings(files map[string]string) []string {
	var found []string
	for path, text := range files {
		for index, line := range strings.Split(text, "\n") {
			if !privateConsumerName.MatchString(line) {
				continue
			}
			allowed := false
			for _, allowance := range consumerNameAllowances {
				if allowance.allows(path, line) {
					allowed = true
					break
				}
			}
			if !allowed {
				found = append(found, path+":"+strconv.Itoa(index+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	return found
}

// TestTrackedFilesDoNotNamePrivateConsumers is the enforcement of the public
// repository's naming guarantee: no tracked text file calls a private consumer
// by name outside the reviewed allowances above.
func TestTrackedFilesDoNotNamePrivateConsumers(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Skip("not a git checkout; there is no tracked file list to read")
	}
	listed, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	files := map[string]string{}
	for name := range strings.SplitSeq(string(listed), "\x00") {
		if name == "" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", name, err)
		}
		if bytes.IndexByte(raw, 0) >= 0 {
			continue
		}
		files[name] = string(raw)
	}
	if len(files) == 0 {
		t.Fatal("no tracked text file was read")
	}
	for _, finding := range consumerNameFindings(files) {
		t.Errorf("public file names a private consumer: %s", finding)
	}
}

// TestConsumerNameCheckRejectsAnUnallowedLine proves the check can fail: one
// line outside the allowances is reported, and the allowed forms are not.
func TestConsumerNameCheckRejectsAnUnallowedLine(t *testing.T) {
	t.Parallel()

	got := consumerNameFindings(map[string]string{
		"docs/example.md": "fine line\nthe private web server reads this\n",
	})
	if len(got) != 1 || !strings.HasPrefix(got[0], "docs/example.md:2:") {
		t.Fatalf("findings = %q, want exactly docs/example.md:2", got)
	}
	for _, path := range []string{"pkg/projmux/example.go", "test/sdk/testdata/boundary/consumer/consumer.go"} {
		for _, text := range []string{"// the web client calls this", `Owner: "web"`, "projmux-web-delivered", "internal/web/fixture", "a web-dev build"} {
			if got := consumerNameFindings(map[string]string{path: text}); len(got) != 1 {
				t.Errorf("%s %q findings = %q, want one", path, text, got)
			}
		}
	}
	for _, allowed := range []struct{ path, text string }{
		{"CHANGELOG.md", "the web client was removed"},
		{"internal/core/agentguidance/migration.go", `const webBegin = "<!-- projmux-web:artifacts:begin -->"`},
		{"internal/ui/picker/backend_test.go", `{Title: "web", Value: "/repo/web"}`},
		{"docs/example.md", "the web search setting and ./web directory"},
	} {
		if got := consumerNameFindings(map[string]string{allowed.path: allowed.text}); len(got) != 0 {
			t.Errorf("%s %q was reported: %q", allowed.path, allowed.text, got)
		}
	}
}
