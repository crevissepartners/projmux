package operatorclient

import (
	"errors"
	"strings"
	"testing"
)

func TestValidAdmitsOnlyTheClientNameRule(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want bool
	}{
		{"ui", true},
		{"a", true},
		{"client-2", true},
		{"a-", true},
		{strings.Repeat("a", MaxBytes), true},
		{"", false},
		{strings.Repeat("a", MaxBytes+1), false},
		{"2client", false},
		{"-client", false},
		{"Client", false},
		{"cli_ent", false},
		{"cli ent", false},
		{"cli.ent", false},
		{"klïent", false},
		{" ui", false},
	} {
		if got := Valid(test.name); got != test.want {
			t.Errorf("Valid(%q) = %t, want %t", test.name, got, test.want)
		}
		err := Validate(test.name)
		if (err == nil) != test.want {
			t.Errorf("Validate(%q) = %v, want valid %t", test.name, err, test.want)
		}
		if err != nil && (!errors.Is(err, ErrInvalid) || !strings.HasPrefix(err.Error(), ReasonInvalid+": ")) {
			t.Errorf("Validate(%q) = %v, want the %s token", test.name, err, ReasonInvalid)
		}
	}
}
