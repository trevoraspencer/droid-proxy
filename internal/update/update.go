package update

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultRemote = "origin"
	DefaultBranch = "main"

	expectedRemote = "github.com/trevoraspencer/droid-proxy"
)

// Runner abstracts command execution so update safety behavior can be tested
// without running git or go.
type Runner interface {
	LookPath(file string) (string, error)
	Run(ctx context.Context, dir, name string, args ...string) (string, error)
}

// OSRunner runs commands on the local system.
type OSRunner struct{}

func (OSRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (OSRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			return text, fmt.Errorf("%s %s: %s: %w", name, strings.Join(args, " "), trimmed, err)
		}
		return text, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return text, nil
}

type Options struct {
	RepoPath   string
	Remote     string
	Branch     string
	BinaryPath string
	DryRun     bool
	Runner     Runner
}

type Result struct {
	RepoPath      string
	Remote        string
	Branch        string
	RemoteURL     string
	BinaryPath    string
	BeforeCommit  string
	AfterCommit   string
	Updated       bool
	Built         bool
	DryRun        bool
	WorktreeDirty bool
}

func Run(ctx context.Context, opts Options) (Result, error) {
	if opts.Remote == "" {
		opts.Remote = DefaultRemote
	}
	if opts.Branch == "" {
		opts.Branch = DefaultBranch
	}
	runner := opts.Runner
	if runner == nil {
		runner = OSRunner{}
	}

	repo, err := filepath.Abs(opts.RepoPath)
	if err != nil {
		return Result{}, fmt.Errorf("repo path: %w", err)
	}
	binary, err := filepath.Abs(opts.BinaryPath)
	if err != nil {
		return Result{}, fmt.Errorf("binary path: %w", err)
	}
	res := Result{
		RepoPath:   repo,
		Remote:     opts.Remote,
		Branch:     opts.Branch,
		BinaryPath: binary,
		DryRun:     opts.DryRun,
	}

	if _, err := runner.LookPath("git"); err != nil {
		return res, fmt.Errorf("git is required for droid-proxy update: %w", err)
	}
	if _, err := runner.LookPath("go"); err != nil {
		return res, fmt.Errorf("go is required to rebuild droid-proxy: %w", err)
	}
	if err := ValidateRepo(repo); err != nil {
		return res, err
	}
	top, err := runTrim(ctx, runner, repo, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return res, fmt.Errorf("repo is not a git checkout: %w", err)
	}
	if filepath.Clean(top) != filepath.Clean(repo) {
		return res, fmt.Errorf("repo path %s is inside %s; pass the repository root", repo, top)
	}

	remoteURL, err := runTrim(ctx, runner, repo, "git", "remote", "get-url", opts.Remote)
	if err != nil {
		return res, fmt.Errorf("git remote %q is required: %w", opts.Remote, err)
	}
	res.RemoteURL = remoteURL
	if !IsDroidProxyRemote(remoteURL) {
		return res, fmt.Errorf("remote %q points to %q, want github.com/trevoraspencer/droid-proxy", opts.Remote, remoteURL)
	}
	currentBranch, err := runTrim(ctx, runner, repo, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return res, fmt.Errorf("reading current git branch: %w", err)
	}
	if currentBranch != opts.Branch {
		return res, fmt.Errorf("current branch is %q, want %q for update", currentBranch, opts.Branch)
	}

	status, err := runTrim(ctx, runner, repo, "git", "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return res, fmt.Errorf("checking worktree status: %w", err)
	}
	if status != "" {
		res.WorktreeDirty = true
		if opts.DryRun {
			return res, nil
		}
		return res, fmt.Errorf("worktree has local changes; commit, stash, or remove them before updating")
	}

	before, err := runTrim(ctx, runner, repo, "git", "rev-parse", "--short=12", "HEAD")
	if err == nil {
		res.BeforeCommit = before
		res.AfterCommit = before
	}
	if opts.DryRun {
		return res, nil
	}

	if _, err := runner.Run(ctx, repo, "git", "fetch", "--prune", opts.Remote, opts.Branch); err != nil {
		return res, fmt.Errorf("fetching %s/%s: %w", opts.Remote, opts.Branch, err)
	}
	counts, err := runTrim(ctx, runner, repo, "git", "rev-list", "--left-right", "--count", "HEAD...FETCH_HEAD")
	if err != nil {
		return res, fmt.Errorf("comparing local branch to %s/%s: %w", opts.Remote, opts.Branch, err)
	}
	ahead, behind, err := parseAheadBehind(counts)
	if err != nil {
		return res, err
	}
	switch {
	case ahead > 0 && behind > 0:
		return res, fmt.Errorf("local branch has %d local commit(s) and %d remote commit(s); refusing to update a diverged checkout", ahead, behind)
	case ahead > 0:
		return res, fmt.Errorf("local branch is ahead of %s/%s by %d commit(s); refusing to overwrite local work", opts.Remote, opts.Branch, ahead)
	case behind > 0:
		if _, err := runner.Run(ctx, repo, "git", "merge", "--ff-only", "FETCH_HEAD"); err != nil {
			return res, fmt.Errorf("fast-forwarding to %s/%s: %w", opts.Remote, opts.Branch, err)
		}
		res.Updated = true
	}

	commit, err := runTrim(ctx, runner, repo, "git", "rev-parse", "HEAD")
	if err != nil {
		return res, fmt.Errorf("reading updated commit: %w", err)
	}
	short, err := runTrim(ctx, runner, repo, "git", "rev-parse", "--short=12", "HEAD")
	if err == nil {
		res.AfterCommit = short
	}
	if err := buildBinary(ctx, runner, repo, binary, commit); err != nil {
		return res, err
	}
	res.Built = true
	return res, nil
}

func runTrim(ctx context.Context, runner Runner, dir, name string, args ...string) (string, error) {
	out, err := runner.Run(ctx, dir, name, args...)
	return strings.TrimSpace(out), err
}

func parseAheadBehind(raw string) (int, int, error) {
	fields := strings.Fields(raw)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", raw)
	}
	ahead, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, fmt.Errorf("unexpected local commit count %q", fields[0])
	}
	behind, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("unexpected remote commit count %q", fields[1])
	}
	return ahead, behind, nil
}

func buildBinary(ctx context.Context, runner Runner, repo, binary, commit string) error {
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		return fmt.Errorf("creating binary directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(binary), ".droid-proxy-update-*")
	if err != nil {
		return fmt.Errorf("creating temporary binary: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing temporary binary: %w", err)
	}
	if err := os.Remove(tmpPath); err != nil {
		return fmt.Errorf("preparing temporary binary: %w", err)
	}
	defer os.Remove(tmpPath)

	ldflags := fmt.Sprintf("-X droid-proxy/internal/version.Commit=%s", commit)
	if _, err := runner.Run(ctx, repo, "go", "build", "-ldflags", ldflags, "-o", tmpPath, "./cmd/droid-proxy"); err != nil {
		return fmt.Errorf("building updated droid-proxy: %w", err)
	}
	if err := os.Rename(tmpPath, binary); err != nil {
		return fmt.Errorf("installing updated binary: %w", err)
	}
	return nil
}

func ValidateRepo(repo string) error {
	if _, err := os.Stat(filepath.Join(repo, "cmd", "droid-proxy")); err != nil {
		return fmt.Errorf("%s is not a droid-proxy source checkout: missing cmd/droid-proxy", repo)
	}
	data, err := os.ReadFile(filepath.Join(repo, "go.mod"))
	if err != nil {
		return fmt.Errorf("%s is not a droid-proxy source checkout: missing go.mod", repo)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "module droid-proxy" {
			return nil
		}
	}
	return fmt.Errorf("%s is not a droid-proxy source checkout: go.mod module is not droid-proxy", repo)
}

func ResolveRepoPath(explicit, cwd, executable string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	if cwd != "" && ValidateRepo(cwd) == nil {
		return filepath.Abs(cwd)
	}
	if executable != "" {
		if dir := filepath.Dir(executable); ValidateRepo(dir) == nil {
			return filepath.Abs(dir)
		}
	}
	return "", fmt.Errorf("could not find droid-proxy source checkout; run from the repo root or pass --repo")
}

func ResolveBinaryPath(explicit, executable string) (string, error) {
	path := explicit
	if path == "" {
		path = executable
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("could not resolve current executable; pass --binary")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("binary path: %w", err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return abs, nil
}

func IsDroidProxyRemote(raw string) bool {
	return normalizeRemote(raw) == expectedRemote
}

func normalizeRemote(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	if strings.HasPrefix(s, "git@github.com:") {
		return "github.com/" + strings.TrimPrefix(s, "git@github.com:")
	}
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		host := strings.ToLower(u.Host)
		path := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
		return host + "/" + path
	}
	return s
}
