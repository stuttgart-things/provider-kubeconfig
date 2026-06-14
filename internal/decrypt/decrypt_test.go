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

package decrypt

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestFormatFromPath(t *testing.T) {
	cases := map[string]struct {
		path string
		want string
	}{
		"YAML": {
			path: "clusters/dev.yaml",
			want: "yaml",
		},
		"YML": {
			path: "clusters/dev.yml",
			want: "yaml",
		},
		"JSON": {
			path: "clusters/dev.json",
			want: "json",
		},
		"DotEnv": {
			path: "secrets/.env",
			want: "dotenv",
		},
		"INI": {
			path: "config/app.ini",
			want: "dotenv",
		},
		"Binary": {
			path: "clusters/dev.bin",
			want: "binary",
		},
		"NoExtension": {
			path: "clusters/dev",
			want: "binary",
		},
		"UppercaseYAML": {
			path: "clusters/dev.YAML",
			want: "yaml",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := FormatFromPath(tc.path)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("FormatFromPath(%q): -want, +got:\n%s", tc.path, diff)
			}
		})
	}
}

func TestSOPSDecryptEmptyKey(t *testing.T) {
	_, err := SOPSDecrypt([]byte("data"), "file.yaml", "")
	if err == nil {
		t.Fatal("expected error for empty age key, got nil")
	}
	if err.Error() != "age key is empty" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSOPSDecryptInvalidData(t *testing.T) {
	// Non-empty key but invalid SOPS data should return a decrypt error.
	_, err := SOPSDecrypt([]byte("not-encrypted"), "file.yaml", "AGE-SECRET-KEY-FAKE")
	if err == nil {
		t.Fatal("expected error for non-SOPS data, got nil")
	}
}

// TestSOPSDecryptConcurrentKeyIsolation is a regression test for issue #58:
// concurrent decrypts with different age keys must not clobber the shared
// SOPS_AGE_KEY env var. After all goroutines complete, the env var must be
// restored to its original value. Without serialization, interleaved
// save/restore can leak one goroutine's key into the process environment.
// Run with -race to also catch any data race on the env var.
func TestSOPSDecryptConcurrentKeyIsolation(t *testing.T) {
	const original = "SENTINEL-ORIGINAL-KEY"
	t.Setenv("SOPS_AGE_KEY", original)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			// Distinct key per goroutine; invalid data means decrypt errors,
			// which is fine — we only care that the env var is not corrupted.
			_, _ = SOPSDecrypt([]byte("not-encrypted"), "file.yaml", fmt.Sprintf("AGE-SECRET-KEY-%d", i))
		}(i)
	}
	wg.Wait()

	if got := os.Getenv("SOPS_AGE_KEY"); got != original {
		t.Errorf("SOPS_AGE_KEY leaked after concurrent decrypts: want %q, got %q", original, got)
	}
}
