/*
Copyright 2025 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package git

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/pkg/errors"
)

const (
	// envCacheDir overrides the cache root location. Point it at a dedicated
	// writable volume (e.g. an emptyDir) to keep cached repos off the node's
	// shared /tmp.
	envCacheDir = "PROVIDER_KUBECONFIG_CACHE_DIR"
	// envMaxCacheEntries overrides the maximum number of cached repo
	// directories retained before least-recently-used eviction kicks in.
	envMaxCacheEntries = "PROVIDER_KUBECONFIG_CACHE_MAX_ENTRIES"
	// defaultMaxCacheEntries bounds the cache so a long-running pod that
	// reconciles many distinct repos does not grow the cache without limit.
	defaultMaxCacheEntries = 32
	// cacheDirPerm keeps cached repo contents (kubeconfigs, .git) unreadable by
	// other users sharing the pod.
	cacheDirPerm = 0o700
)

var (
	// muMap guards concurrent access to the same repo cache directory.
	muMap   = make(map[string]*sync.Mutex)
	muMapMu sync.Mutex
)

// cacheRoot returns the root directory under which per-repo caches live. It
// prefers an explicit override, then the user cache dir, falling back to the
// system temp dir. The root is always created with 0700 permissions.
func cacheRoot() string {
	if v := os.Getenv(envCacheDir); v != "" {
		return v
	}
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "provider-kubeconfig")
	}
	return filepath.Join(os.TempDir(), "provider-kubeconfig")
}

// ensureCacheRoot creates the cache root with private permissions, enforcing
// 0700 even when the process umask would otherwise widen it.
func ensureCacheRoot(root string) error {
	if err := os.MkdirAll(root, cacheDirPerm); err != nil {
		return errors.Wrap(err, "cannot create cache root directory")
	}
	if err := os.Chmod(root, cacheDirPerm); err != nil {
		return errors.Wrap(err, "cannot set cache root permissions")
	}
	return nil
}

// maxCacheEntries returns the configured cache entry cap, or the default.
func maxCacheEntries() int {
	if v := os.Getenv(envMaxCacheEntries); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxCacheEntries
}

type cacheEntry struct {
	path string
	mod  time.Time
}

// cacheEntriesByAge lists the subdirectories of root, sorted least-recently-used
// (oldest mtime) first. Returns nil on any read error.
func cacheEntriesByAge(root string) []cacheEntry {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	dirs := make([]cacheEntry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, cacheEntry{path: filepath.Join(root, e.Name()), mod: info.ModTime()})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].mod.Before(dirs[j].mod) })
	return dirs
}

// evictCache bounds root to at most maxEntries directories, removing the
// least-recently-used entries beyond the limit. Directories currently locked by
// an in-flight reconcile are skipped (TryLock), and keep is never removed.
// Best-effort: any error leaves the cache as-is.
func evictCache(root, keep string, maxEntries int) {
	dirs := cacheEntriesByAge(root)
	excess := len(dirs) - maxEntries
	if excess <= 0 {
		return
	}

	for _, d := range dirs {
		if excess <= 0 {
			break
		}
		if d.path == keep {
			continue
		}
		mu := repoMutex(d.path)
		if !mu.TryLock() {
			continue // another reconcile is using this directory
		}
		if err := os.RemoveAll(d.path); err == nil {
			excess--
		}
		mu.Unlock()
	}
}

// touch updates a directory's mtime so recently-used caches sort as newest for
// eviction purposes (LRU).
func touch(path string) {
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

func repoMutex(key string) *sync.Mutex {
	muMapMu.Lock()
	defer muMapMu.Unlock()
	if mu, ok := muMap[key]; ok {
		return mu
	}
	mu := &sync.Mutex{}
	muMap[key] = mu
	return mu
}

// Operation describes which git action EnsureCloned performed, so callers can
// distinguish a fresh clone from a cache-hit pull or a pinned-revision checkout
// (e.g. for metrics) without importing any observability dependency here.
type Operation string

const (
	// OpClone means the repo was (re)cloned from scratch.
	OpClone Operation = "clone"
	// OpPull means an existing cache was updated via pull.
	OpPull Operation = "pull"
	// OpRevision means a pinned revision was ensured (full clone or cache hit).
	OpRevision Operation = "revision"
)

// Repo manages cloning and pulling a Git repository to a local cache directory.
type Repo struct {
	url      string
	branch   string
	revision string
	token    string
	cacheDir string
}

// NewRepo creates a Repo. The cache directory is derived deterministically from
// the URL, branch and revision, so a pinned-revision checkout never collides
// with the branch-tip cache for the same repo.
func NewRepo(url, branch, revision, token string) *Repo {
	if branch == "" {
		branch = "main"
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(url+"@"+branch+"#"+revision)))[:16]
	cacheDir := filepath.Join(cacheRoot(), hash)
	return &Repo{url: url, branch: branch, revision: revision, token: token, cacheDir: cacheDir}
}

func (r *Repo) auth() *http.BasicAuth {
	if r.token == "" {
		return nil
	}
	return &http.BasicAuth{
		Username: "x-access-token",
		Password: r.token,
	}
}

// EnsureCloned clones the repo if not cached, or pulls latest if it is. When a
// revision is pinned it delegates to ensureRevision, which checks out the exact
// commit/tag instead of tracking the branch tip. On success it marks the cache
// as recently used and evicts least-recently-used entries beyond the cap.
func (r *Repo) EnsureCloned(ctx context.Context) (string, Operation, error) {
	mu := repoMutex(r.cacheDir)
	mu.Lock()
	defer mu.Unlock()

	dir, op, err := r.ensure(ctx)
	if err != nil {
		return "", op, err
	}

	touch(dir)
	evictCache(filepath.Dir(r.cacheDir), r.cacheDir, maxCacheEntries())
	return dir, op, nil
}

// ensure performs the clone/pull (or pinned-revision checkout) and returns the
// cache directory and which Operation it performed, without the LRU bookkeeping
// handled by EnsureCloned.
func (r *Repo) ensure(ctx context.Context) (string, Operation, error) {
	if r.revision != "" {
		dir, err := r.ensureRevision(ctx)
		return dir, OpRevision, err
	}

	refName := plumbing.NewBranchReferenceName(r.branch)

	if _, err := os.Stat(filepath.Join(r.cacheDir, ".git")); err == nil {
		dir, pullErr := r.pull(ctx, refName)
		if pullErr == nil {
			return dir, OpPull, nil
		}
		// Pull failed (e.g. stale shallow clone) — remove cache and re-clone
		_ = os.RemoveAll(r.cacheDir)
	}

	if err := ensureCacheRoot(filepath.Dir(r.cacheDir)); err != nil {
		return "", OpClone, err
	}

	opts := &git.CloneOptions{
		URL:           r.url,
		ReferenceName: refName,
		SingleBranch:  true,
		Depth:         1,
		Auth:          r.auth(),
	}

	if _, err := git.PlainCloneContext(ctx, r.cacheDir, false, opts); err != nil {
		// Clean up partial clone on failure
		_ = os.RemoveAll(r.cacheDir)
		return "", OpClone, errors.Wrap(err, "cannot clone git repository")
	}

	return r.cacheDir, OpClone, nil
}

// ensureRevision clones the full repository (no shallow/single-branch limits, so
// an arbitrary commit SHA or tag is resolvable) and checks out the pinned
// revision. The revision is treated as immutable: because the cache key embeds
// it, an existing cache is reused as-is without fetching.
func (r *Repo) ensureRevision(ctx context.Context) (string, error) {
	if _, err := os.Stat(filepath.Join(r.cacheDir, ".git")); err == nil {
		return r.cacheDir, nil
	}

	if err := ensureCacheRoot(filepath.Dir(r.cacheDir)); err != nil {
		return "", err
	}

	repo, err := git.PlainCloneContext(ctx, r.cacheDir, false, &git.CloneOptions{
		URL:  r.url,
		Auth: r.auth(),
		Tags: git.AllTags,
	})
	if err != nil {
		_ = os.RemoveAll(r.cacheDir)
		return "", errors.Wrap(err, "cannot clone git repository")
	}

	hash, err := repo.ResolveRevision(plumbing.Revision(r.revision))
	if err != nil {
		_ = os.RemoveAll(r.cacheDir)
		return "", errors.Wrapf(err, "cannot resolve git revision %q", r.revision)
	}

	wt, err := repo.Worktree()
	if err != nil {
		_ = os.RemoveAll(r.cacheDir)
		return "", errors.Wrap(err, "cannot get worktree")
	}

	if err := wt.Checkout(&git.CheckoutOptions{Hash: *hash}); err != nil {
		_ = os.RemoveAll(r.cacheDir)
		return "", errors.Wrapf(err, "cannot checkout git revision %q", r.revision)
	}

	return r.cacheDir, nil
}

func (r *Repo) pull(ctx context.Context, refName plumbing.ReferenceName) (string, error) {
	repo, err := git.PlainOpen(r.cacheDir)
	if err != nil {
		return "", errors.Wrap(err, "cannot open cached git repository")
	}

	wt, err := repo.Worktree()
	if err != nil {
		return "", errors.Wrap(err, "cannot get worktree")
	}

	err = wt.PullContext(ctx, &git.PullOptions{
		RemoteName:    "origin",
		ReferenceName: refName,
		SingleBranch:  true,
		Depth:         1,
		Auth:          r.auth(),
		Force:         true,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return "", errors.Wrap(err, "cannot pull git repository")
	}

	return r.cacheDir, nil
}

// ReadFile reads a file at the given relative path from the cloned repo.
func (r *Repo) ReadFile(relativePath string) ([]byte, error) {
	fullPath := filepath.Join(r.cacheDir, relativePath)
	data, err := os.ReadFile(fullPath) //nolint:gosec // path is constructed from controlled cacheDir + relative path
	if err != nil {
		return nil, errors.Wrapf(err, "cannot read file %q from git repository", relativePath)
	}
	return data, nil
}
