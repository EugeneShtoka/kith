package matrix

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
)

// Each account is a space in the rail holding its rooms, as every network's accounts
// are, so two accounts' rooms are told apart; leaving it signs the account out.

// errNoSuchAccountSpace is a space that is no Matrix account's.
var errNoSuchAccountSpace = fmt.Errorf("%w: no Matrix account here is that space", api.ErrNotOnNetwork)

// accountSpace is the runner whose account space id is.
func (m *Adapter) accountSpace(id domain.SpaceID) (*runner, bool) {
	for _, r := range m.runnersNow() {
		if r.ids.accountSpace() == id {
			return r, true
		}
	}
	return nil, false
}

// withAccountSpaces is spaces and a space per logged-in account with cached rooms,
// holding all of them. Derived on read, so a new room appears in it at once.
func (m *Adapter) withAccountSpaces(ctx context.Context, spaces []domain.Space) ([]domain.Space, error) {
	rooms, err := m.Rooms(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range m.live() {
		var children []domain.RoomID
		for i := range rooms {
			if r.ids.owner().Owns(rooms[i].ID) {
				children = append(children, rooms[i].ID)
			}
		}
		if len(children) == 0 {
			continue
		}
		spaces = append(spaces, domain.Space{
			ID: r.ids.accountSpace(), Name: "Matrix " + r.account().Name, Children: children,
			// Home of every room no space claims; left by signing out.
			Bridge: domain.ProtocolMatrix, Original: true, Leaving: domain.LeftBySigningOut,
		})
	}
	return spaces, nil
}

// SignOut logs the account owning space out: the homeserver ends its device, and its
// session and crypto store go (the store is moved aside, as no other device can use
// it). The account stays configured; logging in makes a new device. forget also drops
// its rooms from the cache.
func (m *Adapter) SignOut(ctx context.Context, space domain.SpaceID, forget bool) error {
	r, ok := m.accountSpace(space)
	if !ok {
		return errNoSuchAccountSpace
	}
	acc := r.account()
	if !r.loggedIn.Load() {
		return fmt.Errorf("matrix: %s is not logged in, and only its session can end its device: %w", acc.Name, api.ErrNetworkOff)
	}
	if _, err := r.client.Logout(ctx); err != nil {
		return fmt.Errorf("matrix: sign %s out: %w", acc.Name, err)
	}
	if err := session.Forget(acc.Crypto.Keys, acc.AllowTokenFile); err != nil {
		return fmt.Errorf("matrix: %s is signed out, but its session stays saved: %w", acc.Name, err)
	}
	if err := m.renew(r); err != nil {
		return err
	}
	if forget && m.cache != nil {
		if err := m.cache.ForgetOwned(ctx, r.ids.owner()); err != nil {
			return fmt.Errorf("matrix: forget %s's rooms: %w", acc.Name, err)
		}
	}
	m.mu.Lock()
	stale := m.onRoomsStale
	m.mu.Unlock()
	if stale != nil {
		stale()
	}
	return nil
}

// renew replaces a signed-out account's runner with a new one waiting for a login,
// its crypto store moved aside first.
func (m *Adapter) renew(old *runner) error {
	m.configs.Lock()
	defer m.configs.Unlock()
	m.mu.Lock()
	i := slices.Index(m.runners, old)
	if i < 0 || m.closing {
		m.mu.Unlock()
		return nil // removed or stopping meanwhile: nothing to renew
	}
	// Out of reach first, so no call meets it stopping.
	m.runners = slices.Delete(m.runners, i, i+1)
	acc, allowFile, log := old.account(), m.allowFile, m.log
	m.mu.Unlock()
	m.retire(old)
	// Still renewed if this fails: the account stays configured, and says why it
	// cannot encrypt once logged in.
	var moved error
	aside := fmt.Sprintf("%s.signed-out-%d", acc.Crypto.Path, time.Now().Unix())
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Rename(acc.Crypto.Path+suffix, aside+suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			moved = errors.Join(moved, err)
		}
	}
	if moved != nil {
		moved = fmt.Errorf("matrix: move %s's crypto store aside: %w", acc.Name, moved)
	}
	configured := config.MatrixAccount{Name: acc.Name, Homeserver: acc.Homeserver, User: acc.User}
	r := newRunner(m.cache, log, m.place, configured, allowFile, m.hooks)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		r.stop()
		return moved
	}
	m.runners = slices.Insert(m.runners, min(i, len(m.runners)), r)
	m.forward(r)
	m.apply(r)
	m.launch(r)
	return moved
}
