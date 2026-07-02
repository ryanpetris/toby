// Starting a launch through the daemon: StartSession spawns/connects the daemon,
// asks it to bring the project up and return the foreground plan, runs the tool
// itself, and releases the session. The peer stays open for the tool's lifetime so
// daemon->client callbacks (approval prompts) can reach this client.

package client

import (
	"context"
	"encoding/json"
	"os"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/daemon/protocol"
	sandboxapi "petris.dev/toby/sandbox"
)

// StartSession runs one launch end to end and returns the tool's exit code.
func (s *Service) StartSession(ctx context.Context, params protocol.SessionStartParams) (int, error) {
	// The prompter holds the foreground's approval modal; the daemon's approval.prompt
	// callbacks are dispatched to it over this peer.
	prompt := newPrompter()
	peer, err := s.dial(ctx, true, approvalHandler(prompt))
	if err != nil {
		return 1, err
	}
	defer peer.Close()

	result, err := call[protocol.SessionStartResult](ctx, peer, protocol.MethodSessionStart, params)
	if err != nil {
		return 1, err
	}
	if result.InstallOnly {
		return 0, nil
	}

	code, runErr := s.runForeground(ctx, result.ContainerID, result.Managed, prompt.Set)

	// Always release, even if the foreground errored, so the project can fall idle.
	_, _ = call[struct{}](context.Background(), peer, protocol.MethodSessionRelease,
		protocol.SessionReleaseParams{SessionID: result.SessionID, ExitCode: code})

	return code, runErr
}

// approvalHandler dispatches daemon->client callbacks: approval.prompt is answered by
// the active foreground prompter; install.output notifications stream the shared home
// manager's install/exec output to this client's terminal.
func approvalHandler(prompt *prompter) control.Handler {
	return func(ctx context.Context, data []byte) ([]byte, error) {
		req, err := control.DecodeRequest(data)
		if err != nil {
			return control.ResponseError(nil, control.CodeInvalidRequest, err.Error(), nil), nil
		}
		switch req.Method {
		case protocol.MethodInstallOutput:
			var params protocol.InstallOutputParams
			if err := json.Unmarshal(req.Params, &params); err == nil {
				out := os.Stdout
				if params.Stream == "stderr" {
					out = os.Stderr
				}
				_, _ = out.Write(params.Data)
			}
			return nil, nil
		case protocol.MethodApprovalPrompt:
			var params protocol.ApprovalPromptParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), nil
			}
			allow, err := prompt.prompt(ctx, sandboxapi.ApprovalRequest{
				Action:  params.Action,
				Name:    params.Name,
				Message: params.Message,
			})
			if err != nil {
				return control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil), nil
			}
			return control.ResponseOK(req.ID, protocol.ApprovalPromptResult{Allow: allow}), nil
		default:
			return control.ResponseError(req.ID, control.CodeMethodNotFound, "method not found: "+req.Method, nil), nil
		}
	}
}
