package control

import (
	"encoding/json"
	"errors"
	"fmt"
)

const JSONRPCVersion = "2.0"

const (
	CodeParseError                = -32700
	CodeInvalidRequest            = -32600
	CodeMethodNotFound            = -32601
	CodeInvalidParams             = -32602
	CodeInternalError             = -32603
	CodeTmuxRequired              = -32001
	CodeDenied                    = -32002
	CodeMountFailed               = -32003
	CodeControlFileError          = -32004
	CodeProjectNotFound           = -32005
	CodeReadmeNotFound            = -32006
	CodeProjectNotVisible         = -32007
	CodeMountableProjectsDisabled = -32008
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

type ProjectParams struct {
	Name string `json:"name" jsonschema:"project directory name under XDG_PROJECTS_DIR"`
}

type ProjectInfo struct {
	Name string `json:"name" jsonschema:"project directory name"`
	Path string `json:"path" jsonschema:"absolute host path to the project directory"`
}

type ProjectListResult struct {
	ProjectRoot string        `json:"project_root" jsonschema:"absolute host path of XDG_PROJECTS_DIR"`
	Projects    []ProjectInfo `json:"projects" jsonschema:"available project directories"`
}

type ProjectReadmeResult struct {
	Name    string `json:"name" jsonschema:"project directory name"`
	Path    string `json:"path" jsonschema:"absolute host path to README.md"`
	Content string `json:"content" jsonschema:"README.md contents"`
}

type MountResult struct {
	HostPath    string `json:"host_path" jsonschema:"host path requested for mounting"`
	SandboxPath string `json:"sandbox_path" jsonschema:"sandbox path where the host path is visible"`
	VirtualPath string `json:"virtual_path" jsonschema:"internal FUSE virtual path for the mount"`
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

func NewProjectListRequest(id int64) ([]byte, error) {
	return newRequest(id, "project_list", nil)
}

func NewProjectReadmeRequest(id int64, name string) ([]byte, error) {
	params, err := json.Marshal(ProjectParams{Name: name})
	if err != nil {
		return nil, err
	}
	return newRequest(id, "project_readme", params)
}

func NewProjectMountRequest(id int64, name string) ([]byte, error) {
	params, err := json.Marshal(ProjectParams{Name: name})
	if err != nil {
		return nil, err
	}
	return newRequest(id, "project_mount", params)
}

func NewGitCommitRequest(id int64, repository, message string) ([]byte, error) {
	params, err := json.Marshal(GitCommitParams{Repository: repository, Message: message})
	if err != nil {
		return nil, err
	}
	return newRequest(id, "git_commit", params)
}

func NewGitFetchRequest(id int64, repository string) ([]byte, error) {
	params, err := json.Marshal(GitRepositoryParams{Repository: repository})
	if err != nil {
		return nil, err
	}
	return newRequest(id, "git_fetch", params)
}

func NewGitPushRequest(id int64, repository, branch, origin string) ([]byte, error) {
	params, err := json.Marshal(GitPushParams{Repository: repository, Branch: branch, Origin: origin})
	if err != nil {
		return nil, err
	}
	return newRequest(id, "git_push", params)
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

func DecodeProjectParams(raw json.RawMessage) (ProjectParams, error) {
	if len(raw) == 0 {
		return ProjectParams{}, errors.New("missing params")
	}
	var params ProjectParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return ProjectParams{}, err
	}
	if params.Name == "" {
		return ProjectParams{}, errors.New("name is required")
	}
	return params, nil
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

func DecodeMountResult(result any) (MountResult, error) {
	var mount MountResult
	if err := decodeResult(result, &mount); err != nil {
		return MountResult{}, err
	}
	return mount, nil
}

func DecodeProjectListResult(result any) (ProjectListResult, error) {
	var projects ProjectListResult
	if err := decodeResult(result, &projects); err != nil {
		return ProjectListResult{}, err
	}
	return projects, nil
}

func DecodeProjectReadmeResult(result any) (ProjectReadmeResult, error) {
	var readme ProjectReadmeResult
	if err := decodeResult(result, &readme); err != nil {
		return ProjectReadmeResult{}, err
	}
	return readme, nil
}

func DecodeGitResult(result any) (GitResult, error) {
	var gitResult GitResult
	if err := decodeResult(result, &gitResult); err != nil {
		return GitResult{}, err
	}
	return gitResult, nil
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
