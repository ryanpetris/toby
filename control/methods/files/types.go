package files

// Wire shapes for the file.* methods, carried in the JSON-RPC params field.

type CreateParams struct {
	Path string `json:"path" jsonschema:"path to write inside the sandbox"`
	Mode uint32 `json:"mode" jsonschema:"file mode bits"`
	Data []byte `json:"data" jsonschema:"file contents, base64-encoded by JSON"`
	UID  int    `json:"uid,omitempty" jsonschema:"owner user id; 0 means root"`
	GID  int    `json:"gid,omitempty" jsonschema:"owner group id; 0 means root"`
}

type DeleteParams struct {
	Path      string `json:"path" jsonschema:"path to remove inside the sandbox"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"remove directories recursively when true"`
}

type MkdirParams struct {
	Path string `json:"path" jsonschema:"directory path to create inside the sandbox"`
	Mode uint32 `json:"mode" jsonschema:"directory mode bits"`
	UID  int    `json:"uid,omitempty" jsonschema:"owner user id; 0 means root"`
	GID  int    `json:"gid,omitempty" jsonschema:"owner group id; 0 means root"`
}

type SymlinkParams struct {
	Path   string `json:"path" jsonschema:"symlink path to create inside the sandbox"`
	Target string `json:"target" jsonschema:"symlink target"`
	UID    int    `json:"uid,omitempty" jsonschema:"owner user id; 0 means root"`
	GID    int    `json:"gid,omitempty" jsonschema:"owner group id; 0 means root"`
}
