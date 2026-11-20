package daemon

import (
	"bytes"
	"context"
	"crypto"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The bridges of a mesh share config.toml's shared keys (config.Local names the rest):
// each a register of the mesh store. kithd renders the file as its local keys plus the
// mesh's shared ones, and reads what else changes the file as writes: a client's (Write)
// and a hand edit's (Settle). planning/design-mesh.md, Config.

// configSetAsideSuffix names the copy of config.toml a bridge kept as it took a mesh's
// config.
const configSetAsideSuffix = ".pre-mesh"

// Share has f keep config.toml's shared keys in the mesh store paired keeps, saying
// through say what a user must hear (a config set aside, a merged one that does not
// check).
func (f *ConfigFile) Share(paired *Devices, say func(string), log *slog.Logger) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paired, f.say, f.log = paired, say, log
}

// meshLocked is the mesh store and the key this bridge signs with; none while f is not
// shared or the store is not to be had, and the file is then the config alone.
func (f *ConfigFile) meshLocked() (RosterStore, crypto.Signer, bool) {
	if f.paired == nil {
		return nil, nil, false
	}
	store, _, _, signer, err := f.paired.meshParts()
	if err != nil {
		return nil, nil, false
	}
	members, _, err := store.Roster() // none until the roster file is made the store's genesis
	return store, signer, err == nil && len(members) > 0
}

// Settle brings config.toml and the mesh's shared config together, then applies the
// file when it wrote it, or when edited (a hand edit, or one may have been made). It
// reports whether it wrote the file. Without a mesh the file is the config, applied
// when edited.
//
//   - The first time (or after joining a mesh) the bridge takes the mesh's config,
//     its own set aside in config.toml.pre-mesh, or, the first bridge of its mesh, makes
//     its own the mesh's.
//   - A file that differs from the last rendering was edited by hand: a shared key
//     counts as edited only if its value differs from its value in every rendering
//     since the last hand edit was read, so an editor that opened the file before a
//     remote change arrived does not undo it by saving a key it never touched. Edited
//     keys become one write.
//   - Then the file is rendered from its local keys and the mesh's shared ones, unless
//     that config does not decode or check: the bridge keeps running its last valid
//     config, and says so.
func (f *ConfigFile) Settle(ctx context.Context, check CheckConfig, apply Reload, edited bool) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	wrote, err := f.settleLocked(ctx, check)
	if err != nil {
		return false, err
	}
	if (wrote || edited) && apply != nil {
		return wrote, apply(ctx)
	}
	return wrote, nil
}

// settleLocked is Settle without applying.
func (f *ConfigFile) settleLocked(ctx context.Context, check CheckConfig) (bool, error) {
	store, signer, ok := f.meshLocked()
	if !ok {
		return false, nil
	}
	data, err := os.ReadFile(f.path)
	if err != nil {
		return false, fmt.Errorf("daemon: read the config: %w", err)
	}
	renderings, joined, err := store.Rendered(ctx)
	if err != nil {
		return false, fmt.Errorf("daemon: %w", err)
	}
	file, ferr := config.Decode(string(data))
	if ferr == nil && check != nil {
		ferr = check(ctx, file)
	}
	switch {
	case joined || len(renderings) == 0:
		if ferr != nil {
			return false, fmt.Errorf("daemon: the config is shared with the mesh once it reads: %w", ferr)
		}
		own, serr := sharedLeaves(file)
		if serr != nil {
			return false, serr
		}
		if mesh, _ := store.Config(); joined || len(mesh) > 0 {
			f.setAsideLocked(data, own, mesh)
		} else if werr := store.WriteConfig(ctx, signer, own); werr != nil {
			return false, fmt.Errorf("daemon: share the config with the mesh: %w", werr)
		}
		return f.renderLocked(ctx, check, store, data, file, true)
	case string(data) != renderings[len(renderings)-1]:
		if ferr != nil {
			return false, ferr
		}
		edits, herr := handEdits(file, renderings)
		if herr != nil {
			return false, herr
		}
		if werr := store.WriteConfig(ctx, signer, edits); werr != nil {
			return false, fmt.Errorf("daemon: share a hand edit of the config: %w", werr)
		}
		return f.renderLocked(ctx, check, store, data, file, true)
	}
	if file, err = config.Decode(string(data)); err != nil { // its rendering: only its local keys are read
		return false, fmt.Errorf("daemon: %w", err)
	}
	return f.renderLocked(ctx, check, store, data, file, false)
}

// renderLocked writes the file (as data, its local keys file's) as its local keys plus
// the mesh's shared ones, when that differs, keeping the rendering; fresh drops the
// renderings before it. A config that does not decode or check is not written. A file
// changed since data was read is not written over: it is read next, as a hand edit.
func (f *ConfigFile) renderLocked(ctx context.Context, check CheckConfig, store RosterStore, data []byte, file config.Config, fresh bool) (bool, error) {
	leaves, err := config.Leaves(file)
	if err != nil {
		return false, fmt.Errorf("daemon: %w", err)
	}
	maps.DeleteFunc(leaves, func(path string, _ []byte) bool { return !config.IsLocal(path) })
	mesh, _ := store.Config()
	for path, v := range mesh {
		if !config.IsLocal(path) {
			leaves[path] = v
		}
	}
	cfg, err := config.FromLeaves(leaves)
	if err == nil && check != nil {
		err = check(ctx, cfg)
	}
	if f.refusedLocked(err) {
		return false, nil
	}
	body := config.Encode(cfg)
	if body == string(data) {
		if fresh {
			if kerr := store.KeepRendered(ctx, body, true); kerr != nil {
				return false, fmt.Errorf("daemon: %w", kerr)
			}
		}
		return false, nil
	}
	now, err := os.ReadFile(f.path)
	switch {
	case err != nil:
		return false, fmt.Errorf("daemon: read the config: %w", err)
	case !bytes.Equal(now, data):
		return false, nil
	}
	// Kept before it is written: a file left unwritten then reads as the hand edit of no key.
	if err := store.KeepRendered(ctx, body, fresh); err != nil {
		return false, fmt.Errorf("daemon: %w", err)
	}
	if err := config.Save(f.path, cfg); err != nil {
		return false, unwritable{err}
	}
	return true, nil
}

// refusedLocked reports whether the mesh's config was refused here (err, why it does not
// decode or check), saying so once for each reason.
func (f *ConfigFile) refusedLocked(err error) bool {
	if err == nil {
		f.refused = ""
		return false
	}
	if err.Error() == f.refused {
		return true
	}
	f.refused = err.Error()
	f.logger().Warn("the mesh's config does not check here; this bridge runs its last valid config until a change fixes it", "err", err)
	f.tell(fmt.Sprintf("The config the mesh's bridges share does not check here: %v. This bridge runs its last valid config until a change fixes it.", err))
	return true
}

// setAsideLocked keeps data, the file as it was before this bridge took the mesh's
// config, in config.toml.pre-mesh, and says how many of its shared keys (own) differ
// from the mesh's.
func (f *ConfigFile) setAsideLocked(data []byte, own, mesh map[string][]byte) {
	aside := f.path + configSetAsideSuffix
	dir, name, err := configFolder(f.path)
	if err == nil {
		err = dir.WriteFile(name+configSetAsideSuffix, data, 0o600)
		_ = dir.Close()
	}
	if err != nil {
		f.logger().Error("keep this bridge's own config before taking the mesh's", "file", aside, "err", err)
	}
	n := len(configChanges(own, mesh))
	f.logger().Warn("this bridge took the config its mesh shares; its own is set aside", "file", aside, "keys", n)
	if n > 0 {
		f.tell(fmt.Sprintf("This bridge took the config its mesh shares: the mesh's values are in force. %d keys of its own are set aside in %s.", n, aside))
	}
}

// SetAside is what this bridge set aside of its own config as it took the mesh's: the
// file it is kept in, and each shared key it held at another value than the mesh
// holds now. None when there is no such file or no mesh.
func (f *ConfigFile) SetAside() (domain.ConfigSetAside, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	store, _, ok := f.meshLocked()
	if !ok {
		return domain.ConfigSetAside{}, false
	}
	dir, name, err := configFolder(f.path)
	if err != nil {
		return domain.ConfigSetAside{}, false
	}
	data, err := dir.ReadFile(name + configSetAsideSuffix)
	_ = dir.Close()
	if err != nil {
		return domain.ConfigSetAside{}, false
	}
	cfg, err := config.Decode(string(data))
	if err != nil {
		return domain.ConfigSetAside{}, false
	}
	own, err := sharedLeaves(cfg)
	if err != nil {
		return domain.ConfigSetAside{}, false
	}
	mesh, _ := store.Config()
	return domain.ConfigSetAside{File: f.path + configSetAsideSuffix, Keys: configChanges(own, mesh)}, true
}

// configFolder is the folder of the config at path, where its set-aside copy is kept,
// and the config's name in it.
func configFolder(path string) (*os.Root, string, error) {
	dir, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, "", fmt.Errorf("daemon: open the config's folder: %w", err)
	}
	return dir, filepath.Base(path), nil
}

// configChanges is each shared key whose value differs between was and now, by path.
func configChanges(was, now map[string][]byte) []domain.ConfigChange {
	var out []domain.ConfigChange
	for _, path := range slices.Sorted(maps.Keys(changedLeaves(was, now))) {
		if config.IsLocal(path) {
			continue
		}
		out = append(out, domain.ConfigChange{Path: path, Was: leafText(was, path), Now: leafText(now, path)})
	}
	return out
}

// leafText is the value at path of leaves, as shown.
func leafText(leaves map[string][]byte, path string) string {
	if v, ok := leaves[path]; ok {
		return config.LeafText(v)
	}
	return config.LeafText(config.Unset)
}

// changedLeaves is each key whose value differs from was in now, at its value in now:
// Unset for one now lacks. A key unset in one and absent in the other is no change.
func changedLeaves(was, now map[string][]byte) map[string][]byte {
	out := map[string][]byte{}
	for path, v := range now {
		if !sameLeaf(was[path], v) {
			out[path] = v
		}
	}
	for path, v := range was {
		if _, ok := now[path]; !ok && !sameLeaf(v, nil) {
			out[path] = config.Unset
		}
	}
	return out
}

// sameLeaf reports whether two values of a key read alike: equal, or both unset.
func sameLeaf(a, b []byte) bool {
	unset := func(v []byte) bool { return v == nil || bytes.Equal(v, config.Unset) }
	return bytes.Equal(a, b) || (unset(a) && unset(b))
}

// sharedLeaves is cfg's shared keys as registers.
func sharedLeaves(cfg config.Config) (map[string][]byte, error) {
	leaves, err := config.Leaves(cfg)
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	maps.DeleteFunc(leaves, func(path string, _ []byte) bool { return config.IsLocal(path) })
	return leaves, nil
}

// handEdits is each shared key of file whose value differs from its value in every
// one of renderings: the keys a hand edit changed, at their value in file.
func handEdits(file config.Config, renderings []string) (map[string][]byte, error) {
	now, err := sharedLeaves(file)
	if err != nil {
		return nil, err
	}
	var edits map[string][]byte
	for i, body := range renderings {
		r, err := config.Decode(body)
		if err != nil {
			return nil, fmt.Errorf("daemon: a rendering of the config: %w", err)
		}
		was, err := sharedLeaves(r)
		if err != nil {
			return nil, err
		}
		changed := changedLeaves(was, now)
		if i == 0 {
			edits = changed
			continue
		}
		maps.DeleteFunc(edits, func(path string, _ []byte) bool { _, ok := changed[path]; return !ok })
	}
	return edits, nil
}

// writeSharedLocked is a client's write of cfg as the mesh takes it: each shared key whose
// value differs from the file's (data) written to the mesh as one op, then the file
// rendered. False when the config is not shared: the caller writes the file alone.
func (f *ConfigFile) writeSharedLocked(ctx context.Context, check CheckConfig, data []byte, cfg config.Config) (bool, error) {
	store, signer, ok := f.meshLocked()
	if !ok {
		return false, nil
	}
	renderings, joined, err := store.Rendered(ctx)
	if err != nil {
		return false, fmt.Errorf("daemon: %w", err)
	}
	if joined || len(renderings) == 0 {
		return false, nil // not shared yet: the file alone, shared when it is settled
	}
	file, err := config.Decode(string(data))
	if err != nil {
		return false, fmt.Errorf("daemon: %w", err)
	}
	was, err := sharedLeaves(file)
	if err != nil {
		return false, err
	}
	now, err := sharedLeaves(cfg)
	if err != nil {
		return false, err
	}
	if err := store.WriteConfig(ctx, signer, changedLeaves(was, now)); err != nil {
		return false, fmt.Errorf("daemon: share the config with the mesh: %w", err)
	}
	if _, err := f.renderLocked(ctx, check, store, data, cfg, false); err != nil {
		return false, err
	}
	return true, nil
}

// ConfigConflicts is each shared key bridges wrote concurrently with different values,
// each value as shown; none without a mesh.
func (f *ConfigFile) ConfigConflicts() []domain.ConfigConflict {
	f.mu.Lock()
	defer f.mu.Unlock()
	store, _, ok := f.meshLocked()
	if !ok {
		return nil
	}
	_, conflicts := store.Config()
	for i := range conflicts {
		for j := range conflicts[i].Values {
			v := &conflicts[i].Values[j]
			v.Value = config.LeafText([]byte(v.Value))
		}
	}
	return conflicts
}

// Why keeping a value is refused.
var (
	errNoConflict = errors.New("that key of the config is in no conflict")
	errNoValue    = errors.New("that key's conflict has no such value")
)

// KeepConfigValue ends the conflict of the shared key at path by keeping its which-th
// value (as ConfigConflicts orders them): a write that has seen every value, so every
// bridge ends it. Then the file is settled and applied.
func (f *ConfigFile) KeepConfigValue(ctx context.Context, path string, which int, check CheckConfig, apply Reload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	store, signer, ok := f.meshLocked()
	if !ok {
		return errNoConflict
	}
	_, conflicts := store.Config()
	i := slices.IndexFunc(conflicts, func(c domain.ConfigConflict) bool { return c.Path == path })
	switch {
	case i < 0:
		return errNoConflict
	case which < 0 || which >= len(conflicts[i].Values):
		return errNoValue
	}
	if err := store.WriteConfig(ctx, signer, map[string][]byte{path: []byte(conflicts[i].Values[which].Value)}); err != nil {
		return fmt.Errorf("daemon: keep a value of the config: %w", err)
	}
	wrote, err := f.settleLocked(ctx, check)
	if err == nil && wrote && apply != nil {
		err = apply(ctx)
	}
	return err
}

// logger is the log f says things to.
func (f *ConfigFile) logger() *slog.Logger {
	if f.log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return f.log
}

// tell raises a popup saying body, when f has one to raise.
func (f *ConfigFile) tell(body string) {
	if f.say != nil {
		f.say(body)
	}
}

// configRecheck is how often the config is settled with nothing to wake it.
const configRecheck = time.Minute

// followConfig settles the config after every change to the mesh store, and every
// configRecheck, until ctx ends; each rendering written is applied and told to every
// client.
func (s *server) followConfig(ctx context.Context) {
	for {
		var changed <-chan struct{}
		if store, _, _, _, err := s.Paired.meshParts(); err == nil {
			changed = store.Changed() // before reading, so nothing committed after is missed
		}
		wrote, err := s.Config.Settle(ctx, s.Daemon.CheckConfig, s.Reload, false)
		switch {
		case err != nil:
			s.Log.Warn("bring the config and the mesh's together", "err", err)
		case wrote:
			if snap, rerr := s.Config.Read(); rerr == nil {
				s.Streams.Configured(snap)
			}
		}
		timer := time.NewTimer(configRecheck)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-changed:
		case <-timer.C:
		}
		timer.Stop()
	}
}
