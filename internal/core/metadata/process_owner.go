package metadata

// LabelProcessOwner is the Agent label that names the host claiming a process
// Agent, such as a web server that recovered or moved it. Client.MarkProcessOwner
// writes it only while the given activation is current; the generic label
// command can also set it.
const LabelProcessOwner = "process-owner"
