package control

import (
	"encoding/json"
	"errors"
	"fmt"
)

const JSONRPCVersion = "2.0"

const (
	MethodContextInit      = "context.init"
	MethodFileCreate       = "file.create"
	MethodFileDelete       = "file.delete"
	MethodFileMkdir        = "file.mkdir"
	MethodFileSymlink      = "file.symlink"
	MethodCommandRun       = "command.run"
	MethodCommandExit      = "command.exit"
	MethodSandboxTerminate = "sandbox.terminate"
	MethodGitCommit        = "git.commit"
	MethodGitFetch         = "git.fetch"
	MethodGitPush          = "git.push"
)

const (
	CodeParseError        = -32700
	CodeInvalidRequest    = -32600
	CodeMethodNotFound    = -32601
	CodeInvalidParams     = -32602
	CodeInternalError     = -32603
	CodeProjectNotVisible = -32007
)

type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type EmptyResult struct{}

type FileCreateParams struct {
	Path string `json:"path" jsonschema:"path to write inside the sandbox"`
	Mode uint32 `json:"mode" jsonschema:"file mode bits"`
	Data []byte `json:"data" jsonschema:"file contents, base64-encoded by JSON"`
}

type FileDeleteParams struct {
	Path      string `json:"path" jsonschema:"path to remove inside the sandbox"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"remove directories recursively when true"`
}

type FileMkdirParams struct {
	Path string `json:"path" jsonschema:"directory path to create inside the sandbox"`
	Mode uint32 `json:"mode" jsonschema:"directory mode bits"`
}

type FileSymlinkParams struct {
	Path   string `json:"path" jsonschema:"symlink path to create inside the sandbox"`
	Target string `json:"target" jsonschema:"symlink target"`
}

type CommandRunParams struct {
	CommandID  string   `json:"command_id" jsonschema:"UUID identifying this command execution"`
	Argv       []string `json:"argv" jsonschema:"command argv to run inside the sandbox"`
	Foreground bool     `json:"foreground,omitempty" jsonschema:"whether this command is the foreground process"`
	HideOutput bool     `json:"hide_output,omitempty" jsonschema:"redirect stdout and stderr to /dev/null"`
}

type CommandExitParams struct {
	CommandID string `json:"command_id" jsonschema:"UUID identifying this command execution"`
	ExitCode  int    `json:"exit_code" jsonschema:"process exit code"`
	Error     string `json:"error,omitempty" jsonschema:"optional process execution error"`
}

type GitRepositoryParams struct {
	Repository string `json:"repository" jsonschema:"repository name visible in the sandbox, relative to XDG_PROJECTS_DIR"`
}

type GitCommitParams struct {
	Repository string `json:"repository" jsonschema:"repository name visible in the sandbox, relative to XDG_PROJECTS_DIR"`
	Message    string `json:"message" jsonschema:"commit message passed to git commit -m"`
}

type GitPushParams struct {
	Repository string `json:"repository" jsonschema:"repository name visible in the sandbox, relative to XDG_PROJECTS_DIR"`
	Branch     string `json:"branch" jsonschema:"single branch to push"`
	Origin     string `json:"origin,omitempty" jsonschema:"remote name to push to, defaults to origin"`
}

type GitResult struct {
	Repository string `json:"repository" jsonschema:"repository name used for the git command"`
	ExitCode   int    `json:"exit_code" jsonschema:"git process exit code"`
	Stdout     string `json:"stdout" jsonschema:"git standard output"`
	Stderr     string `json:"stderr" jsonschema:"git standard error"`
}

func NewContextInitRequest(id int64) ([]byte, error) {
	return newRequest(id, MethodContextInit, nil)
}

func NewFileCreateRequest(id int64, params FileCreateParams) ([]byte, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return newRequest(id, MethodFileCreate, data)
}

func NewFileDeleteRequest(id int64, params FileDeleteParams) ([]byte, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return newRequest(id, MethodFileDelete, data)
}

func NewFileMkdirRequest(id int64, params FileMkdirParams) ([]byte, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return newRequest(id, MethodFileMkdir, data)
}

func NewFileSymlinkRequest(id int64, params FileSymlinkParams) ([]byte, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return newRequest(id, MethodFileSymlink, data)
}

func NewCommandRunRequest(id int64, params CommandRunParams) ([]byte, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return newRequest(id, MethodCommandRun, data)
}

func NewCommandExitRequest(id int64, params CommandExitParams) ([]byte, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return newRequest(id, MethodCommandExit, data)
}

func NewSandboxTerminateRequest(id int64) ([]byte, error) {
	return newRequest(id, MethodSandboxTerminate, nil)
}

func NewGitCommitRequest(id int64, repository, message string) ([]byte, error) {
	params, err := json.Marshal(GitCommitParams{Repository: repository, Message: message})
	if err != nil {
		return nil, err
	}
	return newRequest(id, MethodGitCommit, params)
}

func NewGitFetchRequest(id int64, repository string) ([]byte, error) {
	params, err := json.Marshal(GitRepositoryParams{Repository: repository})
	if err != nil {
		return nil, err
	}
	return newRequest(id, MethodGitFetch, params)
}

func NewGitPushRequest(id int64, repository, branch, origin string) ([]byte, error) {
	params, err := json.Marshal(GitPushParams{Repository: repository, Branch: branch, Origin: origin})
	if err != nil {
		return nil, err
	}
	return newRequest(id, MethodGitPush, params)
}

func newRequest(id int64, method string, params json.RawMessage) ([]byte, error) {
	idBytes, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(RPCRequest{JSONRPC: JSONRPCVersion, ID: idBytes, Method: method, Params: params})
}

func DecodeRequest(data []byte) (RPCRequest, error) {
	var req RPCRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return RPCRequest{}, fmt.Errorf("parse request: %w", err)
	}
	if req.JSONRPC != JSONRPCVersion || len(req.ID) == 0 || req.Method == "" {
		return RPCRequest{}, errors.New("invalid JSON-RPC request")
	}
	return req, nil
}

func DecodeGitRepositoryParams(raw json.RawMessage) (GitRepositoryParams, error) {
	if len(raw) == 0 {
		return GitRepositoryParams{}, errors.New("missing params")
	}
	var params GitRepositoryParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return GitRepositoryParams{}, err
	}
	if params.Repository == "" {
		return GitRepositoryParams{}, errors.New("repository is required")
	}
	return params, nil
}

func DecodeGitCommitParams(raw json.RawMessage) (GitCommitParams, error) {
	if len(raw) == 0 {
		return GitCommitParams{}, errors.New("missing params")
	}
	var params GitCommitParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return GitCommitParams{}, err
	}
	if params.Repository == "" {
		return GitCommitParams{}, errors.New("repository is required")
	}
	if params.Message == "" {
		return GitCommitParams{}, errors.New("message is required")
	}
	return params, nil
}

func DecodeGitPushParams(raw json.RawMessage) (GitPushParams, error) {
	if len(raw) == 0 {
		return GitPushParams{}, errors.New("missing params")
	}
	var params GitPushParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return GitPushParams{}, err
	}
	if params.Repository == "" {
		return GitPushParams{}, errors.New("repository is required")
	}
	if params.Branch == "" {
		return GitPushParams{}, errors.New("branch is required")
	}
	return params, nil
}

func DecodeFileCreateParams(raw json.RawMessage) (FileCreateParams, error) {
	var params FileCreateParams
	if err := decodeRequiredParams(raw, &params); err != nil {
		return FileCreateParams{}, err
	}
	if params.Path == "" {
		return FileCreateParams{}, errors.New("path is required")
	}
	return params, nil
}

func DecodeFileDeleteParams(raw json.RawMessage) (FileDeleteParams, error) {
	var params FileDeleteParams
	if err := decodeRequiredParams(raw, &params); err != nil {
		return FileDeleteParams{}, err
	}
	if params.Path == "" {
		return FileDeleteParams{}, errors.New("path is required")
	}
	return params, nil
}

func DecodeFileMkdirParams(raw json.RawMessage) (FileMkdirParams, error) {
	var params FileMkdirParams
	if err := decodeRequiredParams(raw, &params); err != nil {
		return FileMkdirParams{}, err
	}
	if params.Path == "" {
		return FileMkdirParams{}, errors.New("path is required")
	}
	return params, nil
}

func DecodeFileSymlinkParams(raw json.RawMessage) (FileSymlinkParams, error) {
	var params FileSymlinkParams
	if err := decodeRequiredParams(raw, &params); err != nil {
		return FileSymlinkParams{}, err
	}
	if params.Path == "" {
		return FileSymlinkParams{}, errors.New("path is required")
	}
	if params.Target == "" {
		return FileSymlinkParams{}, errors.New("target is required")
	}
	return params, nil
}

func DecodeCommandRunParams(raw json.RawMessage) (CommandRunParams, error) {
	var params CommandRunParams
	if err := decodeRequiredParams(raw, &params); err != nil {
		return CommandRunParams{}, err
	}
	if params.CommandID == "" {
		return CommandRunParams{}, errors.New("command_id is required")
	}
	if len(params.Argv) == 0 {
		return CommandRunParams{}, errors.New("argv is required")
	}
	return params, nil
}

func DecodeCommandExitParams(raw json.RawMessage) (CommandExitParams, error) {
	var params CommandExitParams
	if err := decodeRequiredParams(raw, &params); err != nil {
		return CommandExitParams{}, err
	}
	if params.CommandID == "" {
		return CommandExitParams{}, errors.New("command_id is required")
	}
	return params, nil
}

func decodeRequiredParams(raw json.RawMessage, dest any) error {
	if len(raw) == 0 {
		return errors.New("missing params")
	}
	return json.Unmarshal(raw, dest)
}

func ResponseOK(id json.RawMessage, result any) []byte {
	data, _ := json.Marshal(RPCResponse{JSONRPC: JSONRPCVersion, ID: cloneID(id), Result: result})
	return append(data, '\n')
}

func ResponseError(id json.RawMessage, code int, message string, data any) []byte {
	resp := RPCResponse{JSONRPC: JSONRPCVersion, ID: cloneID(id), Error: &RPCError{Code: code, Message: message, Data: data}}
	encoded, _ := json.Marshal(resp)
	return append(encoded, '\n')
}

func DecodeResponse(data []byte) (RPCResponse, error) {
	var resp RPCResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return RPCResponse{}, err
	}
	if resp.JSONRPC != JSONRPCVersion {
		return RPCResponse{}, errors.New("invalid JSON-RPC response")
	}
	return resp, nil
}

func DecodeGitResult(result any) (GitResult, error) {
	var gitResult GitResult
	if err := decodeResult(result, &gitResult); err != nil {
		return GitResult{}, err
	}
	return gitResult, nil
}

func DecodeEmptyResult(result any) (EmptyResult, error) {
	var empty EmptyResult
	if result == nil {
		return empty, nil
	}
	if err := decodeResult(result, &empty); err != nil {
		return EmptyResult{}, err
	}
	return empty, nil
}

func decodeResult(result any, dest any) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dest)
}

func cloneID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return append(json.RawMessage(nil), id...)
}
