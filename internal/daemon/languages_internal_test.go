package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
)

// What can happen to the owed dictionary offer, one minute's grain at a time.
type languageStep int

const (
	newOnline     languageStep = iota // the account a login set up comes online
	oldOnline                         // an account that was already there comes online again
	historyCached                     // messages sent before then are cached: a backfill's page
	liveCached                        // a message sent now is cached
	minutePasses
	kithdRestarts
	clientTakes // a client makes the owed offer
	languageSteps
)

func (s languageStep) String() string {
	return [...]string{"new online", "old online", "history", "live", "a minute", "restart", "take"}[s]
}

// owedModel is what should be owed after each step: the offer is owed once the new
// account has been online in one run for historySettle with no history arriving.
type owedModel struct {
	added, owed, online bool
	since, last         int // minutes; -1 while the new account is not online this run
	now                 int
}

func (m *owedModel) step(s languageStep) (owes bool) {
	switch s {
	case newOnline:
		if m.added && !m.online {
			m.online = true
			if m.since < 0 {
				m.since = m.now
			}
		}
	case historyCached:
		if m.since >= 0 {
			m.last = m.now
		}
	case minutePasses:
		m.now++
		if m.since >= 0 && m.now >= max(m.since, m.last)+int(historySettle/time.Minute) {
			m.owed, m.added, m.online, m.since, m.last = true, false, false, -1, -1
			return true
		}
	case kithdRestarts:
		m.online, m.since, m.last = false, -1, -1
	case clientTakes:
		m.owed = false
	case oldOnline, liveCached, languageSteps:
	}
	return false
}

// Over every sequence of four steps from each start (no account set up; one just set
// up; its offer owed already; kithd restarted while waiting for its history): the offer
// is owed exactly when the model says, the clients are told exactly then, and a restart
// keeps what was owed and which account is new.
func TestTheOfferIsOwedOnceANewAccountsHistoryHasArrived(t *testing.T) {
	t.Parallel()

	starts := []struct {
		added bool
		steps []languageStep
	}{
		{added: false},
		{added: true},
		{added: true, steps: []languageStep{newOnline, minutePasses, minutePasses}},
		{added: true, steps: []languageStep{newOnline, historyCached, minutePasses, kithdRestarts}},
	}
	var walk func(added bool, steps []languageStep, depth int)
	walk = func(added bool, steps []languageStep, depth int) {
		runOwedSteps(t, added, steps)
		if depth == 0 {
			return
		}
		for s := range languageSteps {
			walk(added, append(slices.Clip(steps), s), depth-1)
		}
	}
	for _, start := range starts {
		walk(start.added, start.steps, 4)
	}
}

func runOwedSteps(t *testing.T, added bool, steps []languageStep) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "languages.toml")
		var told atomic.Int32
		var l *Languages
		var stop context.CancelFunc
		start := func() {
			l = NewLanguages(path, func() { told.Add(1) }, logging.Discard())
			var ctx context.Context
			ctx, stop = context.WithCancel(t.Context())
			go l.Run(ctx)
			synctest.Wait()
		}
		start()
		defer func() { stop(); synctest.Wait() }()

		m := owedModel{added: added, since: -1, last: -1}
		if added {
			l.Added(domain.ProtocolWhatsApp, "new")
		}
		want := int32(0)
		for i, s := range steps {
			switch s {
			case newOnline:
				l.Status(domain.AccountStatus{Network: domain.ProtocolWhatsApp, Account: "new", Phase: domain.AccountOnline})
			case oldOnline:
				l.Status(domain.AccountStatus{Network: domain.ProtocolWhatsApp, Account: "old", Phase: domain.AccountOnline})
			case historyCached:
				l.Arrived("room", time.Now().Add(-time.Hour))
			case liveCached:
				l.Arrived("room", time.Now())
			case minutePasses:
				time.Sleep(time.Minute)
			case kithdRestarts:
				stop()
				synctest.Wait()
				start()
			case clientTakes:
				l.take()
			case languageSteps:
			}
			synctest.Wait()
			if m.step(s) {
				want++
			}
			l.mu.Lock()
			owed := l.file.Owed
			l.mu.Unlock()
			if owed != m.owed || told.Load() != want {
				t.Fatalf("added %v, after %v: owed %v, clients told %d times; want %v, %d",
					added, steps[:i+1], owed, told.Load(), m.owed, want)
			}
		}
	})
}

// detections is a backend that counts the detections it runs, failing them with fail.
type detections struct {
	apitest.Nop
	runs int
	fail error
}

func (d *detections) DetectLanguages(context.Context) (domain.SpellSuggestion, error) {
	d.runs++
	return domain.SpellSuggestion{Candidates: []domain.LanguageCandidate{{Tag: "he_IL"}}}, d.fail
}

// Asked for what is owed, kithd answers once and only when owed, reading the corpus only
// then; a detection that fails stays owed. Asked outright, it always detects.
func TestDetectLanguagesAnswersWhatIsOwedOnce(t *testing.T) {
	t.Parallel()

	backend := &detections{}
	l := NewLanguages(filepath.Join(t.TempDir(), "languages.toml"), func() {}, logging.Discard())
	s := &server{Daemon: &Daemon{Backend: backend, Languages: l}}
	ask := func(owed bool) []string {
		t.Helper()
		got, err := s.DetectLanguages(t.Context(), connect.NewRequest(&v1.DetectLanguagesRequest{Owed: owed}))
		if err != nil {
			return []string{"error"}
		}
		var tags []string
		for _, c := range got.Msg.GetCandidates() {
			tags = append(tags, c.GetTag())
		}
		return tags
	}

	for i, c := range []struct {
		owe, fail, owed bool
		want            string
		runs            int
		stillOwed       bool
	}{
		{owed: true, want: "", runs: 0},
		{owe: true, owed: true, want: "he_IL", runs: 1},
		{owed: true, want: "", runs: 1}, // taken by the answer before
		{owe: true, fail: true, owed: true, want: "error", runs: 2, stillOwed: true},
		{owed: true, want: "he_IL", runs: 3},
		{owed: false, want: "he_IL", runs: 4},
	} {
		if c.owe {
			l.restore()
		}
		backend.fail = nil
		if c.fail {
			backend.fail = errors.New("the cache is closed")
		}
		got := strings.Join(ask(c.owed), ",")
		l.mu.Lock()
		owed := l.file.Owed
		l.mu.Unlock()
		if got != c.want || backend.runs != c.runs || owed != c.stillOwed {
			t.Errorf("ask %d: answered %q after %d detections, owed %v; want %q, %d, %v",
				i, got, backend.runs, owed, c.want, c.runs, c.stillOwed)
		}
	}
}

// settingUp is a network whose login of a new account names it "home", then ends as end
// says.
type settingUp struct{ end string }

func (n settingUp) LoginNetwork() api.LoginNetwork { return api.LoginNetwork{Network: "fake"} }

func (n settingUp) Login(_ context.Context, account string, talk api.LoginTalk) (api.LoginEnd, error) {
	if account == "" {
		talk.Named("home")
	}
	switch n.end {
	case "restart":
		return api.LoginEnd{Done: "logged in", Restart: true}, nil
	case "failed":
		return api.LoginEnd{}, errors.New("wrong code")
	}
	return api.LoginEnd{Done: "logged in"}, nil
}

// Only a login that set an account up and succeeded makes it new: one signing an
// account in again, or one that failed, does not.
func TestOnlyALoginThatSetsAnAccountUpAddsIt(t *testing.T) {
	t.Parallel()

	for _, account := range []string{"", "home"} {
		for _, end := range []string{"done", "restart", "failed"} {
			var added []string
			l := NewLogins(t.Context(), func() []api.LoginLeader { return []api.LoginLeader{settingUp{end}} })
			l.OnAdded(func(network, account string) { added = append(added, network+"/"+account) })
			if _, err := l.BeginLogin(t.Context(), "fake", account); err != nil && end != "failed" {
				t.Fatalf("login of %q ending %s: %v", account, end, err)
			}
			want := ""
			if account == "" && end != "failed" {
				want = "fake/home"
			}
			if got := strings.Join(added, ","); got != want {
				t.Errorf("login of %q ending %s added %q, want %q", account, end, got, want)
			}
		}
	}
}
