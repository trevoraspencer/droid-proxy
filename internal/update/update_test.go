package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	lookErr map[string]error
	outputs map[string][]string
	errs    map[string]error
	calls   []string
}

func newFakeRunner(repo string) *fakeRunner {
	f := &fakeRunner{
		lookErr: map[string]error{},
		outputs: map[string][]string{},
		errs:    map[string]error{},
	}
	f.set("git", []string{"rev-parse", "--show-toplevel"}, repo+"\n")
	f.set("git", []string{"remote", "get-url", "origin"}, "https://github.com/trevoraspencer/droid-proxy.git\n")
	f.set("git", []string{"rev-parse", "--abbrev-ref", "HEAD"}, "main\n")
	f.set("git", []string{"status", "--porcelain=v1", "--untracked-files=normal"}, "")
	f.set("git", []string{"rev-parse", "--short=12", "HEAD"}, "before123456\n", "after1234567\n")
	f.set("git", []string{"fetch", "--prune", "origin", "main"}, "")
	f.set("git", []string{"merge", "--ff-only", "FETCH_HEAD"}, "")
	f.set("git", []string{"rev-parse", "HEAD"}, "after1234567890abcdef\n")
	return f
}

func (f *fakeRunner) LookPath(file string) (string, error) {
	if err := f.lookErr[file]; err != nil {
		return "", err
	}
	return "/usr/bin/" + file, nil
}

func (f *fakeRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	call := commandKey(name, args)
	f.calls = append(f.calls, call)
	if name == "go" && len(args) > 0 && args[0] == "build" {
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "-o" {
				if err := os.WriteFile(args[i+1], []byte("new droid-proxy binary"), 0o755); err != nil {
					return "", err
				}
				break
			}
		}
	}
	if err := f.errs[call]; err != nil {
		return "", err
	}
	values := f.outputs[call]
	if len(values) == 0 {
		return "", nil
	}
	out := values[0]
	if len(values) > 1 {
		f.outputs[call] = values[1:]
	}
	return out, nil
}

func (f *fakeRunner) set(name string, args []string, outputs ...string) {
	f.outputs[commandKey(name, args)] = outputs
}

func (f *fakeRunner) called(name string, args ...string) bool {
	want := commandKey(name, args)
	for _, call := range f.calls {
		if call == want {
			return true
		}
	}
	return false
}

func (f *fakeRunner) calledPrefix(prefix string) bool {
	for _, call := range f.calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

func commandKey(name string, args []string) string {
	return name + " " + strings.Join(args, "\x00")
}

func testRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "droid-proxy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module droid-proxy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunFastForwardBuildsBinary(t *testing.T) {
	repo := testRepo(t)
	binary := filepath.Join(t.TempDir(), "droid-proxy")
	if err := os.WriteFile(binary, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := newFakeRunner(repo)
	runner.set("git", []string{"rev-list", "--left-right", "--count", "HEAD...FETCH_HEAD"}, "0\t1\n")

	res, err := Run(context.Background(), Options{
		RepoPath:   repo,
		BinaryPath: binary,
		Runner:     runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated || !res.Built {
		t.Fatalf("Updated=%t Built=%t, want both true", res.Updated, res.Built)
	}
	if !runner.called("git", "fetch", "--prune", "origin", "main") {
		t.Fatal("expected fetch")
	}
	if !runner.called("git", "merge", "--ff-only", "FETCH_HEAD") {
		t.Fatal("expected fast-forward merge")
	}
	if !runner.calledPrefix("go build\x00-ldflags\x00-X github.com/trevoraspencer/droid-proxy/internal/version.Commit=after1234567890abcdef\x00-o") {
		t.Fatal("expected go build with commit ldflags")
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new droid-proxy binary" {
		t.Fatalf("binary content = %q", data)
	}
}

func TestRunAlreadyCurrentStillBuilds(t *testing.T) {
	repo := testRepo(t)
	binary := filepath.Join(t.TempDir(), "droid-proxy")
	runner := newFakeRunner(repo)
	runner.set("git", []string{"rev-list", "--left-right", "--count", "HEAD...FETCH_HEAD"}, "0 0\n")

	res, err := Run(context.Background(), Options{RepoPath: repo, BinaryPath: binary, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated {
		t.Fatal("did not expect Updated for already-current source")
	}
	if !res.Built {
		t.Fatal("expected rebuild even when already current")
	}
	if runner.called("git", "merge", "--ff-only", "FETCH_HEAD") {
		t.Fatal("did not expect merge when already current")
	}
}

func TestRunRefusesDirtyWorktree(t *testing.T) {
	repo := testRepo(t)
	runner := newFakeRunner(repo)
	runner.set("git", []string{"status", "--porcelain=v1", "--untracked-files=normal"}, "?? scratch.txt\n")

	_, err := Run(context.Background(), Options{RepoPath: repo, BinaryPath: filepath.Join(t.TempDir(), "bin"), Runner: runner})
	if err == nil || !strings.Contains(err.Error(), "local changes") {
		t.Fatalf("error = %v, want local changes refusal", err)
	}
	if runner.called("git", "fetch", "--prune", "origin", "main") {
		t.Fatal("dirty worktree should abort before fetch")
	}
}

func TestRunRefusesWrongCurrentBranch(t *testing.T) {
	repo := testRepo(t)
	runner := newFakeRunner(repo)
	runner.set("git", []string{"rev-parse", "--abbrev-ref", "HEAD"}, "feature\n")

	_, err := Run(context.Background(), Options{RepoPath: repo, BinaryPath: filepath.Join(t.TempDir(), "bin"), Runner: runner})
	if err == nil || !strings.Contains(err.Error(), "current branch") {
		t.Fatalf("error = %v, want current branch refusal", err)
	}
	if runner.called("git", "status", "--porcelain=v1", "--untracked-files=normal") {
		t.Fatal("wrong branch should abort before worktree status")
	}
}

func TestRunRefusesLocalAheadBranch(t *testing.T) {
	repo := testRepo(t)
	runner := newFakeRunner(repo)
	runner.set("git", []string{"rev-list", "--left-right", "--count", "HEAD...FETCH_HEAD"}, "2 0\n")

	_, err := Run(context.Background(), Options{RepoPath: repo, BinaryPath: filepath.Join(t.TempDir(), "bin"), Runner: runner})
	if err == nil || !strings.Contains(err.Error(), "ahead") {
		t.Fatalf("error = %v, want ahead refusal", err)
	}
	if runner.calledPrefix("go build") {
		t.Fatal("ahead branch should abort before build")
	}
}

func TestRunRefusesDivergedBranch(t *testing.T) {
	repo := testRepo(t)
	runner := newFakeRunner(repo)
	runner.set("git", []string{"rev-list", "--left-right", "--count", "HEAD...FETCH_HEAD"}, "1 3\n")

	_, err := Run(context.Background(), Options{RepoPath: repo, BinaryPath: filepath.Join(t.TempDir(), "bin"), Runner: runner})
	if err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("error = %v, want diverged refusal", err)
	}
	if runner.called("git", "merge", "--ff-only", "FETCH_HEAD") {
		t.Fatal("diverged branch should abort before merge")
	}
}

func TestRunRequiresGitAndGo(t *testing.T) {
	repo := testRepo(t)
	runner := newFakeRunner(repo)
	runner.lookErr["git"] = errors.New("not found")
	if _, err := Run(context.Background(), Options{RepoPath: repo, BinaryPath: filepath.Join(t.TempDir(), "bin"), Runner: runner}); err == nil || !strings.Contains(err.Error(), "git is required") {
		t.Fatalf("git error = %v", err)
	}

	runner = newFakeRunner(repo)
	runner.lookErr["go"] = errors.New("not found")
	if _, err := Run(context.Background(), Options{RepoPath: repo, BinaryPath: filepath.Join(t.TempDir(), "bin"), Runner: runner}); err == nil || !strings.Contains(err.Error(), "go is required") {
		t.Fatalf("go error = %v", err)
	}
}

func TestRunDryRunDoesNotMutate(t *testing.T) {
	repo := testRepo(t)
	runner := newFakeRunner(repo)
	res, err := Run(context.Background(), Options{
		RepoPath:   repo,
		BinaryPath: filepath.Join(t.TempDir(), "bin"),
		DryRun:     true,
		Runner:     runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun {
		t.Fatal("expected dry-run result")
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "fetch") || strings.Contains(call, "merge") || strings.HasPrefix(call, "go ") {
			t.Fatalf("dry run made mutating call %q", call)
		}
	}
}

func TestRunDryRunReportsDirtyWithoutFailing(t *testing.T) {
	repo := testRepo(t)
	runner := newFakeRunner(repo)
	runner.set("git", []string{"status", "--porcelain=v1", "--untracked-files=normal"}, "M README.md\n")

	res, err := Run(context.Background(), Options{
		RepoPath:   repo,
		BinaryPath: filepath.Join(t.TempDir(), "bin"),
		DryRun:     true,
		Runner:     runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.WorktreeDirty {
		t.Fatal("expected dirty worktree marker")
	}
	if runner.called("git", "fetch", "--prune", "origin", "main") {
		t.Fatal("dry-run should not fetch")
	}
}

func TestResolveRepoPathUsesCwdThenExecutableDir(t *testing.T) {
	cwdRepo := testRepo(t)
	exeRepo := testRepo(t)
	exe := filepath.Join(exeRepo, "droid-proxy")

	got, err := ResolveRepoPath("", cwdRepo, exe)
	if err != nil {
		t.Fatal(err)
	}
	if got != cwdRepo {
		t.Fatalf("ResolveRepoPath cwd = %q, want %q", got, cwdRepo)
	}

	got, err = ResolveRepoPath("", filepath.Join(t.TempDir(), "not-repo"), exe)
	if err != nil {
		t.Fatal(err)
	}
	if got != exeRepo {
		t.Fatalf("ResolveRepoPath exe dir = %q, want %q", got, exeRepo)
	}
}

func TestIsDroidProxyRemote(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/trevoraspencer/droid-proxy.git",
		"git@github.com:trevoraspencer/droid-proxy.git",
		"ssh://git@github.com/trevoraspencer/droid-proxy.git",
	} {
		if !IsDroidProxyRemote(raw) {
			t.Fatalf("%q should be accepted", raw)
		}
	}
	if IsDroidProxyRemote("https://github.com/other/droid-proxy.git") {
		t.Fatal("unexpectedly accepted wrong remote")
	}
}
