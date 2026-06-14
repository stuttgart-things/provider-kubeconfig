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
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/getsops/sops/v3/decrypt"
	"github.com/pkg/errors"
)

// sopsEnvMu serializes access to the process-global SOPS_AGE_KEY environment
// variable. SOPS reads the age key from this env var, so without serialization
// concurrent reconciles of RemoteClusters with different age keys can race:
// one goroutine could overwrite the key while another is mid-decrypt, causing
// sporadic decrypt failures or a key mismatch. Decryption is CPU-cheap and not
// the reconcile hot path (git fetch is), so a global lock is an acceptable
// mitigation. See issue #58.
var sopsEnvMu sync.Mutex

// SOPSDecrypt decrypts SOPS-encrypted data using the provided age key.
// It detects the format from the file path extension.
func SOPSDecrypt(data []byte, filePath string, ageKey string) ([]byte, error) {
	if ageKey == "" {
		return nil, errors.New("age key is empty")
	}

	// Serialize the env-var set/decrypt/restore so concurrent callers with
	// different keys cannot clobber each other's SOPS_AGE_KEY.
	sopsEnvMu.Lock()
	defer sopsEnvMu.Unlock()

	// Set the age key for SOPS to pick up
	prev := os.Getenv("SOPS_AGE_KEY")
	if err := os.Setenv("SOPS_AGE_KEY", ageKey); err != nil {
		return nil, errors.Wrap(err, "cannot set SOPS_AGE_KEY")
	}
	defer func() { _ = os.Setenv("SOPS_AGE_KEY", prev) }()

	format := FormatFromPath(filePath)
	cleartext, err := decrypt.Data(data, format)
	if err != nil {
		return nil, errors.Wrap(err, "cannot decrypt SOPS data")
	}

	return cleartext, nil
}

// FormatFromPath returns the SOPS format string based on file extension.
func FormatFromPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		return "json"
	case ".env", ".ini":
		return "dotenv"
	case ".yml", ".yaml":
		return "yaml"
	default:
		return "binary"
	}
}
