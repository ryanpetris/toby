package command

// Wire shapes for the command.* methods, carried in the JSON-RPC params field.

type RunParams struct {
	CommandID  string   `json:"command_id" jsonschema:"UUID identifying this command execution"`
	Argv       []string `json:"argv" jsonschema:"command argv to run inside the sandbox"`
	Foreground bool     `json:"foreground,omitempty" jsonschema:"whether this command is the foreground process"`
	HideOutput bool     `json:"hide_output,omitempty" jsonschema:"redirect stdout and stderr to /dev/null"`
	UID        int      `json:"uid,omitempty" jsonschema:"process user id; 0 means root"`
	GID        int      `json:"gid,omitempty" jsonschema:"process group id; 0 means root"`
	Groups     []int    `json:"groups,omitempty" jsonschema:"supplementary group ids"`
}

type ExitParams struct {
	CommandID string `json:"command_id" jsonschema:"UUID identifying this command execution"`
	ExitCode  int    `json:"exit_code" jsonschema:"process exit code"`
	Error     string `json:"error,omitempty" jsonschema:"optional process execution error"`
}
