package protocol

// Param/result DTOs for the client<->daemon methods. Everything here is pure data
// and JSON-round-trippable. Tool options and launch overrides ride as opaque
// json.RawMessage so this package stays a dependency-free leaf; the daemon decodes
// them into their real types.

import "encoding/json"

// PingParams carries the caller's version for the compatibility handshake.
type PingParams struct {
	Version string `json:"version"`
}

// PingResult reports the daemon's identity.
type PingResult struct {
	Version string `json:"version"`
	PID     int    `json:"pid"`
}

// StatusResult reports daemon and per-project state for `toby daemon status`.
type StatusResult struct {
	Version  string          `json:"version"`
	PID      int             `json:"pid"`
	Uptime   string          `json:"uptime"`
	Projects []ProjectStatus `json:"projects"`
}

// ProjectStatus is a sanitized view of one live project (never env/argv/secrets).
type ProjectStatus struct {
	Label       string `json:"label"`
	ContainerID string `json:"containerID"`
	Sessions    int    `json:"sessions"`
	IdleSince   string `json:"idleSince,omitempty"`
}

// ProjectStopParams names the project whose container should be stopped.
type ProjectStopParams struct {
	Label string `json:"label"`
}

// ProjectStopResult reports how many project containers were stopped.
type ProjectStopResult struct {
	Stopped int `json:"stopped"`
}

// SessionStartParams is everything the daemon needs to bring a project up and
// produce a foreground launch plan. Options and Overrides are opaque here.
type SessionStartParams struct {
	Options        json.RawMessage `json:"options,omitempty"`
	Overrides      json.RawMessage `json:"overrides,omitempty"`
	Extra          []string        `json:"extra,omitempty"`
	RequestedTools []string        `json:"requestedTools,omitempty"`
	Primary        string          `json:"primary"`
	Install        bool            `json:"install,omitempty"`
	Upgrade        bool            `json:"upgrade,omitempty"`
	Interactive    bool            `json:"interactive,omitempty"`
	Managed        bool            `json:"managed,omitempty"`
	Cols           int             `json:"cols,omitempty"`
	Rows           int             `json:"rows,omitempty"`
}

// SessionStartResult hands the client the tool container id it attaches to (the tool
// runs `sandbox launch` as its main process, so no argv/env is needed here), or
// signals an install-only run that is already done. Managed selects the managed
// terminal for the client's attach.
type SessionStartResult struct {
	SessionID   string `json:"sessionID"`
	ContainerID string `json:"containerID"`
	Managed     bool   `json:"managed,omitempty"`
	InstallOnly bool   `json:"installOnly,omitempty"`
}

// InstallOutputParams is daemon->client: one streamed chunk of install/exec output.
type InstallOutputParams struct {
	SessionID string `json:"sessionID"`
	Stream    string `json:"stream"`
	Data      []byte `json:"data"`
}

// SessionReleaseParams drops a session and reports the tool's exit code.
type SessionReleaseParams struct {
	SessionID string `json:"sessionID"`
	ExitCode  int    `json:"exitCode"`
}

// ApprovalPromptParams is daemon->client: ask the user to approve an action.
type ApprovalPromptParams struct {
	SessionID string `json:"sessionID"`
	Action    string `json:"action"`
	Name      string `json:"name"`
	Message   string `json:"message"`
}

// ApprovalPromptResult is the user's decision.
type ApprovalPromptResult struct {
	Allow bool `json:"allow"`
}

// StatusProgressParams is daemon->client: a progress line to render (notification).
type StatusProgressParams struct {
	SessionID string `json:"sessionID,omitempty"`
	Message   string `json:"message"`
}
