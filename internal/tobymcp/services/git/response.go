package gitservice

// Decodes an encoded host-action response while preserving logical JSON-RPC
// errors even when the live launch transport also reports an error. An
// approval-required error decodes into a typed pending error so the tool
// handler can shape it as a normal result.

import (
	"fmt"
	"io"

	"petris.dev/toby/internal/hostaction"
	"petris.dev/toby/internal/hostaction/methods/git"
)

// approvalPendingError carries an approval-required response through the
// reverse client to the tool handler.
type approvalPendingError struct {
	data hostaction.ApprovalRequiredData
}

// Error returns the human-readable failure message.
func (e *approvalPendingError) Error() string {
	return fmt.Sprintf(
		"approval %s required: %s (%s)",
		e.data.ApprovalID,
		e.data.Name,
		e.data.Message,
	)
}

func decodeResponse(response []byte, callErr error) (git.Result, error) {
	if len(response) == 0 {
		if callErr != nil {
			return git.Result{}, callErr
		}
		return git.Result{}, io.ErrUnexpectedEOF
	}

	decoded, err := hostaction.DecodeResponse(response)
	if err != nil {
		if callErr != nil {
			return git.Result{}, fmt.Errorf("%w; decode response: %v", callErr, err)
		}
		return git.Result{}, err
	}
	if decoded.Error != nil {
		if decoded.Error.Code == hostaction.CodeApprovalRequired {
			data, dataErr := hostaction.DecodeApprovalRequiredData(
				decoded.Error.Data,
			)
			if dataErr != nil {
				return git.Result{}, fmt.Errorf(
					"%s; parse approval data: %v",
					decoded.Error.Message,
					dataErr,
				)
			}
			return git.Result{}, &approvalPendingError{data: data}
		}
		return git.Result{}, decoded.Error
	}
	if callErr != nil {
		return git.Result{}, callErr
	}

	return git.DecodeResult(decoded.Result)
}

// DecodeToolResponse decodes an encoded git.* host-action response into its
// result for callers outside the reverse client, such as the approvals wait
// tool rendering a saved response.
func DecodeToolResponse(response []byte) (git.Result, error) {
	return decodeResponse(response, nil)
}
