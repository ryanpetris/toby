package anthropic

// Wire decoding for the Anthropic /models response: the JSON shape and a helper
// that turns a successful HTTP response into it (or a descriptive error).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type modelsResponse struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

func decodeModelsResponse(resp *http.Response) (modelsResponse, error) {
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		details := strings.TrimSpace(string(body))
		if details == "" {
			details = resp.Status
		}
		return modelsResponse{}, fmt.Errorf("request failed with HTTP %d: %s", resp.StatusCode, details)
	}

	var payload modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return modelsResponse{}, err
	}

	return payload, nil
}
