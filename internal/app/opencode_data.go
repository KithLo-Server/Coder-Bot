package app

import (
	"net/url"
	"os"
	"path/filepath"

	"opencodebot/internal/config"
	"opencodebot/internal/docker"
)

func (a *App) opencodeDataPath(name string) string {
	safe := url.PathEscape(name)
	if safe == "" || safe == "." || safe == ".." {
		safe = "repo"
	}
	return filepath.Join(a.Cfg.DataDir, "opencode", safe)
}

func (a *App) prepareOpencodeData(name string) (string, error) {
	dataPath := a.opencodeDataPath(name)
	if err := os.MkdirAll(dataPath, 0o755); err != nil {
		return "", err
	}
	return dataPath, nil
}

func (a *App) removeOpencodeData(name string) error {
	return os.RemoveAll(a.opencodeDataPath(name))
}

func (a *App) workerVolumes(name string) ([]docker.Volume, error) {
	dataPath, err := a.prepareOpencodeData(name)
	if err != nil {
		return nil, err
	}
	volumes := a.repoVolumes(name)
	volumes = append(volumes, docker.Volume{
		Source: dataPath,
		Target: config.OpenCodeDataDir,
		Mode:   "rw",
	})
	return volumes, nil
}
