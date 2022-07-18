//go:build amd64 || arm64
// +build amd64 arm64

package machine

import (
	"errors"
	"fmt"
	"os"

	"net/http"
	"net/url"
	"path/filepath"
	"time"
)

const (
	githubLatestReleaseURL = "https://github.com/containers/podman-wsl-fedora/releases/latest/download/rootfs.tar.xz"
)

type FedoraDownload struct {
	Download
}

func NewFedoraDownloader(vmType, vmName, releaseStream string) (DistributionDownload, error) {
	downloadURL, size, err := getFedoraDownload(githubLatestReleaseURL)
	if err != nil {
		return nil, err
	}

	cacheDir, err := GetCacheDir(vmType)
	if err != nil {
		return nil, err
	}

	imageName := "rootfs.tar.xz"

	f := FedoraDownload{
		Download: Download{
			Arch:      getFcosArch(),
			Artifact:  artifact,
			CacheDir:  cacheDir,
			Format:    Format,
			ImageName: imageName,
			LocalPath: filepath.Join(cacheDir, imageName),
			URL:       downloadURL,
			VMName:    vmName,
			Size:      size,
		},
	}
	dataDir, err := GetDataDir(vmType)
	if err != nil {
		return nil, err
	}
	f.Download.LocalUncompressedFile = f.getLocalUncompressedFile(dataDir)
	return f, nil
}

func (f FedoraDownload) Get() *Download {
	return &f.Download
}

func (f FedoraDownload) HasUsableCache() (bool, error) {
	info, err := os.Stat(f.LocalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return info.Size() == f.Size, nil
}

func (f FedoraDownload) CleanCache() error {
	// Set cached image to expire after 2 weeks
	expire := 14 * 24 * time.Hour
	return removeImageAfterExpire(f.CacheDir, expire)
}

func getFedoraDownload(releaseURL string) (*url.URL, int64, error) {
	downloadURL, err := url.Parse(releaseURL)
	if err != nil {
		return nil, -1, fmt.Errorf("invalid URL generated from discovered Fedora file: %s: %w", releaseURL, err)
	}

	resp, err := http.Head(releaseURL)
	if err != nil {
		return nil, -1, fmt.Errorf("head request failed: %s: %w", releaseURL, err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, -1, fmt.Errorf("head request failed: %s: %w", releaseURL, err)
	}

	return downloadURL, resp.ContentLength, nil
}
