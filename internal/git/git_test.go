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
	"os"
	"path/filepath"
	"testing"

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
