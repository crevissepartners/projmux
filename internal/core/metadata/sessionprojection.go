package metadata

// LowerProjectSessionsEndedOnServer is the one judgement that turns "this
// exact tmux server no longer has that session" into Registry state.
//
// socketPath is the exact absolute socket path of one server, and present is
// the complete set of session names that server has now -- every session, not
// only the ones carrying projmux identity, so a session that merely lost its
// mirror still counts as present. A Project is lowered when, and only when,
// its projection is live, records exactly socketPath (a plain string match;
// an empty recorded path never matches), and names a session absent from
// present. The lower writes live=false and nothing else: the session name and
// socketPath stay as recorded, and no Window, Pane, or Agent is touched.
//
// Absence from a server is evidence only about sessions that server was
// recorded as holding. A Project recorded on another server, or on no known
// server, is never lowered here, so an empty socketPath lowers nothing.
//
// It returns a copy of every Project it lowered, in Registry order. A pass
// that lowered nothing leaves the Registry, including UpdatedAt, untouched.
func (m Mutator) LowerProjectSessionsEndedOnServer(reg *Registry, socketPath string, present map[string]bool) []Project {
	if reg == nil || socketPath == "" {
		return nil
	}
	var lowered []Project
	for i := range reg.Projects {
		session := reg.Projects[i].Status.Session
		if session == nil || !session.Live || session.SocketPath != socketPath || present[session.Name] {
			continue
		}
		// A fresh value, never a write through the shared pointer: a shallow
		// Registry copy elsewhere must not observe this lower.
		next := *session
		next.Live = false
		reg.Projects[i].Status.Session = &next
		lowered = append(lowered, reg.Projects[i].Clone())
	}
	if len(lowered) != 0 {
		reg.UpdatedAt = m.clock()().UTC()
	}
	return lowered
}
