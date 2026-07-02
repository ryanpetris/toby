// Package protocol is the wire contract between the Toby client and daemon: the
// JSON-RPC method names and their param/result DTOs. It is a leaf package (no fx,
// no transport, no control dependency) so both the client and the daemon depend on
// exactly the same payloads regardless of which transport carries them.
package protocol

// Method names for the client<->daemon channel. Client-initiated methods drive a
// session; daemon-initiated methods (approval.prompt, status.progress) call back to
// the client that owns a session, which is why the channel is bidirectional.
const (
	// MethodDaemonPing is a liveness + version handshake.
	MethodDaemonPing = "daemon.ping"
	// MethodDaemonStatus reports daemon and project state.
	MethodDaemonStatus = "daemon.status"
	// MethodDaemonStop asks the daemon to shut down.
	MethodDaemonStop = "daemon.stop"
	// MethodProjectStop stops a single project's container.
	MethodProjectStop = "project.stop"
	// MethodSessionStart brings the project up (once) and returns the plan the
	// client needs to run the foreground tool itself.
	MethodSessionStart = "session.start"
	// MethodSessionRelease drops a session's refcount and approval registration.
	MethodSessionRelease = "session.release"

	// MethodApprovalPrompt is daemon->client: ask the user to approve an action.
	MethodApprovalPrompt = "approval.prompt"
	// MethodStatusProgress is daemon->client: a progress line to render (notification).
	MethodStatusProgress = "status.progress"
	// MethodInstallOutput is daemon->client: one streamed chunk of install/exec output
	// (notification).
	MethodInstallOutput = "install.output"
)
