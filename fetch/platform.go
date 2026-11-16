// Package fetch locates, downloads and caches the shared Cactus inference
// engine and the model weights that run on it.
//
// The engine is published on the Hugging Face Hub as per-platform wheel
// packages whose payload is the shared library (libcactus.so, libcactus.dll
// or libcactus.dylib). One engine runs every model: Needle 3 (text), Whistle
// (speech) and Mirage (vision) are separate .cact weight archives in their
// own repositories. Every engine and base-model download uses a pinned
// commit and a SHA-256 checksum.
package fetch

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Model keys understood by this package.
const (
	Needle3 = "needle3"
	Whistle = "whistle"
	Mirage  = "mirage"
)

// The pinned engine release. ENGINE_REVISION is the commit the release was
// cut from and MANIFEST_SHA256 pins the release manifest itself; every
// artifact the manifest lists carries its own size and SHA-256, which the
// download path verifies.
const (
	EngineRepo       = "Cactus-Compute/cactus"
	EngineVersion    = "4.1.1"
	EngineRevision   = "d2c5cd70432ed78345f3ba6b620bff2feeec6c86"
	ManifestSHA256   = "27168631232dbff52a205b54bbfad54fd9b81f06fa368657d07449e2221f5e25"
	EngineReleaseDir = "releases/" + EngineVersion
)

// ModelRepos maps a model key to its Hugging Face repository.
var ModelRepos = map[string]string{
	Needle3: "Cactus-Compute/needle3",
	Whistle: "Cactus-Compute/whistle",
	Mirage:  "Cactus-Compute/mirage",
}

// ModelRevisions pins the commit each base model is downloaded from.
var ModelRevisions = map[string]string{
	Needle3: "d6c7d6a2e9655505e0f8e23339356dd1705900eb",
	Whistle: "741118687e1f008f06d1351a402a731274789790",
	Mirage:  "76c1e9e53381aca8fc3843c636216c96ff220122",
}

// ModelSHA256 pins the archive checksum each base model must match.
var ModelSHA256 = map[string]string{
	Needle3: "c9d915eca282ed42d1a09b143b592adb4cc6744ffe2d294adf5cfc5548170c38",
	Whistle: "b6e02f048568ac5d01a2042556c658061e699acbc0aa2a1439f52f3d461dffeb",
	Mirage:  "4249a0f7515272ff76b9414bec17d7e7daf84b15b66c7fb4f17fce1e66f9f237",
}

// BaseWeights names the archive file of each model inside its repository.
var BaseWeights = map[string]string{
	Needle3: "needle3.cact",
	Whistle: "whistle.cact",
	Mirage:  "mirage.cact",
}

// WeightsEnv maps a model key to the environment variable that overrides
// its base weights path.
var WeightsEnv = map[string]string{
	Needle3: "CACTUS_NEEDLE_WEIGHTS",
	Whistle: "CACTUS_WHISTLE_WEIGHTS",
	Mirage:  "CACTUS_MIRAGE_WEIGHTS",
}

// Platforms is the list of standalone engine-runner platform folders in the
// engine release, used by DownloadPlatform.
var Platforms = []string{
	"macos-arm64", "linux-x86_64", "linux-arm64", "linux-armv7",
	"linux-riscv64", "linux-mipsel", "windows-x86_64", "windows-arm64",
	"android-arm64", "android-armv7", "android-riscv64",
	"ios-arm64", "ios-sim-arm64", "tvos-arm64", "watchos-arm64", "wasm",
	"wasm-component",
}

// WheelTags are the wheel platform tags a library download can pick from.
var WheelTags = []string{
	"macosx_11_0_arm64", "macosx_11_0_x86_64",
	"manylinux2014_aarch64", "manylinux2014_x86_64",
	"musllinux_1_2_aarch64", "musllinux_1_2_x86_64",
	"win_amd64", "win_arm64",
}

// ValidModel reports whether key is a model this package can fetch.
func ValidModel(key string) bool {
	_, ok := BaseWeights[key]
	return ok
}

// CheckModel validates a model key, returning a friendly error otherwise.
func CheckModel(key string) error {
	if ValidModel(key) {
		return nil
	}
	return fmt.Errorf("unsupported model %q; pick one of: %s, %s, %s",
		key, Needle3, Whistle, Mirage)
}

// LibName returns the file name of the engine shared library for the
// current operating system.
func LibName() string {
	switch runtime.GOOS {
	case "darwin":
		return "libcactus.dylib"
	case "windows":
		return "libcactus.dll"
	default:
		return "libcactus.so"
	}
}

// LibNameForTag returns the library file name inside a wheel built for the
// given platform tag.
func LibNameForTag(tag string) string {
	switch {
	case strings.HasPrefix(tag, "macosx"):
		return "libcactus.dylib"
	case strings.HasPrefix(tag, "win"):
		return "libcactus.dll"
	default:
		return "libcactus.so"
	}
}

// localLibNames lists the library file names accepted next to the running
// executable: the canonical name first.
func localLibNames() []string {
	return []string{LibName()}
}

// PlatformTag returns the wheel platform tag matching the current machine,
// e.g. manylinux2014_x86_64, musllinux_1_2_aarch64, win_amd64 or
// macosx_11_0_arm64. An error is returned for machines without a published
// wheel build; those can still use an engine placed manually (see
// LibraryPath).
func PlatformTag() (string, error) {
	return PlatformTagFor(runtime.GOOS, runtime.GOARCH)
}

// PlatformTagFor maps an explicit GOOS/GOARCH pair to its wheel tag,
// detecting the libc flavour on Linux.
func PlatformTagFor(goos, goarch string) (string, error) {
	switch goos {
	case "darwin":
		if goarch == "arm64" {
			return "macosx_11_0_arm64", nil
		}
		return "macosx_11_0_x86_64", nil
	case "windows":
		switch goarch {
		case "arm64":
			return "win_arm64", nil
		case "amd64":
			return "win_amd64", nil
		}
		return "", fmt.Errorf("no Cactus engine wheel for windows/%s", goarch)
	case "linux":
		arch := ""
		switch goarch {
		case "arm64", "aarch64":
			arch = "aarch64"
		case "amd64", "x86_64":
			arch = "x86_64"
		default:
			return "", fmt.Errorf("no Cactus engine wheel for linux/%s; "+
				"build the engine from the platform's libcactus.a or set CACTUS_LIB_PATH to a compatible library", goarch)
		}
		if isMusl() {
			return "musllinux_1_2_" + arch, nil
		}
		return "manylinux2014_" + arch, nil
	}
	return "", fmt.Errorf("no Cactus engine wheel for %s/%s", goos, goarch)
}

// isMusl reports whether the current Linux userland is musl-based (Alpine
// and friends), which needs the musllinux engine build.
func isMusl() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if data, err := os.ReadFile("/proc/self/maps"); err == nil {
		if strings.Contains(string(data), "musl") {
			return true
		}
		if strings.Contains(string(data), "/libc.so.6") || strings.Contains(string(data), "/ld-linux") {
			return false
		}
	}
	for _, pattern := range []string{"/lib/ld-musl-*.so*", "/usr/lib/ld-musl-*.so*"} {
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			return true
		}
	}
	for _, pattern := range []string{"/lib/libc.so.6", "/lib64/libc.so.6", "/lib/*-linux-gnu/libc.so.6"} {
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			return false
		}
	}
	return false
}

// cacheRoot is the shared cache root, ~/.cache/cactus by default.
func cacheRoot() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); xdg != "" {
		return filepath.Join(xdg, "cactus"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cactus: cannot determine home directory for the cache: %w", err)
	}
	return filepath.Join(home, ".cache", "cactus"), nil
}

// EngineCacheDir is where the engine library for the pinned release lives.
func EngineCacheDir() (string, error) {
	root, err := cacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "engine", EngineVersion, EngineRevision), nil
}

// ModelCacheDir is where a model's base weights live.
func ModelCacheDir(model string) (string, error) {
	if err := CheckModel(model); err != nil {
		return "", err
	}
	root, err := cacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "models", BaseWeights[model][:len(BaseWeights[model])-len(".cact")], ModelRevisions[model]), nil
}
