package app

import (
	"fmt"
	"strconv"

	o "example.com/formatguard/internal/opts"
)

// Conditional uses a variable tmux does not define.
func Conditional() string {
	return "#{?client_active_pane,a,b}"
}

// Nested hides an unknown name inside a modifier and a comparison.
func Nested() string {
	return "#{==:" +
		"#{q:no_such_var_xyz},x}"
}

// Resolved builds formats from constants in another package.
func Resolved() string {
	return "#{" + o.PaneVar + "}" + "#{" + o.UserOption + "_x}" + "#{" + sessionName + "}"
}

// Runtime builds a name that only exists at runtime.
func Runtime(n int) string {
	return "#{" +
		strconv.Itoa(n) + "}"
}

// Verb formats a name with fmt.
func Verb(name string) string { return fmt.Sprintf("#{%s}", name) }

const sessionName = "session" + "_name"
