package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"petris.dev/toby/control"
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

// handleGit dispatches a request through a git capability wired with resolver,
// mirroring how the host registry routes git.* methods.
func handleGit(t *testing.T, resolver *fakeResolver, request []byte) ([]byte, error) {
	t.Helper()
	service := New()
	service.SetResolver(resolver)
	router, err := control.NewRouter([]control.Capability{service})
	if err != nil {
		t.Fatal(err)
	}
	req, err := control.DecodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	return router.Handle(context.Background(), req)
}

func TestGitCommitCommitsStagedFilesOnly(t *testing.T) {
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
	response, err := handleGit(t, &fakeResolver{visible: map[string]string{"foo": repo}}, mustGitCommitRequest(t, "foo", "commit staged", false))
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

func TestGitCommitCanAmendPreviousCommit(t *testing.T) {
	requireGit(t)
	projectRoot := t.TempDir()
	repo := filepath.Join(projectRoot, "foo")
	initGitRepo(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "file.txt")
	runTestGit(t, repo, "commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(repo, "amended.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "amended.txt")

	response, err := handleGit(t, &fakeResolver{visible: map[string]string{"foo": repo}}, mustGitCommitRequest(t, "foo", "amended", true))
	if err != nil {
		t.Fatal(err)
	}
	result := mustGitResult(t, response)
	if result.ExitCode != 0 {
		t.Fatalf("git result = %#v", result)
	}
	if subject := strings.TrimSpace(runTestGit(t, repo, "log", "--format=%s", "-1")); subject != "amended" {
		t.Fatalf("commit subject = %q", subject)
	}
	if count := strings.TrimSpace(runTestGit(t, repo, "rev-list", "--count", "HEAD")); count != "1" {
		t.Fatalf("commit count = %s, want 1", count)
	}
}

func TestGitFetchDoesNotAdvanceHEAD(t *testing.T) {
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

	response, err := handleGit(t, &fakeResolver{visible: map[string]string{"foo": repo}}, mustGitFetchRequest(t, "foo"))
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

func TestGitRebaseRebasesOntoBase(t *testing.T) {
	requireGit(t)
	projectRoot := t.TempDir()
	repo := filepath.Join(projectRoot, "foo")
	initGitRepo(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "base.txt")
	runTestGit(t, repo, "commit", "-m", "base")
	runTestGit(t, repo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "feature.txt")
	runTestGit(t, repo, "commit", "-m", "feature commit")
	runTestGit(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "main.txt")
	runTestGit(t, repo, "commit", "-m", "main commit")
	mainHead := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD"))
	runTestGit(t, repo, "checkout", "feature")

	response, err := handleGit(t, &fakeResolver{visible: map[string]string{"foo": repo}}, mustGitRebaseRequest(t, "foo", "main", false, false))
	if err != nil {
		t.Fatal(err)
	}
	result := mustGitResult(t, response)
	if result.ExitCode != 0 {
		t.Fatalf("git result = %#v", result)
	}
	if base := strings.TrimSpace(runTestGit(t, repo, "merge-base", "HEAD", "main")); base != mainHead {
		t.Fatalf("merge base = %s, want %s", base, mainHead)
	}
	if subject := strings.TrimSpace(runTestGit(t, repo, "log", "--format=%s", "-1")); subject != "feature commit" {
		t.Fatalf("commit subject = %q", subject)
	}
}

func TestGitRebaseCanContinueAfterConflict(t *testing.T) {
	requireGit(t)
	projectRoot := t.TempDir()
	repo := filepath.Join(projectRoot, "foo")
	initGitRepo(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "conflict.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "conflict.txt")
	runTestGit(t, repo, "commit", "-m", "base")
	runTestGit(t, repo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "conflict.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "conflict.txt")
	runTestGit(t, repo, "commit", "-m", "feature change")
	runTestGit(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "conflict.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "conflict.txt")
	runTestGit(t, repo, "commit", "-m", "main change")
	runTestGit(t, repo, "checkout", "feature")

	resolver := &fakeResolver{visible: map[string]string{"foo": repo}}
	response, err := handleGit(t, resolver, mustGitRebaseRequest(t, "foo", "main", false, false))
	if err != nil {
		t.Fatal(err)
	}
	result := mustGitResult(t, response)
	if result.ExitCode == 0 {
		t.Fatalf("git result = %#v, want conflict", result)
	}
	if status := runTestGit(t, repo, "status", "--short"); status != "UU conflict.txt\n" {
		t.Fatalf("status = %q, want unresolved conflict", status)
	}
	if err := os.WriteFile(filepath.Join(repo, "conflict.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "conflict.txt")

	response, err = handleGit(t, resolver, mustGitRebaseRequest(t, "foo", "", true, false))
	if err != nil {
		t.Fatal(err)
	}
	result = mustGitResult(t, response)
	if result.ExitCode != 0 {
		t.Fatalf("git result = %#v", result)
	}
	if status := runTestGit(t, repo, "status", "--short"); status != "" {
		t.Fatalf("status = %q, want clean", status)
	}
	if subject := strings.TrimSpace(runTestGit(t, repo, "log", "--format=%s", "-1")); subject != "feature change" {
		t.Fatalf("commit subject = %q", subject)
	}
}

func TestGitRebaseCanAbortConflict(t *testing.T) {
	requireGit(t)
	projectRoot := t.TempDir()
	repo := filepath.Join(projectRoot, "foo")
	initGitRepo(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "conflict.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "conflict.txt")
	runTestGit(t, repo, "commit", "-m", "base")
	runTestGit(t, repo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "conflict.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "conflict.txt")
	runTestGit(t, repo, "commit", "-m", "feature change")
	featureHead := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD"))
	runTestGit(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "conflict.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "conflict.txt")
	runTestGit(t, repo, "commit", "-m", "main change")
	runTestGit(t, repo, "checkout", "feature")

	resolver := &fakeResolver{visible: map[string]string{"foo": repo}}
	response, err := handleGit(t, resolver, mustGitRebaseRequest(t, "foo", "main", false, false))
	if err != nil {
		t.Fatal(err)
	}
	result := mustGitResult(t, response)
	if result.ExitCode == 0 {
		t.Fatalf("git result = %#v, want conflict", result)
	}

	response, err = handleGit(t, resolver, mustGitRebaseRequest(t, "foo", "", false, true))
	if err != nil {
		t.Fatal(err)
	}
	result = mustGitResult(t, response)
	if result.ExitCode != 0 {
		t.Fatalf("git result = %#v", result)
	}
	if head := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD")); head != featureHead {
		t.Fatalf("HEAD = %s, want %s", head, featureHead)
	}
	if status := runTestGit(t, repo, "status", "--short"); status != "" {
		t.Fatalf("status = %q, want clean", status)
	}
}

func TestGitPushPushesOnlyRequestedBranch(t *testing.T) {
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

	response, err := handleGit(t, &fakeResolver{visible: map[string]string{"foo": repo}}, mustGitPushRequest(t, "foo", "feature", "", false))
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

func TestGitPushCanPushTags(t *testing.T) {
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
	runTestGit(t, repo, "tag", "-a", "v1.0.0", "-m", "release 1")
	featureHead := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD"))

	response, err := handleGit(t, &fakeResolver{visible: map[string]string{"foo": repo}}, mustGitPushRequest(t, "foo", "feature", "", true))
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
	if tagType := strings.TrimSpace(runCommand(t, "git", "--git-dir", remote, "cat-file", "-t", "refs/tags/v1.0.0")); tagType != "tag" {
		t.Fatalf("tag type = %s, want tag", tagType)
	}
}

func TestGitTagCreatesAnnotatedTag(t *testing.T) {
	requireGit(t)
	projectRoot := t.TempDir()
	repo := filepath.Join(projectRoot, "foo")
	initGitRepo(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "file.txt")
	runTestGit(t, repo, "commit", "-m", "initial")

	response, err := handleGit(t, &fakeResolver{visible: map[string]string{"foo": repo}}, mustGitTagRequest(t, "foo", "v1.0.0", "release 1", ""))
	if err != nil {
		t.Fatal(err)
	}
	result := mustGitResult(t, response)
	if result.ExitCode != 0 {
		t.Fatalf("git result = %#v", result)
	}
	if tagType := strings.TrimSpace(runTestGit(t, repo, "cat-file", "-t", "v1.0.0")); tagType != "tag" {
		t.Fatalf("tag type = %s, want tag", tagType)
	}
	if subject := strings.TrimSpace(runTestGit(t, repo, "for-each-ref", "refs/tags/v1.0.0", "--format=%(contents:subject)")); subject != "release 1" {
		t.Fatalf("tag subject = %q", subject)
	}
}

func TestGitRejectsDotSegmentRepository(t *testing.T) {
	response, err := handleGit(t, &fakeResolver{visible: map[string]string{"foo/../bar": t.TempDir()}}, mustGitFetchRequest(t, "foo/../bar"))
	if !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("err = %v, want EINVAL", err)
	}
	mustRPCErrorCode(t, response, control.CodeInvalidParams)
}

func TestGitRequiresVisibleRepository(t *testing.T) {
	response, err := handleGit(t, &fakeResolver{}, mustGitFetchRequest(t, "foo"))
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("err = %v, want EACCES", err)
	}
	mustRPCErrorCode(t, response, control.CodeProjectNotVisible)
}

func mustGitCommitRequest(t *testing.T, repository, message string, amend bool) []byte {
	t.Helper()
	request, err := NewCommitRequest(1, repository, message, amend)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func mustGitFetchRequest(t *testing.T, repository string) []byte {
	t.Helper()
	request, err := NewFetchRequest(1, repository)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func mustGitPushRequest(t *testing.T, repository, branch, origin string, tags bool) []byte {
	t.Helper()
	request, err := NewPushRequest(1, repository, branch, origin, tags)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func mustGitRebaseRequest(t *testing.T, repository, base string, continueRebase, abort bool) []byte {
	t.Helper()
	request, err := NewRebaseRequest(1, repository, base, continueRebase, abort)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func mustGitTagRequest(t *testing.T, repository, tag, message, target string) []byte {
	t.Helper()
	request, err := NewTagRequest(1, repository, tag, message, target)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func mustRPCErrorCode(t *testing.T, response []byte, code int) {
	t.Helper()
	decoded, err := control.DecodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Error == nil || decoded.Error.Code != code {
		t.Fatalf("response = %#v, want error code %d", decoded, code)
	}
}

func mustGitResult(t *testing.T, response []byte) Result {
	t.Helper()
	decoded, err := control.DecodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Error != nil {
		t.Fatalf("response error = %#v", decoded.Error)
	}
	result, err := DecodeResult(decoded.Result)
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
	runTestGit(t, repo, "config", "core.editor", "true")
	runTestGit(t, repo, "config", "commit.gpgsign", "false")
	runTestGit(t, repo, "config", "tag.gpgSign", "false")
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
