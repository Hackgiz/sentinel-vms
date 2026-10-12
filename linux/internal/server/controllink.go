package server

// ControlLink is the management side of the control link (PLATFORM.md,
// "Control link"): the mTLS listener remote recording servers dial. It is
// set by cmd/sentinel when the control listener runs; nil means remote
// recording servers can't connect (endpoints that need it answer 409).
//
// Connected servers register themselves with ConnectRecorder and
// ApplyRecorder; this interface is what the API needs in the other
// direction.
type ControlLink interface {
	// EnrollmentToken turns a stored enrollment secret into the token an
	// admin hands out (it embeds the CA fingerprint for pinning).
	EnrollmentToken(secret string) string
	// EnrollCommand is the command line to run on the new machine.
	EnrollCommand(token string) string
	// Disconnect drops a server's connection now (after a revoke).
	Disconnect(serverID string)
}
