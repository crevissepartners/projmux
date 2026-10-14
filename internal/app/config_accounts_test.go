package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	account "github.com/crevissepartners/projmux/internal/core/provideraccounts"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestConfigAccountsCLIAndSDKParity(t *testing.T) {
	cmd := centralSettingsTestCommand(t)
	paths, err := configPaths(cmd.homeDir, cmd.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	client, err := projmux.NewClient(projmux.Options{ConfigDir: paths.ConfigDir})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root := t.TempDir()
	// An inaccessible inert child proves no root enumeration/credential reader is needed.
	if err = os.Mkdir(filepath.Join(root, "auth.json"), 0o000); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if err = cmd.Run([]string{"accounts", "add", "work", "--provider", "codex", "--root", root, "-o", "json"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	var a core.ProviderAccount
	if err = json.Unmarshal(out.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	rows, err := client.ListAccounts(ctx)
	if err != nil || len(rows) != 1 || rows[0] != a {
		t.Fatal("SDK parity", rows, err)
	}
	if a.AuthenticationStatus != "unverified" {
		t.Fatal("false authentication")
	}
	out.Reset()
	if err = cmd.Run([]string{"accounts", "list", "-o", "json"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	var cliRows []core.ProviderAccount
	if err = json.Unmarshal(out.Bytes(), &cliRows); err != nil || len(cliRows) != 1 || cliRows[0] != a {
		t.Fatal("CLI parity", cliRows, err)
	}
	removed, err := client.RemoveAccount(ctx, core.RemoveAccountRequest{Ref: a.Name})
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err = cmd.Run([]string{"accounts", "remove", "uid:" + a.UID, "-o", "json"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	var old core.ProviderAccount
	if err = json.Unmarshal(out.Bytes(), &old); err != nil || old != removed {
		t.Fatal("removed parity", old, err)
	}
	out.Reset()
	if err = cmd.Run([]string{"accounts", "add", "work", "--provider", "codex", "--root", root, "-o", "json"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	var fresh core.ProviderAccount
	_ = json.Unmarshal(out.Bytes(), &fresh)
	if fresh.UID == a.UID {
		t.Fatal("reused UID")
	}
	_, sdkErr := client.RegisterAccount(ctx, core.RegisterAccountRequest{Name: "work", Provider: "claude", Root: t.TempDir()})
	out.Reset()
	cliErr := cmd.Run([]string{"accounts", "add", "work", "--provider", "claude", "--root", t.TempDir()}, &out, &stderr)
	var refusal *projmux.Refusal
	var cliRefusal *account.Error
	if !errors.As(sdkErr, &refusal) || !errors.As(cliErr, &cliRefusal) || string(refusal.Code) != string(cliRefusal.Reason) || out.Len() != 0 {
		t.Fatal("typed parity", sdkErr, cliErr)
	}
	if stderr.Len() != 0 {
		t.Fatal("unexpected stderr", stderr.String())
	}
}
func TestConfigAccountsUsageRefusesBeforeStoreEffects(t *testing.T) {
	for _, args := range [][]string{{"accounts"}, {"accounts", "bogus"}, {"accounts", "add"}, {"accounts", "add", "name", "--provider", "codex", "--root", "/missing", "--bogus"}, {"accounts", "list", "extra"}, {"accounts", "list", "-o", "uid"}, {"accounts", "remove"}} {
		cmd := centralSettingsTestCommand(t)
		paths, _ := configPaths(cmd.homeDir, cmd.lookupEnv)
		var out, stderr bytes.Buffer
		if err := cmd.Run(args, &out, &stderr); err == nil {
			t.Fatalf("accepted %v", args)
		}
		if _, err := os.Stat(paths.ConfigDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("usage effects %v %v", args, err)
		}
	}
}

type failedAccountOutput struct{}

func (failedAccountOutput) Write([]byte) (int, error) { return 0, errors.New("private output detail") }
func TestConfigAccountsOutputFailureReportsCommittedUnknown(t *testing.T) {
	cmd := centralSettingsTestCommand(t)
	root := t.TempDir()
	var stderr bytes.Buffer
	err := cmd.Run([]string{"accounts", "add", "work", "--provider", "codex", "--root", root, "-o", "json"}, failedAccountOutput{}, &stderr)
	var refusal *account.Error
	if !errors.As(err, &refusal) || refusal.Reason != account.Unknown {
		t.Fatal("committed output failure", err)
	}
	var out bytes.Buffer
	if err = cmd.Run([]string{"accounts", "list", "-o", "json"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	var rows []core.ProviderAccount
	if json.Unmarshal(out.Bytes(), &rows) != nil || len(rows) != 1 {
		t.Fatal("successful registration lost")
	}
}
