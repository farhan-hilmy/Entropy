package app

import (
	"context"

	"entropy/internal/config"
)

// Config returns the config file as written, without defaults, for editing.
func (a *App) Config() (config.Snapshot, error) {
	return config.Read(a.configPath)
}

type EditResult struct {
	Hash string     `json:"hash"` // of the file as saved
	Sync SyncResult `json:"sync"`
}

// EditConfig saves an edit made in the UI to the config file, then syncs it.
// hash is from the Config the edit was based on.
func (a *App) EditConfig(ctx context.Context, hash string, e config.Edit) (EditResult, error) {
	a.editMu.Lock()
	defer a.editMu.Unlock()
	newHash, err := config.Apply(a.configPath, hash, e)
	if err != nil {
		return EditResult{}, err
	}
	a.log.Info("config edited from the UI", "config", a.configPath)
	return EditResult{Hash: newHash, Sync: a.Sync(ctx)}, nil
}

// ExperimentSource returns one experiment as it's written in the config file,
// comments included, for the experiment editor.
func (a *App) ExperimentSource(name string) (config.ExperimentSource, error) {
	return config.ReadExperiment(a.configPath, name)
}
