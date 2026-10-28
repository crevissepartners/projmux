package core

import "time"

// ResumeCandidateQuery selects the recorded provider sessions
// ListResumeCandidates lists. Project is the Project the resume would open a
// Window in. Cwd is the directory whose sessions are listed, an absolute path
// that defaults to the Project root; the scan depth and the row limit are the
// ones the CLI resume picker resolves for that directory. Provider lists one
// provider; empty lists every enabled provider.
type ResumeCandidateQuery struct {
	Project, Provider, Cwd string
}

// ResumeCandidate is one listed session in the order the CLI resume picker
// shows it. Provider, Conversation, Source and Cwd are what a ResumeSelection
// names to resume it; Cwd is the directory the session was recorded in when
// the scan reached below the query's Cwd, otherwise the query's Cwd. Title,
// Branch and UpdatedAt are the row's summary; no transcript or preview text
// is read.
type ResumeCandidate struct {
	Provider, Conversation, Source, Cwd string
	Title, Branch                       string
	UpdatedAt                           time.Time
}
