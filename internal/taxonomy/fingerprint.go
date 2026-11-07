package taxonomy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// fingerprintSource is one embedded file's path and raw bytes, the
// unit PackFingerprint hashes over. Path is part of the digest so
// renaming a data file (or two files colliding when concatenated)
// changes the fingerprint.
type fingerprintSource struct {
	Path string
	Data []byte
}

// PackFingerprint returns a stable sha256 (hex) over every embedded
// pack JSON byte: the five data/*.json files in constant order, then
// data/mappings/*.json in lexicographic filename order. The pack ships
// in the binary, so the fingerprint only changes with a rebuild against
// new data — the idempotent import uses it to skip re-parsing the 13MB
// pack on restart. It reads the raw embed.FS bytes only: never calls
// Load(), never parses JSON.
func PackFingerprint() (string, error) {
	files := make([]fingerprintSource, 0, 16)
	for _, name := range []string{topicsFile, depsFile, questionsFile, standardsDataFile, aliasesDataFile} {
		b, err := dataFS.ReadFile(name)
		if err != nil {
			return "", fmt.Errorf("read embedded pack file %s: %w", name, err)
		}
		files = append(files, fingerprintSource{Path: name, Data: b})
	}
	entries, err := dataFS.ReadDir(mappingsDir)
	if err != nil {
		return "", fmt.Errorf("read embedded pack dir %s: %w", mappingsDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !isJSONName(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names) // ReadDir order is not a contract; sort explicitly
	for _, name := range names {
		path := mappingsDir + "/" + name
		b, err := dataFS.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read embedded pack file %s: %w", path, err)
		}
		files = append(files, fingerprintSource{Path: path, Data: b})
	}
	return fingerprintFromSources(files), nil
}

// fingerprintFromSources folds "path bytes + content bytes" of every
// source, in order, into one sha256. Both parts go into the same hash
// (no length-prefix tricks needed: the path is a fixed separator a file
// content cannot forge mid-stream without also matching a path).
func fingerprintFromSources(files []fingerprintSource) string {
	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(f.Path))
		h.Write(f.Data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// isJSONName reports whether name has a .json suffix (case-sensitive:
// the pack's files are all lowercase).
func isJSONName(name string) bool {
	return len(name) > len(".json") && name[len(name)-len(".json"):] == ".json"
}
