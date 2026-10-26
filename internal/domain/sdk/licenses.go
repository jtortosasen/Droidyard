package sdk

import (
	"crypto/sha1"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxLicenseFile bounds a read of <root>/licenses/<id>: a licence file holds a few
// 40-character lines.
const maxLicenseFile = 64 << 10

// licenceHash is the hash sdklib writes into <root>/licenses/<id> and compares against,
// the line AGP, Android Studio and the classic tools read: sha1 of the UTF-8 text after
// com.android.repository.impl.meta.TrimStringAdapter's four steps, in order (the spike's
// §(a); ADR-0130 §3):
//
//  1. replaceAll("(?<=\\s)[ \t]*", ""): a space or tab that follows whitespace goes;
//  2. replaceAll("(?<!\n)\n(?!\n)", " "): a single newline becomes a space;
//  3. replaceAll(" +", " "): a run of spaces becomes one;
//  4. trim(): every character up to U+0020 goes from both ends.
//
// The raw text's sha1 and a plainly trimmed text's match nothing sdklib writes. One
// function for the listing, the consent and the installer.
func licenceHash(text string) string {
	sum := sha1.Sum([]byte(trimStringAdapter(text)))
	return hex.EncodeToString(sum[:])
}

// trimStringAdapter is TrimStringAdapter.unmarshal on s.
func trimStringAdapter(s string) string {
	// Step 1. Java's \s is [ \t\n\x0B\f\r]; the lookbehind reads the original text.
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if (s[i] == ' ' || s[i] == '\t') && i > 0 && isJavaSpace(s[i-1]) {
			continue
		}
		b = append(b, s[i])
	}
	// Step 2. Both lookarounds read this step's input, so the replacements are
	// independent of each other.
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' && (i == 0 || b[i-1] != '\n') && (i == len(b)-1 || b[i+1] != '\n') {
			out = append(out, ' ')
			continue
		}
		out = append(out, b[i])
	}
	// Step 3.
	collapsed := make([]byte, 0, len(out))
	for i := 0; i < len(out); i++ {
		if out[i] == ' ' && i > 0 && out[i-1] == ' ' {
			continue
		}
		collapsed = append(collapsed, out[i])
	}
	// Step 4: String.trim removes every char <= U+0020.
	return strings.TrimFunc(string(collapsed), func(r rune) bool { return r <= ' ' })
}

func isJavaSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
}

// licenseAccepted reports whether <root>/licenses/<id> holds a line equal to hash: the
// way sdklib decides a licence is accepted. The file must be a regular file (Lstat: a
// symlink is not read) of at most maxLicenseFile bytes. An older text's hash, an absent
// file and an unreadable one are all "not accepted"; the licence id is validated by the
// repository model before it reaches a path.
func licenseAccepted(root, id, hash string) bool {
	if root == "" || id == "" || hash == "" || !licenseIDPattern.MatchString(id) {
		return false
	}
	path := filepath.Join(root, "licenses", id)
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxLicenseFile {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxLicenseFile+1))
	if err != nil || len(data) > maxLicenseFile {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == hash {
			return true
		}
	}
	return false
}
