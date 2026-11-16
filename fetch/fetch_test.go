package fetch

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isHex reports whether s is a nonempty lowercase hex string.
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func TestPlatformTagForMatrix(t *testing.T) {
	cases := []struct {
		goos, goarch, want string
	}{
		{"darwin", "arm64", "macosx_11_0_arm64"},
		{"darwin", "amd64", "macosx_11_0_x86_64"},
		{"windows", "amd64", "win_amd64"},
		{"windows", "arm64", "win_arm64"},
	}
	for _, tc := range cases {
		got, err := PlatformTagFor(tc.goos, tc.goarch)
		if err != nil {
			t.Errorf("%s/%s: %v", tc.goos, tc.goarch, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s/%s = %q, want %q", tc.goos, tc.goarch, got, tc.want)
		}
	}
	// linux glibc tags (musl detection depends on the host, so only check
	// the glibc suffixes when the host is glibc linux)
	if runtime.GOOS == "linux" && !isMusl() {
		got, err := PlatformTagFor("linux", "amd64")
		if err != nil || got != "manylinux2014_x86_64" {
			t.Errorf("linux/amd64 = %q, %v", got, err)
		}
		got, err = PlatformTagFor("linux", "arm64")
		if err != nil || got != "manylinux2014_aarch64" {
			t.Errorf("linux/arm64 = %q, %v", got, err)
		}
	}
	// unsupported machines without wheels
	if _, err := PlatformTagFor("linux", "riscv64"); err == nil {
		t.Errorf("linux/riscv64 should have no wheel")
	}
	if _, err := PlatformTagFor("plan9", "amd64"); err == nil {
		t.Errorf("plan9/amd64 should have no wheel")
	}
}

func TestLibName(t *testing.T) {
	name := LibName()
	switch runtime.GOOS {
	case "windows":
		if name != "libcactus.dll" {
			t.Errorf("LibName = %q", name)
		}
	case "darwin":
		if name != "libcactus.dylib" {
			t.Errorf("LibName = %q", name)
		}
	default:
		if name != "libcactus.so" {
			t.Errorf("LibName = %q", name)
		}
	}
}

func TestLibNameForTag(t *testing.T) {
	if LibNameForTag("win_amd64") != "libcactus.dll" {
		t.Error("win tag should map to dll")
	}
	if LibNameForTag("macosx_11_0_arm64") != "libcactus.dylib" {
		t.Error("macos tag should map to dylib")
	}
	if LibNameForTag("manylinux2014_x86_64") != "libcactus.so" {
		t.Error("linux tag should map to so")
	}
}

func TestEngineReleasePins(t *testing.T) {
	if EngineRepo != "Cactus-Compute/cactus" {
		t.Errorf("EngineRepo = %q", EngineRepo)
	}
	if !isHex(EngineRevision) || len(EngineRevision) != 40 {
		t.Errorf("EngineRevision = %q, want a 40-hex-digit commit", EngineRevision)
	}
	if !isHex(ManifestSHA256) || len(ManifestSHA256) != 64 {
		t.Errorf("ManifestSHA256 = %q, want a 64-hex-digit digest", ManifestSHA256)
	}
	// Every model key is pinned end to end.
	want := []string{Needle3, Whistle, Mirage}
	for _, key := range want {
		if ModelRepos[key] == "" || ModelRevisions[key] == "" ||
			!isHex(ModelRevisions[key]) || len(ModelRevisions[key]) != 40 {
			t.Errorf("model %q has no pinned repository revision", key)
		}
		if !isHex(ModelSHA256[key]) || len(ModelSHA256[key]) != 64 {
			t.Errorf("model %q has no pinned checksum", key)
		}
		if BaseWeights[key] == "" || !strings.HasSuffix(BaseWeights[key], ".cact") {
			t.Errorf("model %q has no base weights archive", key)
		}
		if WeightsEnv[key] == "" {
			t.Errorf("model %q has no weights override variable", key)
		}
	}
	if len(ModelRepos) != len(want) || len(ModelSHA256) != len(want) {
		t.Errorf("model pin tables hold unexpected keys: %v / %v", ModelRepos, ModelSHA256)
	}
}

func TestCheckModel(t *testing.T) {
	for _, key := range []string{Needle3, Whistle, Mirage} {
		if err := CheckModel(key); err != nil {
			t.Errorf("CheckModel(%q) = %v", key, err)
		}
		if !ValidModel(key) {
			t.Errorf("ValidModel(%q) should be true", key)
		}
	}
	err := CheckModel("needle2")
	if err == nil {
		t.Fatal("needle2 should not be a fetchable model")
	}
	if !strings.Contains(err.Error(), "needle3") {
		t.Errorf("error should point at the supported models: %v", err)
	}
}

func TestValidPlatform(t *testing.T) {
	for _, p := range []string{"macos-arm64", "linux-x86_64", "windows-x86_64", "wasm", "wasm-component"} {
		if !validPlatform(p) {
			t.Errorf("%q should be valid", p)
		}
	}
	if validPlatform("linux-x86") {
		t.Error("linux-x86 should not be valid")
	}
}

func TestResolveURL(t *testing.T) {
	got := resolveURL("Cactus-Compute/cactus", EngineRevision, "releases/4.1.1/manifest.json")
	want := "https://huggingface.co/Cactus-Compute/cactus/resolve/" + EngineRevision + "/releases/4.1.1/manifest.json"
	if got != want {
		t.Errorf("resolveURL = %q, want %q", got, want)
	}
	t.Setenv("HF_ENDPOINT", "https://hf-mirror.com/")
	if got := Endpoint(); got != "https://hf-mirror.com" {
		t.Errorf("Endpoint = %q, want the mirror without a trailing slash", got)
	}
	got = resolveURL("org/repo", "main", "model.cact")
	if got != "https://hf-mirror.com/org/repo/resolve/main/model.cact" {
		t.Errorf("resolveURL with mirror = %q", got)
	}
}

func TestOffline(t *testing.T) {
	t.Setenv("HF_HUB_OFFLINE", "")
	if Offline() {
		t.Error("HF_HUB_OFFLINE unset should not be offline")
	}
	t.Setenv("HF_HUB_OFFLINE", "1")
	if !Offline() {
		t.Error("HF_HUB_OFFLINE=1 should be offline")
	}
	t.Setenv("HF_HUB_OFFLINE", "TRUE")
	if !Offline() {
		t.Error("HF_HUB_OFFLINE=TRUE should be offline")
	}
}

// neutralizeEnv clears the resolution inputs a test does not control.
func neutralizeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CACTUS_LIB_PATH", "")
	t.Setenv("CACTUS_NEEDLE_WEIGHTS", "")
	t.Setenv("CACTUS_WHISTLE_WEIGHTS", "")
	t.Setenv("CACTUS_MIRAGE_WEIGHTS", "")
	t.Setenv("HF_HUB_OFFLINE", "")
}

func TestLibraryPathEnvOverride(t *testing.T) {
	neutralizeEnv(t)
	dir := t.TempDir()
	lib := filepath.Join(dir, LibName())
	if err := os.WriteFile(lib, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CACTUS_LIB_PATH", lib)
	got, err := LibraryPath(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != lib {
		t.Errorf("LibraryPath = %q, want %q", got, lib)
	}
}

func TestLibraryPathEnvOverrideMissing(t *testing.T) {
	neutralizeEnv(t)
	t.Setenv("CACTUS_LIB_PATH", filepath.Join(t.TempDir(), "missing.so"))
	_, err := LibraryPath(context.Background())
	if err == nil {
		t.Fatal("an override pointing at a missing file must fail")
	}
	if !strings.Contains(err.Error(), "CACTUS_LIB_PATH") {
		t.Errorf("error should name the variable: %v", err)
	}
}

func TestLibraryPathCacheHit(t *testing.T) {
	neutralizeEnv(t)
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)
	cacheDir, err := EngineCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cached := filepath.Join(cacheDir, LibName())
	if err := os.WriteFile(cached, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LibraryPath(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != cached {
		t.Errorf("LibraryPath = %q, want the cached copy %q", got, cached)
	}
	// A model cache directory resolves under the same root.
	modelDir, err := ModelCacheDir(Needle3)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "cactus", "models", "needle3"); !strings.HasPrefix(modelDir, want) {
		t.Errorf("ModelCacheDir = %q, want under %q", modelDir, want)
	}
}

func TestLibraryPathOfflineFailsFast(t *testing.T) {
	neutralizeEnv(t)
	t.Setenv("HF_HUB_OFFLINE", "1")
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // empty cache, nothing next to the binary
	if _, err := LibraryPath(context.Background()); err == nil {
		t.Fatal("offline mode without an engine should fail fast")
	} else if !strings.Contains(err.Error(), "HF_HUB_OFFLINE") {
		t.Errorf("error should explain the offline refusal: %v", err)
	}
}

func TestWeightsPathEnvOverride(t *testing.T) {
	neutralizeEnv(t)
	weights := filepath.Join(t.TempDir(), "needle3.cact")
	if err := os.WriteFile(weights, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CACTUS_NEEDLE_WEIGHTS", weights)
	got, err := WeightsPath(context.Background(), Needle3)
	if err != nil {
		t.Fatal(err)
	}
	if got != weights {
		t.Errorf("WeightsPath = %q, want %q", got, weights)
	}
	// The override skips the model check only for valid keys.
	if _, err := WeightsPath(context.Background(), "needle2"); err == nil {
		t.Error("needle2 should be rejected before the override is honored")
	}
}

func TestWeightsPathOfflineWithoutCache(t *testing.T) {
	neutralizeEnv(t)
	t.Setenv("HF_HUB_OFFLINE", "1")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if _, err := WeightsPath(context.Background(), Needle3); err == nil {
		t.Fatal("offline mode without cached weights should fail fast")
	} else if !strings.Contains(err.Error(), "HF_HUB_OFFLINE") {
		t.Errorf("error should explain the offline refusal: %v", err)
	}
}

func TestWeightsPathChecksumMismatchInCache(t *testing.T) {
	neutralizeEnv(t)
	t.Setenv("HF_HUB_OFFLINE", "1") // a mismatch must not silently fall back later
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir, err := ModelCacheDir(Whistle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, BaseWeights[Whistle]), []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = WeightsPath(context.Background(), Whistle)
	if err == nil {
		t.Fatal("a cached archive with the wrong checksum must not be served")
	}
}
