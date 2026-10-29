package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/keystore"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// configFile is the config file configPath names, the default one for "".
func configFile(configPath string) (string, error) {
	if configPath != "" {
		return configPath, nil
	}
	path, err := config.Path()
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	return path, nil
}

// viaBridge attaches to the bridge this device joined (setup.JoinedBridge), for the
// config configPath names; joined is false, and nothing done, when it joined none.
func viaBridge(ctx context.Context, configPath string, timeout time.Duration) (at attached, joined bool, err error) {
	path, err := configFile(configPath)
	if err != nil {
		return attached{}, false, err
	}
	b, joined, err := setup.JoinedBridge(path)
	if err != nil || !joined {
		return attached{}, joined, err
	}
	at, err = attachBridge(ctx, path, b, timeout)
	return at, true, err
}

// restarter restarts the attached kithd, or is nil for a bridge elsewhere, which is not
// this machine's to restart.
func (at attached) restarter() func(ctx context.Context) error {
	if at.remote {
		return nil
	}
	return func(ctx context.Context) error {
		return daemon.Restart(ctx, at.storage, at.launch, readyTimeout)
	}
}

// attachBridge attaches to the bridge b over TLS as this device, never starting a daemon:
// the config is the bridge's, read through it, and the device file beside path and the
// storage are this machine's.
func attachBridge(ctx context.Context, path string, b config.Bridge, timeout time.Duration) (attached, error) {
	device, dev, err := loadDevice(path)
	if err != nil {
		return attached{remote: true}, err
	}
	storage, allowFile, err := setup.ClientStorage(path)
	if err != nil {
		return attached{remote: true}, err
	}
	failed := attached{remote: true, storage: storage}
	if dev.Display.Media.CacheDir == "" {
		dev.Display.Media.CacheDir = storage.MediaDir()
	}
	cert, err := keystore.ClientCertificate(storage, allowFile)
	if err != nil {
		return failed, fmt.Errorf("attach to kithd: %w", err)
	}
	backend, err := daemon.AttachBridge(ctx, cert, b, timeout)
	if err != nil {
		return failed, fmt.Errorf("attach to kithd: %w", err)
	}
	snap, err := backend.GetConfig(ctx)
	if err != nil {
		backend.Stop()
		return failed, fmt.Errorf("read the bridge's config: %w", err)
	}
	if err := setup.Validate(snap.Config); err != nil {
		backend.Stop()
		return failed, fmt.Errorf("the bridge's config: %w", err)
	}
	at := attached{
		cfg: snap.Config, remote: true, revision: snap.Revision,
		device: device, dev: dev, storage: storage, backend: backend,
	}
	// kith-notify shows the bridge's notifications on this machine too, dialing it as this does.
	launch := daemon.Launch{ConfigPath: path, OwnConfig: !isDefaultConfig(path) || !inDefaultDirs(storage)}
	if nerr := daemon.StartNotifier(ctx, storage, launch); nerr != nil {
		at.note = strings.TrimSpace("Desktop notifications are off: kith-notify did not start: " + nerr.Error())
	}
	return at, nil
}
