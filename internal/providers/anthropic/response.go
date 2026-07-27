package anthropic

// Wire decoding for the Anthropic /models response: the JSON shape and a helper
// that turns a successful HTTP response into it (or a descriptive error).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"petris.dev/toby/internal/diagnostic"
)

const (
	maxModelsResponseBytes = 2 << 20
	maxModelPages          = 32
	maxModels              = 10_000
	errorDrainBytes        = 32 << 10
)

type modelsResponse struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

func decodeModelsResponse(
	resp *http.Response,
	logger *diagnostic.Logger,
) (modelsResponse, error) {
	defer func() {
		logger.DebugError(
			"close Anthropic model response body",
			resp.Body.Close(),
		)
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, drainErr := io.Copy(
			io.Discard,
			io.LimitReader(resp.Body, errorDrainBytes),
		)
		logger.DebugError(
			"drain Anthropic error response body",
			drainErr,
			"http_status", resp.StatusCode,
		)
		return modelsResponse{}, fmt.Errorf(
			"anthropic model request returned HTTP %d",
			resp.StatusCode,
		)
	}

	body, err := io.ReadAll(io.LimitReader(
		resp.Body,
		maxModelsResponseBytes+1,
	))
	if err != nil {
		return modelsResponse{}, fmt.Errorf(
			"read Anthropic model response",
		)
	}
	if len(body) > maxModelsResponseBytes {
		return modelsResponse{}, fmt.Errorf(
			"anthropic model response exceeds its size limit",
		)
	}

	var payload modelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return modelsResponse{}, fmt.Errorf(
			"anthropic model response is invalid",
		)
	}
	if len(payload.Data) > maxModels {
		return modelsResponse{}, fmt.Errorf(
			"anthropic model response exceeds its model limit",
		)
	}

	return payload, nil
}
