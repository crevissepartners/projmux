package render

// WindowStatus presents an intentional headless Window separately from runtime
// failures. The caller supplies localized text; real status tokens keep their
// existing spelling.
func WindowStatus(virtual bool, status, headless string) string {
	if virtual {
		return sanitizeCell(headless)
	}
	return status
}
