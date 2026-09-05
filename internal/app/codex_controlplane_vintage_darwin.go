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
