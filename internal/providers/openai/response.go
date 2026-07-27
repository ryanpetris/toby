package openai

// Boundedly decodes the OpenAI-compatible /models response without retaining
// or returning provider-controlled error bodies.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"petris.dev/toby/internal/diagnostic"
)

const (
	maxModelsResponseBytes = 2 << 20
	maxModelsPerResponse   = 10_000
	errorDrainBytes        = 32 << 10
)

type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func decodeModelsResponse(
	response *http.Response,
	logger *diagnostic.Logger,
) (modelsResponse, error) {
	defer func() {
		logger.DebugError(
			"close OpenAI model response body",
			response.Body.Close(),
		)
	}()

	if response.StatusCode < 200 ||
		response.StatusCode >= 300 {
		_, drainErr := io.Copy(
			io.Discard,
			io.LimitReader(response.Body, errorDrainBytes),
		)
		logger.DebugError(
			"drain OpenAI error response body",
			drainErr,
			"http_status", response.StatusCode,
		)
		return modelsResponse{}, fmt.Errorf(
			"OpenAI model request returned HTTP %d",
			response.StatusCode,
		)
	}

	body, err := io.ReadAll(io.LimitReader(
		response.Body,
		maxModelsResponseBytes+1,
	))
	if err != nil {
		return modelsResponse{}, fmt.Errorf(
			"read OpenAI model response",
		)
	}
	if len(body) > maxModelsResponseBytes {
		return modelsResponse{}, fmt.Errorf(
			"OpenAI model response exceeds its size limit",
		)
	}

	var payload modelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return modelsResponse{}, fmt.Errorf(
			"OpenAI model response is invalid",
		)
	}
	if len(payload.Data) > maxModelsPerResponse {
		return modelsResponse{}, fmt.Errorf(
			"OpenAI model response exceeds its model limit",
		)
	}

	return payload, nil
}
