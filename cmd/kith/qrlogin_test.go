package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
)

// scanning is a daemon whose login shows rotating QR codes until it ends; taking the
// other way shows a code instead.
type scanning struct {
	apitest.Nop

	mu       sync.Mutex
	steps    []api.LoginStep
	insteads int
}

func (s *scanning) next() (api.LoginStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	step := s.steps[0]
	s.steps = s.steps[1:]
	step.Login, step.Account = "L1", "home"
	return step, nil
}

func (s *scanning) BeginLogin(context.Context, string, string) (api.LoginStep, error) {
	return s.next()
}
func (s *scanning) AnswerLogin(context.Context, string, map[string]string) (api.LoginStep, error) {
	return s.next()
}
func (*scanning) CancelLogin(context.Context, string) error { return nil }

func (*scanning) LoginNetworks(context.Context) ([]api.LoginNetwork, error) { return nil, nil }

func (*scanning) GetConfig(context.Context) (config.Snapshot, error) { return config.Snapshot{}, nil }
func (*scanning) UpdateConfig(context.Context, string, config.Config) (string, error) {
	return "", nil
}

func (s *scanning) InsteadLogin(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.insteads++
	s.steps = []api.LoginStep{{Code: "ABCD-1234", Note: "type it on the phone"}, {Done: "linked"}}
	return nil
}

// loginOutput runs a login over d and is what it printed.
func loginOutput(t *testing.T, d *scanning, byCode bool) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	l := &cliLogin{backend: d, in: bufio.NewReader(strings.NewReader("")), out: w, byCode: byCode}
	ran := make(chan error, 1)
	go func() { ran <- l.run(context.Background(), "whatsapp", "home"); _ = w.Close() }()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-ran; err != nil {
		t.Fatalf("login = %v", err)
	}
	return string(out)
}

const offer = "link with a code typed on the phone instead"

// Each QR code is drawn with where to scan it and how to take the code instead; off a
// terminal each follows the last instead of being drawn over it.
func TestEachQRCodeIsDrawnWithTheWayOut(t *testing.T) {
	t.Parallel()
	d := &scanning{steps: []api.LoginStep{
		{QR: "2@first", Note: "scan it", Instead: offer},
		{QR: "2@second", Note: "scan it again", Instead: offer},
		{Done: "linked"},
	}}
	out := loginOutput(t, d, false)
	if strings.Count(out, "█") == 0 || !strings.Contains(out, "scan it") || !strings.Contains(out, "scan it again") {
		t.Fatalf("printed %q, want both codes with their notes", out)
	}
	if hint := "`kith login --code whatsapp home` will " + offer; strings.Count(out, hint) != 2 {
		t.Errorf("printed %q, want %q under each code", out, hint)
	}
	if strings.Contains(out, "\x1b[J") || d.insteads != 0 {
		t.Errorf("drawn over a pipe, or the code taken unasked (%d)", d.insteads)
	}
}

// --code takes the code a QR step offers instead, drawing no QR code.
func TestTheCodeFlagTakesTheOtherWay(t *testing.T) {
	t.Parallel()
	d := &scanning{steps: []api.LoginStep{{QR: "2@first", Note: "scan it", Instead: offer}}}
	out := loginOutput(t, d, true)
	if d.insteads != 1 || strings.Contains(out, "█") {
		t.Errorf("took the code %d times, drew %q; want it taken once, no QR code drawn", d.insteads, out)
	}
	if len(d.steps) != 0 {
		t.Errorf("%d steps never reached: the code shown after was not waited past", len(d.steps))
	}
}

// A QR step that offers nothing else is scanned even with --code.
func TestTheCodeFlagLeavesAQRCodeWithNoOtherWay(t *testing.T) {
	t.Parallel()
	d := &scanning{steps: []api.LoginStep{{QR: "sgnl://linkdevice?x", Note: "scan it"}, {Done: "linked"}}}
	out := loginOutput(t, d, true)
	if d.insteads != 0 || !strings.Contains(out, "█") || strings.Contains(out, "Can't scan") {
		t.Errorf("took the code %d times, printed %q; want the QR code alone", d.insteads, out)
	}
}
