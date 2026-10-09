package localipc

import "errors"

// ErrProcessAbsent means the kernel proved that the PID has no live process.
// Other errors, including permission and identity parsing errors, prove nothing.
var ErrProcessAbsent = errors.New("process unavailable")
