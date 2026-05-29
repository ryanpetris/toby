package control

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type fakeResolver struct {
	visible    map[string]string
	visibleErr error
}

func (m *fakeResolver) VisibleHostPath(repository string) (string, error) {
	if m.visibleErr != nil {
		return "", m.visibleErr
	}
	if path, ok := m.visible[repository]; ok {
		return path, nil
	}
	return "", errors.New("repository is not visible")
}

func TestManagerReturnsContextFiles(t *testing.T) {
	manager := &Manager{ContextFiles: []ContextFile{{Path: "GIT_AGENTS.md", Mode: 0o400, Data: []byte("git")}}}
	request, err := NewContextFilesRequest(1)
	if err != nil {
		t.Fatal(err)
	}
	response, err := manager.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	result, err := DecodeContextFilesResult(decoded.Result)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "GIT_AGENTS.md" || string(result.Files[0].Data) != "git" {
		t.Fatalf("context files = %#v", result.Files)
	}
}

func TestManagerGitCommitCommitsStagedFilesOnly(t *testing.T) {
	requireGit(t)
	projectRoot := t.TempDir()
	repo := filepath.Join(projectRoot, "foo")
	initGitRepo(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(repo, "unstaged.txt"), []byte("unstaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{RepositoryResolver: &fakeResolver{visible: map[string]string{"foo": repo}}}
	response, err := manager.Handle(context.Background(), mustGitCommitRequest(t, "foo", "commit staged"))
	if err != nil {
		t.Fatal(err)
	}
	result := mustGitResult(t, response)
	if result.ExitCode != 0 {
		t.Fatalf("git result = %#v", result)
	}
	if subject := strings.TrimSpace(runTestGit(t, repo, "log", "--format=%s", "-1")); subject != "commit staged" {
		t.Fatalf("commit subject = %q", subject)
	}
	status := runTestGit(t, repo, "status", "--short")
	if status != "?? unstaged.txt\n" {
		t.Fatalf("status = %q, want only unstaged.txt untracked", status)
	}
}

func TestManagerGitFetchDoesNotAdvanceHEAD(t *testing.T) {
	requireGit(t)
	projectRoot := t.TempDir()
	remote := filepath.Join(t.TempDir(), "remote.git")
	runCommand(t, "git", "init", "--bare", remote)
	repo := filepath.Join(projectRoot, "foo")
	initGitRepo(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "file.txt")
	runTestGit(t, repo, "commit", "-m", "initial")
	runTestGit(t, repo, "remote", "add", "origin", remote)
	runTestGit(t, repo, "push", "-u", "origin", "main")
	runCommand(t, "git", "--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/main")
	oldHead := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD"))

	updater := filepath.Join(t.TempDir(), "updater")
	runCommand(t, "git", "clone", remote, updater)
	configureGit(t, updater)
	if err := os.WriteFile(filepath.Join(updater, "file.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, updater, "add", "file.txt")
	runTestGit(t, updater, "commit", "-m", "remote update")
	runTestGit(t, updater, "push", "origin", "main")
	newHead := strings.TrimSpace(runTestGit(t, updater, "rev-parse", "HEAD"))

	manager := &Manager{RepositoryResolver: &fakeResolver{visible: map[string]string{"foo": repo}}}
	response, err := manager.Handle(context.Background(), mustGitFetchRequest(t, "foo"))
	if err != nil {
		t.Fatal(err)
	}
	result := mustGitResult(t, response)
	if result.ExitCode != 0 {
		t.Fatalf("git result = %#v", result)
	}
	if head := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD")); head != oldHead {
		t.Fatalf("HEAD = %s, want unchanged %s", head, oldHead)
	}
	if fetched := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "origin/main")); fetched != newHead {
		t.Fatalf("origin/main = %s, want %s", fetched, newHead)
	}
}

func TestManagerGitPushPushesOnlyRequestedBranch(t *testing.T) {
	requireGit(t)
	projectRoot := t.TempDir()
	remote := filepath.Join(t.TempDir(), "remote.git")
	runCommand(t, "git", "init", "--bare", remote)
	repo := filepath.Join(projectRoot, "foo")
	initGitRepo(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "file.txt")
	runTestGit(t, repo, "commit", "-m", "main commit")
	runTestGit(t, repo, "remote", "add", "origin", remote)
	runTestGit(t, repo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "feature.txt")
	runTestGit(t, repo, "commit", "-m", "feature commit")
	featureHead := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD"))

	manager := &Manager{RepositoryResolver: &fakeResolver{visible: map[string]string{"foo": repo}}}
	response, err := manager.Handle(context.Background(), mustGitPushRequest(t, "foo", "feature", ""))
	if err != nil {
		t.Fatal(err)
	}
	result := mustGitResult(t, response)
	if result.ExitCode != 0 {
		t.Fatalf("git result = %#v", result)
	}
	if pushed := strings.TrimSpace(runCommand(t, "git", "--git-dir", remote, "rev-parse", "refs/heads/feature")); pushed != featureHead {
		t.Fatalf("pushed feature = %s, want %s", pushed, featureHead)
	}
	if remoteRefExists(remote, "refs/heads/main") {
		t.Fatal("main branch was pushed, want only feature")
	}
}

func TestManagerGitRejectsDotSegmentRepository(t *testing.T) {
	manager := &Manager{RepositoryResolver: &fakeResolver{visible: map[string]string{"foo/../bar": t.TempDir()}}}
	response, err := manager.Handle(context.Background(), mustGitFetchRequest(t, "foo/../bar"))
	if !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("err = %v, want EINVAL", err)
	}
	mustRPCErrorCode(t, response, CodeInvalidParams)
}

func TestManagerGitRequiresVisibleRepository(t *testing.T) {
	manager := &Manager{RepositoryResolver: &fakeResolver{}}
	response, err := manager.Handle(context.Background(), mustGitFetchRequest(t, "foo"))
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("err = %v, want EACCES", err)
	}
	mustRPCErrorCode(t, response, CodeProjectNotVisible)
}

func mustGitCommitRequest(t *testing.T, repository, message string) []byte {
	t.Helper()
	request, err := NewGitCommitRequest(1, repository, message)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func mustGitFetchRequest(t *testing.T, repository string) []byte {
	t.Helper()
	request, err := NewGitFetchRequest(1, repository)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func mustGitPushRequest(t *testing.T, repository, branch, origin string) []byte {
	t.Helper()
	request, err := NewGitPushRequest(1, repository, branch, origin)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func mustRPCErrorCode(t *testing.T, response []byte, code int) {
	t.Helper()
	decoded, err := DecodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Error == nil || decoded.Error.Code != code {
		t.Fatalf("response = %#v, want error code %d", decoded, code)
	}
}

func mustGitResult(t *testing.T, response []byte) GitResult {
	t.Helper()
	decoded, err := DecodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Error != nil {
		t.Fatalf("response error = %#v", decoded.Error)
	}
	result, err := DecodeGitResult(decoded.Result)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
}

func initGitRepo(t *testing.T, repo string) {
	t.Helper()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "init")
	runTestGit(t, repo, "symbolic-ref", "HEAD", "refs/heads/main")
	configureGit(t, repo)
}

func configureGit(t *testing.T, repo string) {
	t.Helper()
	runTestGit(t, repo, "config", "user.name", "Toby Test")
	runTestGit(t, repo, "config", "user.email", "toby@example.invalid")
	runTestGit(t, repo, "config", "commit.gpgsign", "false")
}

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return runCommand(t, "git", append([]string{"-C", dir}, args...)...)
}

func runCommand(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func remoteRefExists(remote, ref string) bool {
	cmd := exec.Command("git", "--git-dir", remote, "show-ref", "--verify", "--quiet", ref)
	return cmd.Run() == nil
}
