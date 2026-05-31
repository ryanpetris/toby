package toolutil

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"

	"petris.dev/toby/internal/providers/openai"
)

func GetJSON(ctx context.Context, client *http.Client, url, accept string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("User-Agent", openai.UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		details := strings.TrimSpace(string(body))
		if details == "" {
			details = resp.Status
		}
		return fmt.Errorf("request failed with HTTP %d: %s", resp.StatusCode, details)
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return err
	}
	return nil
}

func GoAssetArch(toolName string) (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64", nil
	case "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported platform for %s: %s", toolName, runtime.GOARCH)
	}
}

func LinuxAssetArch(toolName string) (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64", nil
	case "arm64":
		return "aarch64", nil
	default:
		return "", fmt.Errorf("unsupported platform for %s: %s", toolName, runtime.GOARCH)
	}
}

func RustTargetTriple(toolName string) (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64-unknown-linux-gnu", nil
	case "arm64":
		return "aarch64-unknown-linux-gnu", nil
	default:
		return "", fmt.Errorf("unsupported platform for %s: %s", toolName, runtime.GOARCH)
	}
}
