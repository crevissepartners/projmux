package app

// defaultCodexProcessImages reports that this platform exposes no process table
// this reader can establish binary vintage from.
//
// Reporting the absence is the whole contract. Darwin has no /proc/<pid>/exe
// and therefore no way to tell a process running the installed image from one
// whose image was replaced under it; answering `current` here would certify
// exactly the falsehood this projection exists to prevent.
func defaultCodexProcessImages() ([]codexProcessImage, bool) {
	return nil, false
}

// defaultProjmuxImageReplaced reports no vintage change, because this platform
// exposes no executable link to read one from.
//
// False rather than unknown, and the difference matters here: this answer is
// the entry condition of a drain, so an unobservable platform must decline to
// drain rather than drain on a guess. The absence is stated by the replacement
// table's `unsupported-platform` row, which is where a reader looks for it.
func defaultProjmuxImageReplaced() bool { return false }
