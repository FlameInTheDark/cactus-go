// Resolution order for an engine library:
//
//  1. the CACTUS_LIB_PATH environment variable;
//  2. a library placed next to the running executable;
//  3. the shared cache directory above;
//  4. a fresh download from the Hub, unless HF_HUB_OFFLINE is set.
//
// Resolution order for base weights:
//
//  1. the model's environment variable (CACTUS_NEEDLE_WEIGHTS,
//     CACTUS_WHISTLE_WEIGHTS or CACTUS_MIRAGE_WEIGHTS);
//  2. the shared cache, when the file exists and matches the pinned
//     checksum;
//  3. a fresh download from the model's pinned repository revision,
//     checksum-verified.
package fetch

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// ProgressHook, when set, receives download progress callbacks. Assign it
// before the first fetch if you want progress reporting.
var ProgressHook ProgressFunc

func client() *httpClient { return newClient(ProgressHook) }

// LibraryPath resolves the engine library path, fetching it from the
// Hugging Face Hub on first use. It is the entry point the agents use to
// find the engine.
func LibraryPath(ctx context.Context) (string, error) {
	// 1. environment override
	if override := strings.TrimSpace(os.Getenv("CACTUS_LIB_PATH")); override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("cactus: CACTUS_LIB_PATH points at %s which does not exist", override)
		}
		return override, nil
	}
	// 2. next to the executable
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, name := range localLibNames() {
			local := filepath.Join(dir, name)
			if _, err := os.Stat(local); err == nil {
				return local, nil
			}
		}
	}
	// 3. the shared cache
	cacheDir, err := EngineCacheDir()
	if err != nil {
		return "", err
	}
	cached := filepath.Join(cacheDir, LibName())
	if _, err := os.Stat(cached); err == nil {
		return cached, nil
	}
	// 4. download, unless offline was requested
	if Offline() {
		return "", fmt.Errorf("cactus: engine library not found and HF_HUB_OFFLINE=1 prevents downloading; "+
			"place %s in %s or set CACTUS_LIB_PATH", LibName(), cacheDir)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	return FetchLibrary(ctx, "", cacheDir)
}

// WeightsPath resolves the base weights archive of a model, downloading it
// from its pinned repository revision on first use. The archive is
// checksum-verified both on download and on cache hits.
func WeightsPath(ctx context.Context, model string) (string, error) {
	if err := CheckModel(model); err != nil {
		return "", err
	}
	if override := strings.TrimSpace(os.Getenv(WeightsEnv[model])); override != "" {
		return override, nil
	}
	name := BaseWeights[model]
	destDir, err := ModelCacheDir(model)
	if err != nil {
		return "", err
	}
	out := filepath.Join(destDir, name)
	expected := ModelSHA256[model]
	if _, err := os.Stat(out); err == nil {
		if expected == "" {
			return out, nil
		}
		if sum, err := fileSHA256(out); err == nil && sum == expected {
			return out, nil
		}
	}
	if Offline() {
		return "", fmt.Errorf("cactus: %s not found and HF_HUB_OFFLINE=1 prevents downloading; "+
			"place it in %s or set %s", name, destDir, WeightsEnv[model])
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	c := client()
	url := resolveURL(ModelRepos[model], ModelRevisions[model], name)
	if _, err := c.downloadToFile(ctx, name, url, out, false); err != nil {
		return "", fmt.Errorf("cactus: cannot fetch %s from %s: %w", name, ModelRepos[model], err)
	}
	if expected != "" {
		sum, err := fileSHA256(out)
		if err != nil {
			return "", err
		}
		if sum != expected {
			os.Remove(out)
			return "", fmt.Errorf("cactus: model checksum mismatch: %s", name)
		}
	}
	return out, nil
}

// releaseManifest is the checksum-pinned file listing of an engine release.
type releaseManifest struct {
	Schema  int                      `json:"schema"`
	Version string                   `json:"version"`
	API     string                   `json:"api"`
	Files   map[string]manifestEntry `json:"files"`
}

type manifestEntry struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// fetchManifest downloads the pinned release manifest and verifies it
// against MANIFEST_SHA256 and the pinned version/API markers.
func fetchManifest(ctx context.Context) (*releaseManifest, error) {
	if len(EngineRevision) != 40 || len(ManifestSHA256) != 64 {
		return nil, fmt.Errorf("cactus: this client has no verified engine release pin")
	}
	c := client()
	url := resolveURL(EngineRepo, EngineRevision, EngineReleaseDir+"/manifest.json")
	body, err := c.getJSON(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("cactus: cannot fetch the engine release manifest: %w", err)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != ManifestSHA256 {
		return nil, fmt.Errorf("cactus: engine release manifest checksum mismatch")
	}
	var manifest releaseManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("cactus: engine release manifest is not valid JSON: %w", err)
	}
	if manifest.Version != EngineVersion || manifest.API != "cactus" {
		return nil, fmt.Errorf("cactus: incompatible engine release manifest")
	}
	return &manifest, nil
}

// FetchLibrary downloads the engine wheel for a platform tag (the current
// machine when empty), verifies it against the pinned release manifest,
// extracts the shared library into destDir and returns its path.
func FetchLibrary(ctx context.Context, tag, destDir string) (string, error) {
	if tag == "" {
		var err error
		if tag, err = PlatformTag(); err != nil {
			return "", err
		}
	}
	if !validWheelTag(tag) {
		return "", fmt.Errorf("cactus: unsupported wheel tag %q", tag)
	}
	if destDir == "" {
		return "", fmt.Errorf("cactus: FetchLibrary needs a destination directory")
	}
	manifest, err := fetchManifest(ctx)
	if err != nil {
		return "", err
	}
	wheelName := fmt.Sprintf("cactus_runtime-%s-py3-none-%s.whl", EngineVersion, tag)
	entry, ok := manifest.Files["python/"+wheelName]
	if !ok {
		return "", fmt.Errorf("cactus: the pinned engine release holds no wheel for %s", tag)
	}
	wheelURL := resolveURL(EngineRepo, EngineRevision, EngineReleaseDir+"/python/"+wheelName)
	wheelPath := filepath.Join(os.TempDir(), "cactus-"+EngineVersion+"-"+tag+".whl")
	c := client()
	if _, err := c.downloadToFile(ctx, wheelName, wheelURL, wheelPath, false); err != nil {
		return "", fmt.Errorf("cactus: cannot fetch engine wheel %s: %w", wheelName, err)
	}
	defer os.Remove(wheelPath)
	if err := verifyFile(wheelPath, entry); err != nil {
		return "", fmt.Errorf("cactus: engine artifact checksum mismatch: %s: %w", wheelName, err)
	}
	libName := LibNameForTag(tag)
	if tag == mustPlatformTag() {
		libName = LibName()
	}
	return extractWheelLibrary(wheelPath, libName, destDir)
}

func mustPlatformTag() string {
	tag, err := PlatformTag()
	if err != nil {
		return ""
	}
	return tag
}

func validWheelTag(tag string) bool {
	for _, t := range WheelTags {
		if t == tag {
			return true
		}
	}
	return false
}

func verifyFile(path string, entry manifestEntry) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if entry.Size > 0 && st.Size() != entry.Size {
		return fmt.Errorf("size %d, expected %d", st.Size(), entry.Size)
	}
	if entry.SHA256 == "" {
		return nil
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if sum != entry.SHA256 {
		return fmt.Errorf("sha256 mismatch")
	}
	return nil
}

func fileSHA256(path string) (string, error) {
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

// extractWheelLibrary pulls cactus/<libName> out of a wheel (which is a
// plain zip archive) into destDir atomically.
func extractWheelLibrary(wheelPath, libName, destDir string) (string, error) {
	r, err := zip.OpenReader(wheelPath)
	if err != nil {
		return "", fmt.Errorf("cactus: engine wheel %s is not a valid archive: %w", wheelPath, err)
	}
	defer r.Close()
	member := "cactus/" + libName
	var found *zip.File
	for _, f := range r.File {
		if f.Name == member {
			found = f
			break
		}
	}
	if found == nil {
		return "", fmt.Errorf("cactus: engine wheel %s does not contain %s", wheelPath, member)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(destDir, libName)
	part := out + ".part"
	f, err := os.Create(part)
	if err != nil {
		return "", err
	}
	src, err := found.Open()
	if err != nil {
		f.Close()
		os.Remove(part)
		return "", err
	}
	if _, err := io.Copy(f, src); err != nil {
		src.Close()
		f.Close()
		os.Remove(part)
		return "", err
	}
	src.Close()
	if err := f.Close(); err != nil {
		os.Remove(part)
		return "", err
	}
	_ = os.Chmod(part, 0o644)
	if err := os.Rename(part, out); err != nil {
		os.Remove(part)
		return "", err
	}
	return out, nil
}

// DownloadPlatform copies one platform's standalone engine-runner files
// (for example linux-x86_64 or wasm) from the pinned release manifest into
// outDir/<platform>/, verifying every checksum and marking native runners
// executable. It returns the written paths.
func DownloadPlatform(ctx context.Context, platform, outDir string) ([]string, error) {
	if !validPlatform(platform) {
		return nil, fmt.Errorf("cactus: unknown platform %q, pick one of: %s", platform, strings.Join(Platforms, ", "))
	}
	manifest, err := fetchManifest(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for name := range manifest.Files {
		if strings.HasPrefix(name, platform+"/") {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("cactus: platform %q is missing from the pinned engine release", platform)
	}
	sort.Strings(names)
	dest := filepath.Join(outDir, platform)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	c := client()
	var out []string
	for _, name := range names {
		base := filepath.Base(name)
		executable := base == "cactus" || base == "cactus.exe"
		target := filepath.Join(dest, base)
		url := resolveURL(EngineRepo, EngineRevision, EngineReleaseDir+"/"+name)
		if _, err := c.downloadToFile(ctx, base, url, target, executable && runtime.GOOS != "windows"); err != nil {
			return nil, err
		}
		if err := verifyFile(target, manifest.Files[name]); err != nil {
			return nil, fmt.Errorf("cactus: engine artifact checksum mismatch: %s: %w", name, err)
		}
		out = append(out, target)
	}
	return out, nil
}

func validPlatform(name string) bool {
	for _, p := range Platforms {
		if p == name {
			return true
		}
	}
	return false
}

// DownloadWeights pulls a published .cact weights archive from the Hub.
// The spec is either "<org>/<repo>/<file>.cact" or "<org>/<repo>" when the
// repository holds exactly one archive. The file is placed in outDir and
// its path returned.
func DownloadWeights(ctx context.Context, spec, outDir string) (string, error) {
	parts := strings.Split(strings.Trim(spec, "/"), "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("cactus: pass <org>/<repo>/<file>.cact or <org>/<repo>")
	}
	repo := strings.Join(parts[:2], "/")
	filename := strings.Join(parts[2:], "/")
	c := client()
	if filename == "" {
		files, err := c.listRepoFiles(ctx, repo)
		if err != nil {
			return "", err
		}
		var cacts []string
		for _, f := range files {
			if strings.HasSuffix(f, ".cact") {
				cacts = append(cacts, f)
			}
		}
		if len(cacts) != 1 {
			shown := cacts
			if len(shown) > 5 {
				shown = shown[:5]
			}
			return "", fmt.Errorf("cactus: %s holds %d .cact files, name one: %s", repo, len(cacts), strings.Join(shown, ", "))
		}
		filename = cacts[0]
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(outDir, filepath.Base(filename))
	return c.downloadToFile(ctx, filepath.Base(filename), resolveURL(repo, "main", filename), dest, false)
}
