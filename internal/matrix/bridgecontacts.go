package matrix

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sync"
	"time"

	"maunium.net/go/mautrix"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A bridge knows the address book of each account logged into it, and a mautrix
// bridge lists it on its provisioning API ([bridge_contacts]): kith reads those lists
// with the person's own Matrix login and gives the phone book each contact's name, as
// saved, so a person one account's phone knows is named on every other account and
// network. The lists are read when Matrix connects, every few hours, and when the
// setting changes; a bridge that cannot be read keeps what it gave last. Only a bridge
// on the homeserver's own host is asked, since the request carries the login.

// bridgeContactsEvery is how often the bridges' contact lists are read again.
const bridgeContactsEvery = 6 * time.Hour

// bridgeSourcePrefix begins a bridge login's source in the phone book.
const bridgeSourcePrefix = "bridge:"

// bridgeContacts is the provisioning URLs to read, and a signal that they changed.
type bridgeContacts struct {
	mu      sync.Mutex
	bases   []string
	changed chan struct{}
}

// useBridgeContacts takes the configured bridges, asking for a read when they changed.
func (m *Adapter) useBridgeContacts(bases []string) {
	m.contacts.mu.Lock()
	defer m.contacts.mu.Unlock()
	if slices.Equal(m.contacts.bases, bases) {
		return
	}
	m.contacts.bases = slices.Clone(bases)
	select {
	case m.contacts.changed <- struct{}{}:
	default: // a read is already due
	}
}

// followBridgeContacts reads the bridges' contact lists now, every bridgeContactsEvery
// and on each change of the setting, until ctx ends. It blocks.
func (m *Adapter) followBridgeContacts(ctx context.Context) {
	for {
		m.contacts.mu.Lock()
		bases := slices.Clone(m.contacts.bases)
		m.contacts.mu.Unlock()
		m.readBridgeContacts(ctx, bases)
		select {
		case <-ctx.Done():
			return
		case <-time.After(bridgeContactsEvery):
		case <-m.contacts.changed:
		}
	}
}

// readBridgeContacts reads every bridge's lists and drops what a bridge no longer
// configured gave; the room list is refreshed when anything was read, which carries
// the phone book to the clients.
func (b *InProc) readBridgeContacts(ctx context.Context, bases []string) {
	if b.cache == nil {
		return
	}
	var kept []string
	read := false
	for _, base := range bases {
		sources, err := b.readBridge(ctx, base)
		if err != nil {
			b.warnIf(ctx, err, "read a bridge's contacts", "bridge", base)
			// A bridge that could not be read keeps what it gave last.
			had, herr := b.cache.NumberNameSources(ctx, bridgeSourcePrefix+base+"#")
			b.warnIf(ctx, herr, "read which logins a bridge named numbers for", "bridge", base)
			kept = append(kept, had...)
			continue
		}
		kept = append(kept, sources...)
		read = true
	}
	if err := b.cache.ForgetNumberNames(ctx, bridgeSourcePrefix, kept); err != nil {
		b.warnIf(ctx, err, "forget the contacts of bridges no longer read")
	}
	if read && b.onRoomsStale != nil {
		b.onRoomsStale()
	}
}

// provisioningLogins is a bridge's answer to whoami: this person's logins there.
type provisioningLogins struct {
	Logins []struct {
		ID    string `json:"id"`
		State struct {
			StateEvent string `json:"state_event"`
		} `json:"state"`
	} `json:"logins"`
}

// provisioningContacts is a bridge's contact list for one login.
type provisioningContacts struct {
	Contacts []struct {
		Name        string   `json:"name"`
		Identifiers []string `json:"identifiers"`
	} `json:"contacts"`
}

// telNumber is a contact identifier that is a phone number: "tel:+<digits>".
var telNumber = regexp.MustCompile(`^tel:\+?(\d{7,15})$`)

// readBridge reads the contact list of each of this person's connected logins on the
// bridge at base into the phone book, one source per login, and returns the sources
// written. A login not connected keeps what it gave last.
func (b *InProc) readBridge(ctx context.Context, base string) ([]string, error) {
	if !b.onHomeserver(base) {
		return nil, fmt.Errorf("%s is not on the homeserver's host: kith sends it your login, so it asks only your own server", base)
	}
	var who provisioningLogins
	if err := b.provisioning(ctx, base, "/v3/whoami", nil, &who); err != nil {
		return nil, err
	}
	var written []string
	for _, login := range who.Logins {
		source := bridgeSourcePrefix + base + "#" + login.ID
		// A login not connected, or whose list cannot be read, keeps what it gave last.
		written = append(written, source)
		if login.State.StateEvent != "CONNECTED" {
			continue
		}
		var list provisioningContacts
		if err := b.provisioning(ctx, base, "/v3/contacts", url.Values{"login_id": {login.ID}}, &list); err != nil {
			b.warnIf(ctx, err, "read a login's contacts", "bridge", base)
			continue
		}
		var names []domain.NumberName
		for _, c := range list.Contacts {
			if _, isNumber := domain.PhoneIn(c.Name); c.Name == "" || isNumber {
				continue
			}
			for _, id := range c.Identifiers {
				if m := telNumber.FindStringSubmatch(id); m != nil {
					names = append(names, domain.NumberName{Phone: m[1], Name: c.Name, Rank: domain.RankSaved})
				}
			}
		}
		if err := b.cache.SetNumberNames(ctx, source, names); err != nil {
			b.warnIf(ctx, err, "keep a login's contacts", "bridge", base)
		}
	}
	return written, nil
}

// onHomeserver reports whether base is on the homeserver's own host.
func (b *InProc) onHomeserver(base string) bool {
	u, err := url.Parse(base)
	return err == nil && b.client != nil && b.client.HomeserverURL != nil && u.Host == b.client.HomeserverURL.Host
}

// provisioning GETs path under a bridge's provisioning base, as this person.
func (b *InProc) provisioning(ctx context.Context, base, path string, query url.Values, out any) error {
	if query == nil {
		query = url.Values{}
	}
	query.Set("user_id", b.client.UserID.String())
	_, err := b.client.MakeFullRequest(ctx, mautrix.FullRequest{
		Method: http.MethodGet, URL: base + path + "?" + query.Encode(), ResponseJSON: out, MaxAttempts: 1,
	})
	if err != nil {
		return fmt.Errorf("bridge %s: %w", base+path, err)
	}
	return nil
}
