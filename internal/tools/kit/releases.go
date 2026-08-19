package kit

// Fetching release metadata over HTTP and mapping the host platform to release
// asset architecture strings (Go, Linux, and Rust target-triple conventions).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/version"
)

const (
	jsonRetryAttempts = 3
	jsonRetryMin      = 100 * time.Millisecond
)

// DiagnosticLogger reports non-fatal release metadata cleanup errors.
type DiagnosticLogger interface {
	// DebugError emits a debug diagnostic with structured attributes.
	DebugError(string, error, ...any)
}

type httpStatusError struct {
	status  int
	details string
}

func (e httpStatusError) Error() string {
	return fmt.Sprintf("request failed with HTTP %d: %s", e.status, e.details)
}

// GetJSON retrieves and decodes a JSON document with Toby's user agent.
func GetJSON(
	ctx context.Context,
	client *http.Client,
	logger DiagnosticLogger,
	url, accept string,
	target any,
) error {
	delay := jsonRetryMin
	var lastErr error
	for attempt := 1; attempt <= jsonRetryAttempts; attempt++ {
		err := getJSONOnce(ctx, client, logger, url, accept, target)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryableJSONError(err) || attempt == jsonRetryAttempts {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay *= 2
	}
	return lastErr
}

func getJSONOnce(
	ctx context.Context,
	client *http.Client,
	logger DiagnosticLogger,
	url, accept string,
	target any,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("User-Agent", version.UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		err := resp.Body.Close()
		if logger != nil {
			logger.DebugError(
				"close release metadata response body",
				err,
				"url", url,
			)
			return
		}
		diagnostic.DiscardError(
			"no diagnostic logger is available",
			"close release metadata response body",
			err,
			"url", url,
		)
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if readErr != nil {
			return fmt.Errorf(
				"request failed with HTTP %d and its response body could not be read: %w",
				resp.StatusCode,
				readErr,
			)
		}
		details := strings.TrimSpace(string(body))
		if details == "" {
			details = resp.Status
		}
		return httpStatusError{status: resp.StatusCode, details: details}
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return err
	}
	return nil
}

func retryableJSONError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var statusErr httpStatusError
	if errors.As(err, &statusErr) {
		return statusErr.status == http.StatusTooManyRequests ||
			statusErr.status >= 500
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// GoAssetArch returns the Go release architecture name for the host.
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

// LinuxAssetArch returns the common Linux asset architecture name for the host.
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

// RustTargetTriple returns the Rust Linux target triple for the host.
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
