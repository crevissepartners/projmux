// Package view resolves constants through the imported package name.
package view

import "example.com/formatguard/internal/opts"

// Status tests a user option.
const Status = "#{?" + opts.UserOption + ",set,unset}"
