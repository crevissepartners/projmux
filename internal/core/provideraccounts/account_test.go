package provideraccounts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestAccountValidationAndSafePublicFields(t *testing.T) {
	for _, name := range []string{"", "uid:account-test", "../x", "white space", strings.Repeat("a", 129), "x\n"} {
		if ValidateName(name) == nil {
			t.Fatalf("accepted name %q", name)
		}
	}
	for _, provider := range []string{"", "Codex", "antigravity"} {
		err := ValidateRegister(RegisterAccountRequest{Name: "work", Provider: provider})
		var e *Error
		if !errors.As(err, &e) || e.Reason != ProviderUnsupported {
			t.Fatalf("provider %q: %v", provider, err)
		}
	}
	if ValidateRef("uid:account-"+strings.Repeat("a", 32)) != nil {
		t.Fatal("valid UID refused")
	}
	if ValidateRef("uid:account-old") == nil {
		t.Fatal("malformed UID accepted")
	}
	data, _ := json.Marshal(ProviderAccount{})
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	expected := []string{"uid", "name", "provider", "root", "version", "state", "authenticationStatus"}
	if len(fields) != len(expected) {
		t.Fatalf("public fields %s", data)
	}
	for _, key := range expected {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing %s", key)
		}
	}
	err := Failure(IOFailure, "catalog", context.Canceled)
	var e *Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &e) || e.Reason != Canceled {
		t.Fatalf("cancellation %v", err)
	}
	if strings.Contains(Failure(IOFailure, "catalog", errors.New("private-value")).Error(), "private-value") {
		t.Fatal("unsafe error")
	}
}
