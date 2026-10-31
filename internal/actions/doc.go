// Package actions stops what holds a socket: it signals processes (SIGTERM,
// then SIGKILL), stops systemd services and stops Docker containers. It
// decides which of these to offer for a socket (Plan) and carries them out
// (Runner), independently of the UI.
package actions
