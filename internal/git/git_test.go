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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/go-cmp/cmp"
)

func TestNewRepo(t *testing.T) {
	cases := map[string]struct {
		url      string
		branch   string
		revision string
		token    string
		wantBr   string
	}{
		"DefaultBranch": {
			url:    "https://github.com/example/repo.git",
			branch: "",
			wantBr: "main",
		},
		"CustomBranch": {
			url:    "https://github.com/example/repo.git",
			branch: "develop",
			wantBr: "develop",
		},
		"PinnedRevision": {
			url:      "https://github.com/example/repo.git",
			branch:   "main",
			revision: "v1.2.3",
			wantBr:   "main",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := NewRepo(tc.url, tc.branch, tc.revision, tc.token)
			if diff := cmp.Diff(tc.wantBr, r.branch); diff != "" {
				t.Errorf("branch: -want, +got:\n%s", diff)
			}
			if r.url != tc.url {
				t.Errorf("url: want %q, got %q", tc.url, r.url)
			}
			if r.cacheDir == "" {
				t.Error("cacheDir should not be empty")
			}
		})
	}
}

func TestNewRepoDeterministicCacheDir(t *testing.T) {
	r1 := NewRepo("https://github.com/example/repo.git", "main", "", "")
	r2 := NewRepo("https://github.com/example/repo.git", "main", "", "")
	r3 := NewRepo("https://github.com/example/other.git", "main", "", "")

	if r1.cacheDir != r2.cacheDir {
		t.Errorf("same URL should produce same cacheDir: %q vs %q", r1.cacheDir, r2.cacheDir)
	}
	if r1.cacheDir == r3.cacheDir {
		t.Errorf("different URLs should produce different cacheDirs: %q vs %q", r1.cacheDir, r3.cacheDir)
	}

	r4 := NewRepo("https://github.com/example/repo.git", "develop", "", "")
	if r1.cacheDir == r4.cacheDir {
		t.Errorf("different branches should produce different cacheDirs: %q vs %q", r1.cacheDir, r4.cacheDir)
	}

	// A pinned revision must not collide with the branch-tip cache, and
	// different revisions must map to different cache dirs.
	r5 := NewRepo("https://github.com/example/repo.git", "main", "v1.0.0", "")
	r6 := NewRepo("https://github.com/example/repo.git", "main", "v2.0.0", "")
	if r1.cacheDir == r5.cacheDir {
		t.Errorf("pinned revision should not collide with branch-tip cacheDir: %q", r5.cacheDir)
	}
	if r5.cacheDir == r6.cacheDir {
		t.Errorf("different revisions should produce different cacheDirs: %q vs %q", r5.cacheDir, r6.cacheDir)
	}
}

func TestAuth(t *testing.T) {
	r := NewRepo("https://github.com/example/repo.git", "", "", "my-token")
	auth := r.auth()
	if auth == nil {
		t.Fatal("expected auth to be non-nil when token is set")
	}
	if auth.Username != "x-access-token" {
		t.Errorf("username: want %q, got %q", "x-access-token", auth.Username)
	}
	if auth.Password != "my-token" {
		t.Errorf("password: want %q, got %q", "my-token", auth.Password)
	}

	r2 := NewRepo("https://github.com/example/repo.git", "", "", "")
	if r2.auth() != nil {
		t.Error("expected nil auth when token is empty")
	}
}

func TestEvictCacheLRU(t *testing.T) {
	root := t.TempDir()
	base := time.Unix(1_000_000, 0)
	paths := make([]string, 5)
	for i := range paths {
		p := filepath.Join(root, fmt.Sprintf("r%d", i))
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		mt := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		paths[i] = p
	}

	// Cap to 2 entries, keeping the newest. The 3 oldest must be evicted.
	evictCache(root, paths[4], 2)

	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	for _, i := range []int{0, 1, 2} {
		if exists(paths[i]) {
			t.Errorf("expected LRU entry %s to be evicted", paths[i])
		}
	}
	for _, i := range []int{3, 4} {
		if !exists(paths[i]) {
			t.Errorf("expected recent entry %s to be retained", paths[i])
		}
	}
}

func TestEvictCacheSkipsLocked(t *testing.T) {
	root := t.TempDir()
	base := time.Unix(1_000_000, 0)
	paths := make([]string, 3)
	for i := range paths {
		p := filepath.Join(root, fmt.Sprintf("r%d", i))
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		mt := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		paths[i] = p
	}

	// Lock the oldest entry: eviction must skip it even though it is the
	// least-recently-used, because another reconcile is using it.
	mu := repoMutex(paths[0])
	mu.Lock()
	defer mu.Unlock()

	evictCache(root, "", 1)

	if _, err := os.Stat(paths[0]); err != nil {
		t.Error("locked dir must not be evicted")
	}
	if _, err := os.Stat(paths[1]); err == nil {
		t.Error("unlocked LRU dir should have been evicted")
	}
}

// initSourceRepo creates a local non-bare git repo with one commit on `main`
// and returns its path and the commit hash.
func initSourceRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	if err != nil {
		t.Fatalf("init source repo: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kubeconfig.yaml"), []byte("data"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := wt.Add("kubeconfig.yaml"); err != nil {
		t.Fatalf("add: %v", err)
	}
	h, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Unix(0, 0)},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return dir, h.String()
}

func TestEnsureClonedPrivateCacheAndNoCredentialLeak(t *testing.T) {
	src, hash := initSourceRepo(t)

	// Point the cache at a not-yet-existing dir so ensureCacheRoot must create
	// it, and we can assert the permissions it applies.
	root := filepath.Join(t.TempDir(), "cache")
	t.Setenv(envCacheDir, root)

	const token = "SUPER-SECRET-TOKEN"
	// Pin to the commit hash so the (local-friendly) full-clone path runs.
	r := NewRepo(src, "main", hash, token)
	dir, op, err := r.EnsureCloned(context.Background())
	if err != nil {
		t.Fatalf("EnsureCloned: %v", err)
	}
	if op != OpRevision {
		t.Errorf("operation: want %q, got %q", OpRevision, op)
	}

	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat cache root: %v", err)
	}
	if perm := info.Mode().Perm(); perm != cacheDirPerm {
		t.Errorf("cache root permissions: want %#o, got %#o", cacheDirPerm, perm)
	}

	cfg, err := os.ReadFile(filepath.Join(dir, ".git", "config")) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("read .git/config: %v", err)
	}
	if strings.Contains(string(cfg), token) {
		t.Error(".git/config leaked the access token")
	}
}

func TestReadFile(t *testing.T) {
	// Create a temp directory to act as a fake cloned repo.
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "clusters")
	if err := os.MkdirAll(subDir, 0750); err != nil {
		t.Fatalf("cannot create temp subdir: %v", err)
	}

	content := []byte("apiVersion: v1\nkind: Config\n")
	if err := os.WriteFile(filepath.Join(subDir, "dev.yaml"), content, 0600); err != nil {
		t.Fatalf("cannot write temp file: %v", err)
	}

	r := &Repo{cacheDir: tmpDir}

	t.Run("ExistingFile", func(t *testing.T) {
		got, err := r.ReadFile("clusters/dev.yaml")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if diff := cmp.Diff(content, got); diff != "" {
			t.Errorf("-want, +got:\n%s", diff)
		}
	})

	t.Run("MissingFile", func(t *testing.T) {
		_, err := r.ReadFile("clusters/nonexistent.yaml")
		if err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
	})
}
