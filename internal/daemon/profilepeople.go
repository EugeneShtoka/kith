package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/phonebook"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// ProfilePeople keeps whom each [[profile]] hides current: its people.exclude IDs, and
// the people whose phonebook card meets one of its field terms. It reads the phonebook
// folder only while a profile names a field, and reads it again whenever a card in it
// changes, so a card edited by any program hides or shows its person at once.
type ProfilePeople struct {
	// apply hands the resolved exclusions on (the cache's UseProfileHidden).
	apply func(context.Context, map[string]domain.ExcludedPeople) error
	log   *slog.Logger

	// resolving holds resolves to one at a time, so the last to start lands last.
	resolving sync.Mutex

	mu       sync.Mutex
	profiles []domain.Profile
	dir      string
	// cards is the last folder read that worked; a failed read keeps it, so a passing
	// error never shows the people a card hides.
	cards phonebook.Index
	// watching is the folder watched and how to stop it; "" when none is.
	watching string
	stop     context.CancelFunc
}

// NewProfilePeople hands each profile's exclusions to apply as they change.
func NewProfilePeople(apply func(context.Context, map[string]domain.ExcludedPeople) error, log *slog.Logger) *ProfilePeople {
	return &ProfilePeople{apply: apply, log: log}
}

// Use takes the profiles and the phonebook folder (at start, and on every reload),
// resolves and applies them, and watches the folder while a profile names a card field.
// ctx bounds the watch.
func (p *ProfilePeople) Use(ctx context.Context, profiles []domain.Profile, dir string) error {
	p.mu.Lock()
	p.profiles, p.dir = slices.Clone(profiles), dir
	if dir != p.watching || !namesFields(profiles) {
		p.unwatch()
	}
	if namesFields(profiles) && p.watching == "" {
		p.watch(ctx, dir)
	}
	p.mu.Unlock()
	return p.resolve(ctx)
}

// Stop ends the folder watch.
func (p *ProfilePeople) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unwatch()
}

// namesFields reports whether any profile hides people by a card field.
func namesFields(profiles []domain.Profile) bool {
	return slices.ContainsFunc(profiles, func(p domain.Profile) bool { return len(p.People.Fields) > 0 })
}

// resolve reads the folder when a field needs it and applies every profile's exclusions.
func (p *ProfilePeople) resolve(ctx context.Context) error {
	p.resolving.Lock()
	defer p.resolving.Unlock()
	p.mu.Lock()
	profiles, dir := p.profiles, p.dir
	p.mu.Unlock()
	if namesFields(profiles) {
		// No folder is no cards; a folder that cannot be read keeps the cards before.
		if cards, err := phonebook.New(dir).Cards(); err != nil {
			p.log.Warn("read the phonebook for the profiles' people; keeping the cards read before", "dir", dir, "err", err)
		} else {
			p.mu.Lock()
			p.cards = phonebook.NewIndex(cards)
			p.mu.Unlock()
		}
	}
	p.mu.Lock()
	cards := p.cards
	p.mu.Unlock()
	excluded := make(map[string]domain.ExcludedPeople, len(profiles))
	for _, profile := range profiles {
		if !profile.People.Empty() {
			excluded[profile.Name] = setup.ProfileExcluded(profile.People, cards)
		}
	}
	return p.apply(ctx, excluded)
}

// watch follows dir, and its parent for dir being made or removed, resolving after
// each change to a card. Call with mu held.
func (p *ProfilePeople) watch(ctx context.Context, dir string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		p.log.Warn("watch the phonebook: cards edited from now on are read at the next reload", "err", err)
		return
	}
	dir = filepath.Clean(dir)
	if _, err := watchDown(watcher, dir); err != nil {
		p.log.Warn("watch the phonebook: cards edited from now on are read at the next reload", "dir", dir, "err", err)
		_ = watcher.Close()
		return
	}
	ctx, stop := context.WithCancel(ctx)
	p.watching, p.stop = dir, stop
	go p.follow(ctx, watcher, dir)
}

// watchDown adds to watcher the deepest folder on the way to dir that exists now (dir
// itself once it does), so the next folder down, or a card, is heard when made; there
// reports whether dir is watched. A folder made between looking and watching (`mkdir
// -p`) is never heard, so it looks again after each watch until nothing new is there.
func watchDown(watcher *fsnotify.Watcher, dir string) (there bool, err error) {
	watched := ""
	for {
		at, err := deepestOnTheWay(dir)
		if err != nil {
			return false, err
		}
		if at == watched {
			return at == dir, nil
		}
		if err := watcher.Add(at); err != nil {
			return false, fmt.Errorf("daemon: watch %s: %w", at, err)
		}
		watched = at
	}
}

// deepestOnTheWay is the deepest folder on the way to dir that exists now, dir itself
// once it does.
func deepestOnTheWay(dir string) (string, error) {
	at := dir
	for {
		info, statErr := os.Stat(at)
		if statErr == nil && info.IsDir() {
			return at, nil
		}
		parent := filepath.Dir(at)
		if parent == at {
			return "", fmt.Errorf("daemon: no folder on the way to %s: %w", dir, statErr)
		}
		at = parent
	}
}

// heard takes one event of the folder watch and reports whether the phonebook may have
// changed. A folder on the way to the phonebook's made: watch on down, and read the
// phonebook should it be there, cards and all, already. The phonebook's own going:
// watch from the deepest folder left, to hear it come back.
func (p *ProfilePeople) heard(watcher *fsnotify.Watcher, dir string, ev fsnotify.Event) bool {
	name := filepath.Clean(ev.Name)
	made := ev.Op&fsnotify.Create != 0 && (name == dir || strings.HasPrefix(dir, name+string(filepath.Separator)))
	gone := name == dir && ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0
	if made || gone {
		there, err := watchDown(watcher, dir)
		if err != nil {
			p.log.Warn("watch the phonebook folder made just now", "dir", dir, "err", err)
		}
		if there {
			return true
		}
	}
	return name == dir || (filepath.Dir(name) == dir && strings.EqualFold(filepath.Ext(name), ".md"))
}

// unwatch stops the folder watch. Call with mu held.
func (p *ProfilePeople) unwatch() {
	if p.stop != nil {
		p.stop()
	}
	p.watching, p.stop = "", nil
}

// follow owns the watcher until ctx ends: a change to a card, or the folder appearing
// or going, resolves again after the debounce.
func (p *ProfilePeople) follow(ctx context.Context, watcher *fsnotify.Watcher, dir string) {
	defer func() {
		if err := watcher.Close(); err != nil {
			p.log.Warn("close the phonebook watcher", "err", err)
		}
	}()
	// Debounced as the config is: a card's save is a burst of events too.
	timer := time.NewTimer(configReloadDebounce)
	if !timer.Stop() {
		<-timer.C
	}
	armed := false
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if p.heard(watcher, dir, ev) {
				armed = armTimer(timer, armed)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			p.log.Warn("the phonebook watcher reported an error", "err", err)
		case <-timer.C:
			armed = false
			if err := p.resolve(ctx); err != nil && ctx.Err() == nil {
				p.log.Warn("apply the profiles' people after a card changed", "err", err)
			}
		}
	}
}
