package approval

// Exercises authorization: policy resolution, held-request creation, and
// grant redemption.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/permission"
)

func testService(t *testing.T, config string) *Service {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte(config),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	base, err := appconfig.Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	holder := appconfig.NewLaunchHolder(base)

	return New(holder, nil)
}

func authorizeRequest() Request {
	return Request{
		Action:    "git.push",
		Name:      "Git push",
		Message:   "Push main to origin in repo",
		Default:   permission.RuleAsk,
		RequestID: json.RawMessage("1"),
		Params:    json.RawMessage(`{"repository":"repo"}`),
	}
}

func TestAuthorizeResolvesPolicy(t *testing.T) {
	allow := testService(t, "permissions:\n  actions:\n    git.push: allow\n")
	if err := allow.Authorize(t.Context(), authorizeRequest()); err != nil {
		t.Fatalf("allowed action error = %v", err)
	}

	deny := testService(t, "permissions:\n  actions:\n    git.push: deny\n")
	if err := deny.Authorize(t.Context(), authorizeRequest()); !errors.Is(err, ErrDenied) {
		t.Fatalf("denied action error = %v, want %v", err, ErrDenied)
	}

	yolo := testService(t, "settings:\n  yolo: true\n")
	if err := yolo.Authorize(t.Context(), authorizeRequest()); err != nil {
		t.Fatalf("yolo action error = %v", err)
	}
}

func TestAuthorizeHoldsAskOutcomes(t *testing.T) {
	service := testService(t, "")

	err := service.Authorize(t.Context(), authorizeRequest())
	var pending *PendingError
	if !errors.As(err, &pending) {
		t.Fatalf("ask outcome error = %v, want a pending error", err)
	}
	if pending.ID == "" || pending.Name != "Git push" {
		t.Fatalf("pending error = %+v", pending)
	}

	repeated := service.Authorize(t.Context(), authorizeRequest())
	var again *PendingError
	if !errors.As(repeated, &again) || again.ID != pending.ID {
		t.Fatalf("repeated ask error = %v, want pending %s", repeated, pending.ID)
	}

	held := service.Registry().Pending()
	if len(held) != 1 || held[0].Action != "git.push" {
		t.Fatalf("held records = %+v", held)
	}
	if string(held[0].Params) != `{"repository":"repo"}` {
		t.Fatalf("held params = %s", held[0].Params)
	}
}

func TestAuthorizeRedeemsGrants(t *testing.T) {
	service := testService(t, "")

	err := service.Authorize(t.Context(), authorizeRequest())
	var pending *PendingError
	if !errors.As(err, &pending) {
		t.Fatalf("ask outcome error = %v", err)
	}
	if _, _, _, err := service.Registry().Approve(pending.ID); err != nil {
		t.Fatal(err)
	}

	granted := WithGrant(t.Context(), pending.ID)
	if err := service.Authorize(granted, authorizeRequest()); err != nil {
		t.Fatalf("granted authorize error = %v", err)
	}
	if err := service.Authorize(granted, authorizeRequest()); !errors.Is(err, ErrDenied) {
		t.Fatalf("second granted authorize error = %v, want %v", err, ErrDenied)
	}

	forged := WithGrant(t.Context(), "unknown")
	if err := service.Authorize(forged, authorizeRequest()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forged grant error = %v, want %v", err, ErrNotFound)
	}
}
