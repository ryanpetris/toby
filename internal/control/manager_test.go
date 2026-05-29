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

	"petris.dev/toby/fusekit"
)

type fakeMounter struct {
	paths      []string
	virtual    string
	err        error
	visible    map[string]string
	visibleErr error
}

func (m *fakeMounter) AddHostPath(path string) (string, error) {
	m.paths = append(m.paths, path)
	if m.virtual != "" {
		return m.virtual, m.err
	}
	return path, m.err
}

func (m *fakeMounter) VisibleHostPath(repository string) (string, error) {
	if m.visibleErr != nil {
		return "", m.visibleErr
	}
	if path, ok := m.visible[repository]; ok {
		return path, nil
	}
	return "", errors.New("repository is not visible")
}

type fakeConfirmer struct {
	approved bool
	err      error
}

func (c fakeConfirmer) ConfirmMount(context.Context, Request) (bool, error) {
	return c.approved, c.err
}

func TestManagerProjectMountApprovesAndMountsPath(t *testing.T) {
	projectRoot := t.TempDir()
	projectDir := filepath.Join(projectRoot, "foo")
	if err := os.Mkdir(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mounter := &fakeMounter{virtual: "/foo"}
	manager := &Manager{Mounter: mounter, Confirmer: fakeConfirmer{approved: true}, ProjectRoot: projectRoot, MountableProjects: true}
	response, err := manager.Handle(context.Background(), mustProjectMountRequest(t, "foo"))
	if err != nil {
		t.Fatal(err)
	}
	if len(mounter.paths) != 1 || mounter.paths[0] != projectDir {
		t.Fatalf("paths = %#v", mounter.paths)
	}
	result := mustMountResult(t, response)
	if result.HostPath != projectDir || result.SandboxPath != projectDir || result.VirtualPath != "/foo" {
		t.Fatalf("result = %#v", result)
	}
}

func TestManagerDeniedRequestReturnsEACCES(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectRoot, "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	mounter := &fakeMounter{}
	manager := &Manager{Mounter: mounter, Confirmer: fakeConfirmer{approved: false}, ProjectRoot: projectRoot, MountableProjects: true}
	response, err := manager.Handle(context.Background(), mustProjectMountRequest(t, "foo"))
	if fusekit.ErrnoOf(err) != syscall.EACCES {
		t.Fatalf("err = %v, want EACCES", err)
	}
	mustRPCErrorCode(t, response, CodeDenied)
	if len(mounter.paths) != 0 {
		t.Fatalf("paths = %#v, want none", mounter.paths)
	}
}
func TestTmuxConfirmerRequiresTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	projectRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectRoot, "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Mounter: &fakeMounter{}, Confirmer: TmuxConfirmer{}, ProjectRoot: projectRoot, MountableProjects: true}
	response, err := manager.Handle(context.Background(), mustProjectMountRequest(t, "foo"))
	if fusekit.ErrnoOf(err) != syscall.ENOTSUP {
		t.Fatalf("err = %v, want ENOTSUP", err)
	}
	mustRPCErrorCode(t, response, CodeTmuxRequired)
}

func TestManagerProjectListListsProjectDirectories(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectRoot, "chirp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "not-a-project"), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{ProjectRoot: projectRoot, MountableProjects: true}
	request, err := NewProjectListRequest(1)
	if err != nil {
		t.Fatal(err)
	}
	response, err := manager.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	result := mustProjectListResult(t, response)
	if result.ProjectRoot != projectRoot || len(result.Projects) != 1 || result.Projects[0].Name != "chirp" {
		t.Fatalf("result = %#v", result)
	}
}

func TestManagerProjectReadmeReadsProjectReadme(t *testing.T) {
	projectRoot := t.TempDir()
	projectDir := filepath.Join(projectRoot, "chirp")
	if err := os.Mkdir(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "README.md"), []byte("# Chirp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{ProjectRoot: projectRoot, MountableProjects: true}
	request, err := NewProjectReadmeRequest(1, "chirp")
	if err != nil {
		t.Fatal(err)
	}
	response, err := manager.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	result := mustProjectReadmeResult(t, response)
	if result.Name != "chirp" || result.Content != "# Chirp\n" || result.Path != filepath.Join(projectDir, "README.md") {
		t.Fatalf("result = %#v", result)
	}
}

func TestManagerProjectMountOnlyMountsProjectDirectories(t *testing.T) {
	projectRoot := t.TempDir()
	projectDir := filepath.Join(projectRoot, "chirp")
	if err := os.Mkdir(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mounter := &fakeMounter{virtual: "/projects/chirp"}
	manager := &Manager{Mounter: mounter, Confirmer: fakeConfirmer{approved: true}, ProjectRoot: projectRoot, MountableProjects: true}
	request, err := NewProjectMountRequest(1, "chirp")
	if err != nil {
		t.Fatal(err)
	}
	response, err := manager.Handle(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	result := mustMountResult(t, response)
	if len(mounter.paths) != 1 || mounter.paths[0] != projectDir {
		t.Fatalf("paths = %#v, want %q", mounter.paths, projectDir)
	}
	if result.HostPath != projectDir || result.VirtualPath != "/projects/chirp" {
		t.Fatalf("result = %#v", result)
	}
}

func TestManagerProjectMountRejectsNonProjectPath(t *testing.T) {
	manager := &Manager{Mounter: &fakeMounter{}, Confirmer: fakeConfirmer{approved: true}, ProjectRoot: t.TempDir(), MountableProjects: true}
	request, err := NewProjectMountRequest(1, "../secret")
	if err != nil {
		t.Fatal(err)
	}
	response, err := manager.Handle(context.Background(), request)
	if fusekit.ErrnoOf(err) != syscall.EINVAL {
		t.Fatalf("err = %v, want EINVAL", err)
	}
	mustRPCErrorCode(t, response, CodeInvalidParams)
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
	manager := &Manager{Mounter: &fakeMounter{visible: map[string]string{"foo": repo}}}
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

	manager := &Manager{Mounter: &fakeMounter{visible: map[string]string{"foo": repo}}}
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

	manager := &Manager{Mounter: &fakeMounter{visible: map[string]string{"foo": repo}}}
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
	manager := &Manager{Mounter: &fakeMounter{visible: map[string]string{"foo/../bar": t.TempDir()}}}
	response, err := manager.Handle(context.Background(), mustGitFetchRequest(t, "foo/../bar"))
	if fusekit.ErrnoOf(err) != syscall.EINVAL {
		t.Fatalf("err = %v, want EINVAL", err)
	}
	mustRPCErrorCode(t, response, CodeInvalidParams)
}

func TestManagerGitRequiresVisibleRepository(t *testing.T) {
	manager := &Manager{Mounter: &fakeMounter{}}
	response, err := manager.Handle(context.Background(), mustGitFetchRequest(t, "foo"))
	if fusekit.ErrnoOf(err) != syscall.EACCES {
		t.Fatalf("err = %v, want EACCES", err)
	}
	mustRPCErrorCode(t, response, CodeProjectNotVisible)
}

func mustProjectMountRequest(t *testing.T, name string) []byte {
	t.Helper()
	request, err := NewProjectMountRequest(1, name)
	if err != nil {
		t.Fatal(err)
	}
	return request
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

func mustMountResult(t *testing.T, response []byte) MountResult {
	t.Helper()
	decoded, err := DecodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Error != nil {
		t.Fatalf("response error = %#v", decoded.Error)
	}
	result, err := DecodeMountResult(decoded.Result)
	if err != nil {
		t.Fatal(err)
	}
	return result
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

func mustProjectListResult(t *testing.T, response []byte) ProjectListResult {
	t.Helper()
	decoded, err := DecodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Error != nil {
		t.Fatalf("response error = %#v", decoded.Error)
	}
	result, err := DecodeProjectListResult(decoded.Result)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustProjectReadmeResult(t *testing.T, response []byte) ProjectReadmeResult {
	t.Helper()
	decoded, err := DecodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Error != nil {
		t.Fatalf("response error = %#v", decoded.Error)
	}
	result, err := DecodeProjectReadmeResult(decoded.Result)
	if err != nil {
		t.Fatal(err)
	}
	return result
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
