package models

import "testing"

func TestBundleDownloadURLPrefersBundleURL(t *testing.T) {
	bundleURL := "https://cdn.construct.space/spaces/pinball/1.0/bundle.tar.gz"
	bundlePath := "data/bundles/pinball/1.0/bundle.tar.gz"
	space := Space{Name: "pinball", Version: "1.0", BundleURL: &bundleURL, BundlePath: &bundlePath}

	got := space.BundleDownloadURL("https://developer.lisaos.dev")
	if got != bundleURL {
		t.Fatalf("BundleDownloadURL() = %q, want %q", got, bundleURL)
	}
}

func TestBundleDownloadURLTreatsLegacyBundlePathURLAsExternal(t *testing.T) {
	bundlePath := "https://cdn.construct.space/spaces/pinball/1.0/bundle.tar.gz"
	space := Space{Name: "pinball", Version: "1.0", BundlePath: &bundlePath}

	got := space.BundleDownloadURL("https://developer.lisaos.dev")
	if got != bundlePath {
		t.Fatalf("BundleDownloadURL() = %q, want %q", got, bundlePath)
	}
}

func TestBundleDownloadURLBuildsLocalDownloadFromAppURL(t *testing.T) {
	bundlePath := "data/bundles/pinball/1.0/bundle.tar.gz"
	space := Space{Name: "pinball", Version: "1.0", BundlePath: &bundlePath}

	want := "https://my.lisaos.dev/api/developer/downloads/pinball/1.0/bundle.tar.gz"
	got := space.BundleDownloadURL("https://my.lisaos.dev/api/developer")
	if got != want {
		t.Fatalf("BundleDownloadURL() = %q, want %q", got, want)
	}
}

func TestToRegistryJSONUsesLegacyBundlePathURLAsTarball(t *testing.T) {
	bundlePath := "https://cdn.construct.space/spaces/pinball/1.0/bundle.tar.gz"
	checksum := "sha256:abc"
	space := Space{
		Name:           "pinball",
		DisplayName:    "Pinball",
		Version:        "1.0",
		BundlePath:     &bundlePath,
		BuildChecksum:  &checksum,
		NavigationJSON: "{}",
		PagesJSON:      "[]",
	}

	got := space.ToRegistryJSON()
	if got["tarball"] != bundlePath {
		t.Fatalf("tarball = %#v, want %q", got["tarball"], bundlePath)
	}

	downloads, ok := got["downloads"].(map[string]any)
	if !ok {
		t.Fatalf("downloads = %#v, want map", got["downloads"])
	}
	if downloads["bundle"] != bundlePath {
		t.Fatalf("downloads.bundle = %#v, want %q", downloads["bundle"], bundlePath)
	}
	if downloads["checksum"] != &checksum {
		t.Fatalf("downloads.checksum = %#v, want checksum pointer", downloads["checksum"])
	}
}
