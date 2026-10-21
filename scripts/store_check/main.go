// Command store_check validates this repository against the CLIProxyAPI plugin
// store using the host's own client code (sdk/pluginstore): it fetches
// registry.json, resolves the latest GitHub release for the plugin, downloads the
// archive for a target platform, verifies checksums.txt, and installs the library
// into a scratch directory.
//
// This is the strongest available check that the repository follows the store
// contract, because the parsing, asset selection, checksum verification and
// archive rules are all the host's own implementations - the same code that runs
// when a user clicks "install" in the CPA plugin store.
//
// It is a separate Go module on purpose: it depends on CLIProxyAPI/v8 but must
// not become part of the plugin build.
//
//	go run . --registry https://raw.githubusercontent.com/qizhuxu/anyrouter-cpa-plugin/main/registry.json
//	go run . --goos linux --goarch arm64        # check another platform's asset
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginstore"
)

func main() {
	registryURL := flag.String("registry", "https://raw.githubusercontent.com/qizhuxu/anyrouter-cpa-plugin/main/registry.json", "plugin store registry URL")
	pluginID := flag.String("id", "anyrouter", "plugin id to validate")
	installDir := flag.String("dir", "", "directory to install into (default: a temporary directory)")
	goos := flag.String("goos", runtime.GOOS, "target GOOS for asset selection")
	goarch := flag.String("goarch", runtime.GOARCH, "target GOARCH for asset selection")
	timeout := flag.Duration("timeout", 3*time.Minute, "overall timeout")
	flag.Parse()

	if errRun := run(*registryURL, *pluginID, *installDir, *goos, *goarch, *timeout); errRun != nil {
		fmt.Fprintf(os.Stderr, "store check FAILED: %v\n", errRun)
		os.Exit(1)
	}
	fmt.Println("store check PASSED")
}

func run(registryURL, pluginID, installDir, goos, goarch string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client := pluginstore.NewClient(http.DefaultClient, registryURL)

	fmt.Printf("registry : %s\n", registryURL)
	registry, errFetch := client.FetchRegistry(ctx)
	if errFetch != nil {
		return fmt.Errorf("FetchRegistry: %w", errFetch)
	}
	fmt.Printf("schema   : %d\n", registry.SchemaVersion)

	var plugin pluginstore.Plugin
	found := false
	for _, candidate := range registry.Plugins {
		if candidate.ID == pluginID {
			plugin, found = candidate, true
			break
		}
	}
	if !found {
		return fmt.Errorf("plugin %q not present in the registry", pluginID)
	}
	fmt.Printf("plugin   : id=%s name=%q version=%s install=%s\n", plugin.ID, plugin.Name, plugin.Version, plugin.Install.Type)
	fmt.Printf("repo     : %s\n", plugin.Repository)
	fmt.Printf("license  : %s\n", plugin.License)

	release, errRelease := client.FetchLatestRelease(ctx, plugin)
	if errRelease != nil {
		return fmt.Errorf("FetchLatestRelease: %w", errRelease)
	}
	fmt.Printf("release  : %s\n", release.TagName)
	for _, asset := range release.Assets {
		fmt.Printf("  asset  : %s\n", asset.Name)
	}

	if installDir == "" {
		tmp, errTemp := os.MkdirTemp("", "anyrouter-store-check-")
		if errTemp != nil {
			return errTemp
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		installDir = tmp
	}
	if errMkdir := os.MkdirAll(installDir, 0o755); errMkdir != nil {
		return errMkdir
	}

	fmt.Printf("target   : %s/%s -> %s\n", goos, goarch, installDir)
	result, errInstall := client.Install(ctx, plugin, pluginstore.InstallOptions{
		PluginsDir:   installDir,
		GOOS:         goos,
		GOARCH:       goarch,
		PluginLoaded: func() bool { return false },
	})
	if errInstall != nil {
		return fmt.Errorf("Install: %w", errInstall)
	}
	fmt.Printf("installed: id=%s version=%s tag=%s path=%s overwritten=%t\n",
		result.ID, result.Version, result.ReleaseTag, result.Path, result.Overwritten)

	info, errStat := os.Stat(result.Path)
	if errStat != nil {
		return fmt.Errorf("installed library missing: %w", errStat)
	}
	digest, errDigest := fileSHA256(result.Path)
	if errDigest != nil {
		return errDigest
	}
	fmt.Printf("library  : %d bytes sha256=%s\n", info.Size(), digest)
	return nil
}

func fileSHA256(path string) (string, error) {
	handle, errOpen := os.Open(filepath.Clean(path))
	if errOpen != nil {
		return "", errOpen
	}
	defer func() { _ = handle.Close() }()
	hasher := sha256.New()
	if _, errCopy := io.Copy(hasher, handle); errCopy != nil {
		return "", errCopy
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
