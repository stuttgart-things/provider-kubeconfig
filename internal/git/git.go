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
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/pkg/errors"
)

var (
	// muMap guards concurrent access to the same repo cache directory.
	muMap   = make(map[string]*sync.Mutex)
	muMapMu sync.Mutex
)

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
	cacheDir := filepath.Join(os.TempDir(), "provider-kubeconfig", hash)
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
// commit/tag instead of tracking the branch tip.
func (r *Repo) EnsureCloned(ctx context.Context) (string, error) {
	mu := repoMutex(r.cacheDir)
	mu.Lock()
	defer mu.Unlock()

	if r.revision != "" {
		return r.ensureRevision(ctx)
	}

	refName := plumbing.NewBranchReferenceName(r.branch)

	if _, err := os.Stat(filepath.Join(r.cacheDir, ".git")); err == nil {
		dir, pullErr := r.pull(ctx, refName)
		if pullErr == nil {
			return dir, nil
		}
		// Pull failed (e.g. stale shallow clone) — remove cache and re-clone
		_ = os.RemoveAll(r.cacheDir)
	}

	if err := os.MkdirAll(filepath.Dir(r.cacheDir), 0750); err != nil {
		return "", errors.Wrap(err, "cannot create cache parent directory")
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
		return "", errors.Wrap(err, "cannot clone git repository")
	}

	return r.cacheDir, nil
}

// ensureRevision clones the full repository (no shallow/single-branch limits, so
// an arbitrary commit SHA or tag is resolvable) and checks out the pinned
// revision. The revision is treated as immutable: because the cache key embeds
// it, an existing cache is reused as-is without fetching.
func (r *Repo) ensureRevision(ctx context.Context) (string, error) {
	if _, err := os.Stat(filepath.Join(r.cacheDir, ".git")); err == nil {
		return r.cacheDir, nil
	}

	if err := os.MkdirAll(filepath.Dir(r.cacheDir), 0750); err != nil {
		return "", errors.Wrap(err, "cannot create cache parent directory")
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
