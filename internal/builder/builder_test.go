package builder

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRequireSpaceBundleAcceptsCanonicalLayout(t *testing.T) {
	workDir := t.TempDir()
	distDir := filepath.Join(workDir, "dist")
	spaceDir := filepath.Join(distDir, "video.space")
	if err := os.MkdirAll(filepath.Join(spaceDir, "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(spaceDir, "tools", "darwin-arm64"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(spaceDir, "lib", "darwin-arm64"), 0o755); err != nil {
		t.Fatal(err)
	}

	mustWrite(t, filepath.Join(spaceDir, "manifest.json"), []byte(`{"id":"video","name":"Video","version":"0.1.0"}`+"\n"))
	mustWrite(t, filepath.Join(spaceDir, "app.iife.js"), []byte("window.__CONSTRUCT_SPACE_VIDEO={pages:{}}\n"))
	mustWrite(t, filepath.Join(spaceDir, "style.css"), []byte(".text-xs{font-size:.75rem}\n"))
	mustWrite(t, filepath.Join(spaceDir, "checksums.json"), []byte(`{"format":"lisaos.dev/v1"}`+"\n"))
	mustWrite(t, filepath.Join(spaceDir, "agent", "config.md"), []byte("# Agent\n"))
	mustWrite(t, filepath.Join(spaceDir, "tools", "darwin-arm64", "video-tools"), []byte("#!/bin/sh\n"))
	mustWrite(t, filepath.Join(spaceDir, "lib", "darwin-arm64", "ffmpeg"), []byte("#!/bin/sh\n"))

	got, err := requireSpaceBundle(distDir)
	if err != nil {
		t.Fatalf("requireSpaceBundle() error = %v", err)
	}
	if got != spaceDir {
		t.Fatalf("requireSpaceBundle() = %s, want %s", got, spaceDir)
	}
}

func TestRequireSpaceBundleRejectsFlatArtifacts(t *testing.T) {
	distDir := t.TempDir()
	mustWrite(t, filepath.Join(distDir, "space-video.iife.js"), []byte("window.__CONSTRUCT_SPACE_VIDEO={pages:{}}\n"))
	mustWrite(t, filepath.Join(distDir, "space-video.css"), []byte(".text-xs{font-size:.75rem}\n"))

	if _, err := requireSpaceBundle(distDir); err == nil {
		t.Fatalf("requireSpaceBundle() error = nil, want error")
	}
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
