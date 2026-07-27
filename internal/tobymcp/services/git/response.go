package gitservice

// Decodes an encoded host-action response while preserving logical JSON-RPC
// errors even when the live launch transport also reports an error.

import (
	"fmt"
	"io"

	"petris.dev/toby/internal/hostaction"
	"petris.dev/toby/internal/hostaction/methods/git"
)

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
		return git.Result{}, decoded.Error
	}
	if callErr != nil {
		return git.Result{}, callErr
	}

	return git.DecodeResult(decoded.Result)
}
