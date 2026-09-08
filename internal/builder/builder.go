package builder

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Patched build of @tailwindcss/oxide for linux-arm64-gnu. The stock 4.1.17
// release panics with a from_utf8 unwrap when scanning .vue files under
// certain glob configurations on this host. Source patch + regression test
// live in https://github.com/flakerimi/tailwindcss (vue.rs PreProcessor —
// graceful skip on invalid UTF-8 chunk instead of unwrap). Once tailwindlabs
// merges and releases a fix this overlay can go away.
//
//go:embed oxide-patched-linux-arm64-gnu.node
var patchedOxideLinuxArm64Gnu []byte

// dangerousPatterns are code patterns that indicate potentially malicious code.
var dangerousPatterns = []string{
	"child_process",
	"require('fs')",
	"require(\"fs\")",
	"process.env",
	"eval(",
	"Function(",
	"document.cookie",
	"importScripts",
	"__proto__",
	"constructor.constructor",
}

// BuildResult holds the output of a successful build.
type BuildResult struct {
	BundlePath string
	Checksum   string
	Size       int64
	Duration   string
	Log        string
}

// BuildFromSource extracts the source tarball, runs security checks,
// installs dependencies, and builds the space with vite.
// Returns the path to the built bundle tarball and build metadata.
func BuildFromSource(sourceTarball string) (*BuildResult, error) {
	start := time.Now()

	// Create temp work directory
	workDir, err := os.MkdirTemp("", "space-build-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create work dir: %w", err)
	}

	var logBuilder strings.Builder

	// Extract source tarball
	logBuilder.WriteString("==> Extracting source...\n")
	if err := extractTarGz(sourceTarball, workDir); err != nil {
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to extract source: %w", err)
	}

	// Security scan
	logBuilder.WriteString("==> Running security checks...\n")
	issues, err := scanSource(workDir)
	if err != nil {
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("security scan failed: %w", err)
	}
	if len(issues) > 0 {
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("security check failed:\n  - %s", strings.Join(issues, "\n  - "))
	}
	logBuilder.WriteString("    No issues found.\n")

	// Fix file: dependencies — replace local paths with npm packages
	logBuilder.WriteString("==> Resolving dependencies...\n")
	if err := fixFileDeps(workDir); err != nil {
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to fix dependencies: %w", err)
	}

	// Install dependencies. The CLI's packSource() never includes a lockfile
	// (bun.lock is on the blocked-extensions list - see packages/construct-cli
	// lib/pack.ts), so we always resolve fresh against the registry. This
	// also ensures spaces pick up the latest CLI patch satisfying their
	// semver range on every publish - intentional. --trust allows
	// postinstall scripts (the construct CLI binary install).
	logBuilder.WriteString("==> Installing dependencies...\n")
	installOut, err := runCommand(workDir, "bun", "install", "--trust")
	if err != nil {
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("bun install failed: %s\n%s", err, installOut)
	}
	logBuilder.WriteString(installOut)

	// Overlay our patched @tailwindcss/oxide native binding over the one
	// bun install just pulled. See the embed declaration at the top of
	// this file for context. Best-effort: failure is logged but doesn't
	// block the build (the stock binding may still work for some spaces).
	if len(patchedOxideLinuxArm64Gnu) > 0 {
		oxideDir := filepath.Join(workDir, "node_modules", "@tailwindcss", "oxide-linux-arm64-gnu")
		oxidePath := filepath.Join(oxideDir, "tailwindcss-oxide.linux-arm64-gnu.node")
		if _, statErr := os.Stat(oxideDir); statErr == nil {
			if writeErr := os.WriteFile(oxidePath, patchedOxideLinuxArm64Gnu, 0o644); writeErr == nil {
				logBuilder.WriteString("==> Overlaid patched @tailwindcss/oxide (linux-arm64-gnu)\n")
			} else {
				logBuilder.WriteString(fmt.Sprintf("    (oxide overlay skipped: %s)\n", writeErr))
			}
		}
	}

	// Build via the CLI (`construct build`) instead of calling vite
	// directly. The CLI is the contract owner for .space bundles: it
	// runs Vite, writes manifest build metadata, copies root SKILL.md /
	// agent / assets / tools / lib, and emits dist/<id>.space.
	logBuilder.WriteString("==> Building (via construct CLI)...\n")
	cliBin := filepath.Join(workDir, "node_modules", "@construct-space", "cli", "dist", "index.js")
	if _, err := os.Stat(cliBin); os.IsNotExist(err) {
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("build failed: @construct-space/cli is required so the build can emit dist/<id>.space")
	}
	buildOut, err := runCommandWithEnv(workDir, map[string]string{
		// Marketplace builds need every platform directory in the .space
		// bundle. Local developer builds default to the current platform.
		"CONSTRUCT_TOOL_TARGETS": "all",
	}, "bun", cliBin, "build")
	if err != nil {
		// TEMP DEBUG: keep workDir on failure so we can SSH in and inspect.
		// Remove this once the panic is diagnosed.
		fmt.Fprintf(os.Stderr, "[debug] build failed; preserving workDir at %s\n", workDir)
		return nil, fmt.Errorf("build failed: %s\n%s\n\nBuild log:\n%s\n(workDir preserved at %s)", err, buildOut, logBuilder.String(), workDir)
	}
	logBuilder.WriteString(buildOut)

	// Find dist directory
	distDir := filepath.Join(workDir, "dist")
	if _, err := os.Stat(distDir); os.IsNotExist(err) {
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("build produced no dist/ directory")
	}

	// Server publishes exactly the same app-bundle shape the CLI emits.
	logBuilder.WriteString("==> Validating .space bundle...\n")
	spaceDir, err := requireSpaceBundle(distDir)
	if err != nil {
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("invalid .space bundle: %w", err)
	}

	// Sign app.iife.js (no-op when SPACE_SIGNING_KEY_PEM is unset). Must run on
	// the loose bundle dir, before packaging, so the signed manifest.json ends
	// up in the tarball.
	logBuilder.WriteString("==> Signing bundle...\n")
	if err := signSpaceBundle(spaceDir); err != nil {
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to sign bundle: %w", err)
	}

	// Package the .space directory into a tarball
	logBuilder.WriteString("==> Packaging bundle...\n")
	bundlePath, err := packageSpaceBundle(spaceDir)
	if err != nil {
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("failed to package .space bundle: %w", err)
	}

	// Compute checksum
	checksum, err := sha256File(bundlePath)
	if err != nil {
		os.RemoveAll(workDir)
		os.Remove(bundlePath)
		return nil, fmt.Errorf("failed to compute checksum: %w", err)
	}

	info, _ := os.Stat(bundlePath)
	duration := time.Since(start).Round(time.Millisecond).String()
	fmt.Fprintf(&logBuilder, "==> Build complete in %s\n", duration)

	// Clean up work dir (keep bundle)
	os.RemoveAll(workDir)

	return &BuildResult{
		BundlePath: bundlePath,
		Checksum:   checksum,
		Size:       info.Size(),
		Duration:   duration,
		Log:        logBuilder.String(),
	}, nil
}

func extractTarGz(tarballPath, destDir string) error {
	f, err := os.Open(tarballPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Prevent path traversal
		target := filepath.Join(destDir, header.Name)
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("invalid tar entry: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, io.LimitReader(tr, 50*1024*1024)); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
	return nil
}

func scanSource(dir string) ([]string, error) {
	var issues []string

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == "node_modules" || info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}

		// Only scan code files
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".ts" && ext != ".js" && ext != ".vue" && ext != ".tsx" && ext != ".jsx" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := string(data)
		relPath, _ := filepath.Rel(dir, path)

		for _, pattern := range dangerousPatterns {
			if strings.Contains(content, pattern) {
				issues = append(issues, fmt.Sprintf("%s: contains '%s'", relPath, pattern))
			}
		}

		return nil
	})

	return issues, err
}

// fileDepReplacements maps file: dependency names to their npm package equivalents.
var fileDepReplacements = map[string]string{
	"@construct-space/sdk": "*",
	"construct":            "npm:@construct-space/cli@*",
}

// fixFileDeps reads package.json and replaces any "file:..." dependencies
// with their npm equivalents so the server can install them.
func fixFileDeps(workDir string) error {
	pkgPath := filepath.Join(workDir, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return nil // no package.json, nothing to fix
	}

	content := string(data)
	modified := false

	for name, npmVersion := range fileDepReplacements {
		// Match "name": "file:..." patterns
		prefix := fmt.Sprintf(`"%s": "file:`, name)
		idx := strings.Index(content, prefix)
		if idx < 0 {
			continue
		}
		// Find the closing quote
		start := idx + len(fmt.Sprintf(`"%s": "`, name))
		end := strings.Index(content[start:], `"`)
		if end < 0 {
			continue
		}
		old := content[start : start+end]
		content = strings.Replace(content, fmt.Sprintf(`"%s": "%s"`, name, old), fmt.Sprintf(`"%s": "%s"`, name, npmVersion), 1)
		modified = true
	}

	if modified {
		return os.WriteFile(pkgPath, []byte(content), 0644)
	}
	return nil
}

func runCommand(dir string, name string, args ...string) (string, error) {
	return runCommandWithEnv(dir, nil, name, args...)
}

func runCommandWithEnv(dir string, extraEnv map[string]string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	// Force a UTF-8 locale. Without this, build sub-processes inherit
	// whatever locale the api was started with — on a minimal container
	// that's typically POSIX/C, which makes Rust-native tooling (Tailwind
	// oxide in particular) panic with `from_utf8 unwrap` when scanning
	// content even though the files themselves are valid UTF-8.
	env := append(os.Environ(),
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	)
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func requireSpaceBundle(distDir string) (string, error) {
	entries, err := os.ReadDir(distDir)
	if err != nil {
		return "", err
	}
	var matches []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".space") {
			continue
		}
		spacePath := filepath.Join(distDir, entry.Name())

		// CLI >= 1.9.7 emits dist/<id>.space as a ZIP file (the new
		// self-contained bundle format). Older CLIs emit a directory.
		// Accept both: when we see a file, extract it in place so the
		// rest of the pipeline (validation, tar packaging) stays the
		// same.
		if !entry.IsDir() {
			if err := extractSpaceZipInPlace(spacePath); err != nil {
				return "", fmt.Errorf("extract %s: %w", entry.Name(), err)
			}
		}

		if fileExists(filepath.Join(spacePath, "manifest.json")) &&
			fileExists(filepath.Join(spacePath, "app.iife.js")) &&
			fileExists(filepath.Join(spacePath, "style.css")) &&
			fileExists(filepath.Join(spacePath, "checksums.json")) {
			matches = append(matches, spacePath)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("expected one .space bundle, found %d", len(matches))
	}
	return "", fmt.Errorf("expected dist/<id>.space with manifest.json, app.iife.js, style.css, and checksums.json")
}

// extractSpaceZipInPlace turns `dist/<id>.space` (a ZIP file) into
// `dist/<id>.space` (a directory) holding the unpacked contents. The
// original zip is removed afterwards. Uses an `.unpacking` sidecar so a
// crash leaves either the zip or the dir behind, never a half-state.
func extractSpaceZipInPlace(zipPath string) error {
	tmpDir := zipPath + ".unpacking"
	_ = os.RemoveAll(tmpDir)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}

	r, err := zip.OpenReader(zipPath)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		// Refuse zip-slip: a zip entry named `../foo` would escape tmpDir.
		dest := filepath.Join(tmpDir, f.Name)
		if !strings.HasPrefix(dest, filepath.Clean(tmpDir)+string(os.PathSeparator)) && dest != filepath.Clean(tmpDir) {
			_ = os.RemoveAll(tmpDir)
			return fmt.Errorf("zip entry escapes bundle: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, f.Mode()); err != nil {
				_ = os.RemoveAll(tmpDir)
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			_ = os.RemoveAll(tmpDir)
			return err
		}
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			_ = os.RemoveAll(tmpDir)
			return err
		}
		in, err := f.Open()
		if err != nil {
			out.Close()
			_ = os.RemoveAll(tmpDir)
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			in.Close()
			out.Close()
			_ = os.RemoveAll(tmpDir)
			return err
		}
		in.Close()
		out.Close()
	}

	// Atomic-ish swap: remove the zip, then rename the unpacked dir
	// into its place.
	if err := os.Remove(zipPath); err != nil {
		_ = os.RemoveAll(tmpDir)
		return err
	}
	if err := os.Rename(tmpDir, zipPath); err != nil {
		return err
	}
	return nil
}

func packageSpaceBundle(spaceDir string) (string, error) {
	tmpFile, err := os.CreateTemp("", "space-bundle-*.tar.gz")
	if err != nil {
		return "", err
	}
	bundlePath := tmpFile.Name()

	gz := gzip.NewWriter(tmpFile)
	tw := tar.NewWriter(gz)
	baseDir := filepath.Dir(spaceDir)

	err = filepath.Walk(spaceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, _ := filepath.Rel(baseDir, path)
		if relPath == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relPath

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})

	tw.Close()
	gz.Close()
	tmpFile.Close()

	if err != nil {
		os.Remove(bundlePath)
		return "", err
	}
	return bundlePath, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
