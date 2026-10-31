package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect"

	backendv1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/human"
	"github.com/EugeneShtoka/kith/internal/keystore"
)

// User commands, as the TUI runs them (internal/tui/runcmd.go, script.go): `/zoom` runs
// <commands dir>/zoom, its declared context on stdin and in KITH_* variables, its
// secrets from the keyring, and its output goes where its [[command]] block says. Here
// the window names the command, the room and the message only: the relay looks up
// everything the program is given, so nothing the window says reaches it but those IDs,
// and the window never holds a secret.

// commandWaitDelay bounds the wait, after killing a command, for writers still holding
// its output open (the TUI's).
const commandWaitDelay = 500 * time.Millisecond

// commandOutputLimit caps what a command can put in the composer (the TUI's).
const commandOutputLimit = 8 << 10

// profileHeader is the header kithd reads the profile from; main sets it on the
// window's command calls as on its kithd calls.
const profileHeader = "Kith-Profile"

// commandRuns is the window's command running: one at a time, as the TUI's, so a double
// enter cannot run one twice. stop kills it as the relay leaves with its window, and
// waits until it is gone.
type commandRuns struct {
	mu      sync.Mutex
	running string
	cancel  context.CancelFunc
	stopped bool
	wg      sync.WaitGroup
}

// begin claims the one run for name, under cancel; or says which command still runs.
func (r *commandRuns) begin(name string, cancel context.CancelFunc) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return "", false
	}
	if r.running != "" {
		return r.running, false
	}
	r.running, r.cancel = name, cancel
	r.wg.Add(1)
	return "", true
}

func (r *commandRuns) end() {
	r.mu.Lock()
	r.running, r.cancel = "", nil
	r.mu.Unlock()
	r.wg.Done()
}

// stop kills the command running, if one is, and waits for it: no program outlives the
// relay. None starts after.
func (r *commandRuns) stop() {
	r.mu.Lock()
	r.stopped = true
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

// Commands lists the commands directory's executables, saying of each whether it runs
// in the room (its `in` places), as the TUI's `/` list does.
func (d device) Commands(ctx context.Context, req *connect.Request[devicev1.CommandsRequest]) (*connect.Response[devicev1.CommandsResponse], error) {
	kithd, err := d.kithdFor(req.Header())
	if err != nil {
		return nil, err
	}
	room, err := d.roomOf(ctx, kithd, req.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	dev, err := loadDevice(d.configPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	dir, err := d.commandsDir(ctx)
	if err != nil {
		return nil, err
	}
	var out []*devicev1.Command
	for _, name := range scanCommands(dir) {
		block, _ := dev.CommandFor(name)
		out = append(out, &devicev1.Command{Name: name, Applies: appliesIn(block, room), Places: block.In})
	}
	return connect.NewResponse(&devicev1.CommandsResponse{Commands: out}), nil
}

// RunCommand runs a command of the directory in a room, by name, once every check
// passed: an executable file of that name there, a room of the window's profile, one
// its `in` places hold, and no other command running.
func (d device) RunCommand(ctx context.Context, req *connect.Request[devicev1.RunCommandRequest]) (*connect.Response[devicev1.RunCommandResponse], error) {
	name := req.Msg.GetName()
	dir, err := d.commandsDir(ctx)
	if err != nil {
		return nil, err
	}
	path, found := commandPath(dir, name)
	if !found {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("/%s: no command of that name in %s", name, dir))
	}
	kithd, err := d.kithdFor(req.Header())
	if err != nil {
		return nil, err
	}
	room, err := d.roomOf(ctx, kithd, req.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	dev, err := loadDevice(d.configPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	block, _ := dev.CommandFor(name)
	if !appliesIn(block, room) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("/%s is only for %s", name, strings.Join(block.In, ", ")))
	}
	// The program ends with the request (the window reloaded, or closed) and with the relay.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if other, ok := d.runs.begin(name, cancel); !ok {
		if other == "" {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("the window is closing"))
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("/%s is still running", other))
	}
	defer d.runs.end()

	run := commandRun{
		name: name, path: path, room: room, kithd: kithd, block: block,
		timeout: dev.Commands.RunTimeout(), keyring: d.at.keyring,
	}
	answer, err := run.prepare(ctx, req.Msg.GetEventId(), int(req.Msg.GetLink()))
	if err != nil {
		return nil, err
	}
	if answer == nil {
		answer = run.run(ctx)
	}
	return connect.NewResponse(answer), nil
}

// kithdFor is kithd as the window's profile reaches it: the profile main named on the
// request. A request naming none is refused: kithd would answer for the first profile.
func (d device) kithdFor(header http.Header) (profiled, error) {
	profile := header.Get(profileHeader)
	if profile == "" {
		return profiled{}, connect.NewError(connect.CodePermissionDenied, errors.New("a command runs in a profile, and none was named"))
	}
	return profiled{
		client:  backendv1connect.NewBackendServiceClient(d.at.client, d.at.scheme+"://kithd"),
		profile: profile, at: d.at,
	}, nil
}

// profiled is kithd, every request in one profile.
type profiled struct {
	client  backendv1connect.BackendServiceClient
	profile string
	at      kithdAt
}

// request is msg as a request in p's profile.
func request[T any](p profiled, msg *T) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set(profileHeader, p.profile)
	return req
}

// roomOf is the room id as the profile's rail serves it, refused when the profile does
// not hold it (or kithd cannot say).
func (d device) roomOf(ctx context.Context, p profiled, id string) (domain.ServedRoom, error) {
	res, err := p.client.Rail(ctx, request(p, &backendv1.RailRequest{}))
	if err != nil {
		return domain.ServedRoom{}, p.at.failure(err)
	}
	rail := protoconv.ProtoToRail(res.Msg)
	i := slices.IndexFunc(rail.Rooms, func(r domain.ServedRoom) bool { return r.Facts.ID == id && id != "" })
	if i < 0 {
		return domain.ServedRoom{}, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("profile %s holds no room %q", p.profile, id))
	}
	return rail.Rooms[i], nil
}

// appliesIn is whether block's command runs in room: its `in` names it, or it has none.
func appliesIn(block config.Command, room domain.ServedRoom) bool {
	return len(block.In) == 0 || slices.ContainsFunc(block.In, room.Facts.Names)
}

// commandsDir is the TUI's: the config's [commands] dir, else `commands` beside the
// device file.
func (d device) commandsDir(ctx context.Context) (string, error) {
	cfg, err := d.config(ctx)
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(cfg.Commands.Dir)
	if dir == "" {
		return filepath.Join(filepath.Dir(config.DevicePath(d.configPath)), "commands"), nil
	}
	if dir != "~" && !strings.HasPrefix(dir, "~/") {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot expand ~: %w", err))
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(dir, "~"), "/")), nil
}

// commandPath is the file a command name runs, as the TUI's userCommand finds it: one
// path segment (that is the whole sandbox), an executable file and not a directory.
func commandPath(dir, name string) (string, bool) {
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." || strings.ContainsRune(name, 0) {
		return "", false
	}
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return "", false
	}
	return path, true
}

// scanCommands is dir's executables, by commandPath's test, so the list never offers
// what a run would refuse. No directory is no commands.
func scanCommands(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if _, ok := commandPath(dir, entry.Name()); ok {
			names = append(names, entry.Name())
		}
	}
	return names
}

// commandRun is one command being run.
type commandRun struct {
	name, path string
	room       domain.ServedRoom
	kithd      profiled
	block      config.Command
	timeout    time.Duration
	keyring    string
	// What prepare looked up: the program's stdin and its KITH_* variables.
	stdin []byte
	env   []string
}

// prepare looks up the context the command's needs declare, as the TUI's resolveScript
// and scriptJSON do: the message (event, else the room's newest), its link (link, else
// the only one; several and none picked comes back as links to choose from), and the
// room's history. A non-nil answer ends the run there.
func (c *commandRun) prepare(ctx context.Context, event string, link int) (*devicev1.RunCommandResponse, error) {
	needs := make([]domain.Need, 0, len(c.block.Needs))
	for _, entry := range c.block.Needs {
		if need, ok := domain.ParseNeed(entry); ok {
			needs = append(needs, need)
		}
	}
	selves, err := c.kithd.client.Selves(ctx, request(c.kithd, &backendv1.SelvesRequest{}))
	if err != nil {
		return nil, c.kithd.at.failure(err)
	}
	ctxOf := scriptContext{run: c, event: event, link: link, selves: selves.Msg.GetIds()}
	for _, need := range needs {
		if answer, aerr := ctxOf.add(ctx, need); answer != nil || aerr != nil {
			return answer, aerr
		}
	}
	if c.stdin, err = json.Marshal(ctxOf.stdin); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("could not assemble its context: %w", err))
	}
	room := c.room.Facts.ID
	self := ""
	for _, id := range ctxOf.selves {
		if domain.NetworkOf(id) == domain.NetworkOf(room) {
			self = id
			break
		}
	}
	// No KITH_ARG of the window's: what follows the name stays in the window.
	c.env = append(c.env, "KITH_ARG=", "KITH_ROOM_ID="+room, "KITH_ROOM="+c.room.Facts.Name, "KITH_USER="+self)
	return nil, nil
}

// scriptContext is a command's context as prepare gathers it, need by need.
type scriptContext struct {
	run    *commandRun
	event  string
	link   int
	selves []string
	shown  []domain.Message // the room's main timeline, once read
	read   bool
	msg    *domain.Message
	stdin  domain.ScriptContext
}

func (s *scriptContext) mine(sender string) bool { return slices.Contains(s.selves, sender) }

// add looks up one need's context; a non-nil answer ends the run there.
func (s *scriptContext) add(ctx context.Context, need domain.Need) (*devicev1.RunCommandResponse, error) {
	c := s.run
	if !s.read && (need.Kind == domain.NeedHistory || (s.event == "" && need.Kind != domain.NeedNone)) {
		if err := s.readShown(ctx); err != nil {
			return nil, err
		}
	}
	if s.msg == nil && (need.Kind == domain.NeedMessage || need.Kind == domain.NeedURL) {
		found, err := c.message(ctx, domain.RoomID(c.room.Facts.ID), s.event, s.shown)
		if err != nil {
			return nil, err
		}
		if found == nil {
			return &devicev1.RunCommandResponse{Why: "needs a message and there is none here"}, nil
		}
		s.msg = found
	}
	switch need.Kind {
	case domain.NeedMessage:
		row := domain.ScriptRow(*s.msg, s.mine(s.msg.Sender))
		s.stdin.Message = &row
		c.env = append(c.env, "KITH_MESSAGE_ID="+string(s.msg.ID), "KITH_SENDER="+s.msg.Sender, "KITH_SENDER_NAME="+s.msg.SenderName)
	case domain.NeedURL:
		links := domain.MessageLinks(s.msg.Body, hrefs(*s.msg))
		switch {
		case len(links) == 0:
			return &devicev1.RunCommandResponse{Why: "no link in that message"}, nil
		case s.link == 0 && len(links) > 1:
			return &devicev1.RunCommandResponse{Links: links}, nil
		case s.link > len(links):
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("that message has %d links, not %d", len(links), s.link))
		}
		s.stdin.URL = links[max(s.link, 1)-1]
		c.env = append(c.env, "KITH_URL="+s.stdin.URL)
	case domain.NeedHistory:
		last := s.shown[max(0, len(s.shown)-need.Count):]
		s.stdin.History = make([]domain.ScriptMessage, 0, len(last))
		for i := range last {
			s.stdin.History = append(s.stdin.History, domain.ScriptRow(last[i], s.mine(last[i].Sender)))
		}
	case domain.NeedNone:
	}
	return nil, nil
}

// readShown reads the room's main timeline as kithd's cache holds it.
func (s *scriptContext) readShown(ctx context.Context) error {
	c := s.run
	res, err := c.kithd.client.CachedTimeline(ctx, request(c.kithd, &backendv1.CachedTimelineRequest{RoomId: c.room.Facts.ID}))
	if err != nil {
		return c.kithd.at.failure(err)
	}
	all := protoconv.ProtoToMessages(res.Msg.GetMessages())
	for i := range all {
		if all[i].ThreadRoot == "" || all[i].ThreadRoot == all[i].ID {
			s.shown = append(s.shown, all[i])
		}
	}
	s.read = true
	return nil
}

// message is the message the command is run on: event in room, else the newest shown;
// nil when there is none.
func (c *commandRun) message(ctx context.Context, room domain.RoomID, event string, shown []domain.Message) (*domain.Message, error) {
	if event == "" {
		if len(shown) == 0 {
			return nil, nil
		}
		return &shown[len(shown)-1], nil
	}
	res, err := c.kithd.client.FetchEvent(ctx, request(c.kithd, &backendv1.FetchEventRequest{RoomId: string(room), EventId: event}))
	if ce := new(connect.Error); errors.As(err, &ce) && ce.Code() == connect.CodeNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, c.kithd.at.failure(err)
	}
	m := protoconv.ProtoToMessage(res.Msg.GetMessage())
	if m.RoomID != room {
		return nil, nil // kithd answered another room's: it is no message here
	}
	return &m, nil
}

// hrefs are the links msg's formatting carries, as the TUI's messageHrefs reads them.
func hrefs(msg domain.Message) []string {
	if msg.Format.IsZero() || msg.Redacted || msg.Emote || msg.Media != nil || strings.TrimSpace(msg.Format.Text()) == "" {
		return nil
	}
	var out []string
	for _, span := range msg.Format.Spans() {
		if span.Href != "" {
			out = append(out, span.Href)
		}
	}
	return out
}

// run runs the program and routes what it printed, as the TUI's userCommandCmd and
// handleCommandRan do.
func (c *commandRun) run(ctx context.Context) *devicev1.RunCommandResponse {
	out, _ := domain.ParseScriptOutput(c.block.Output) // checked as the device file loaded
	body, why := c.execute(ctx)
	if why != "" {
		return &devicev1.RunCommandResponse{Why: why}
	}
	output := outputName(out)
	truncated := len(body) > commandOutputLimit
	if truncated {
		body = body[:commandOutputLimit]
	}
	if strings.TrimSpace(body) == "" {
		if out != domain.OutputNone {
			why = "printed nothing"
		}
		return &devicev1.RunCommandResponse{Output: output, Truncated: truncated, Why: why}
	}
	// Truncated output is never sent, copied or dropped unreviewed: it goes to be read.
	if truncated && out != domain.OutputPager {
		out = domain.OutputCompose
	}
	switch out {
	case domain.OutputSend:
		// Into the room it was run in, as the TUI's sendCommandOutput sends it.
		draft := protoconv.DraftToProto(domain.Draft{Body: body, TxnID: domain.NewTxnID()})
		if _, err := c.kithd.client.Send(ctx, request(c.kithd, &backendv1.SendRequest{RoomId: c.room.Facts.ID, Draft: draft})); err != nil {
			return &devicev1.RunCommandResponse{Why: "could not send its output: " + err.Error()}
		}
		body = ""
	case domain.OutputNone:
		body = ""
	case domain.OutputCompose, domain.OutputStatus, domain.OutputPager, domain.OutputClipboard:
	}
	return &devicev1.RunCommandResponse{Output: outputName(out), Body: body, Truncated: truncated}
}

// execute runs the program with its context and secrets, under the timeout: what it
// printed, or why there is nothing.
func (c *commandRun) execute(ctx context.Context) (body, why string) {
	secrets, err := c.secrets()
	if err != nil {
		return "", err.Error()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.path) // #nosec G204 -- a file the user made executable in their own commands directory, by name
	cmd.Env = slices.Concat(os.Environ(), c.env, secrets)
	cmd.Stdin = bytes.NewReader(c.stdin)
	cmd.WaitDelay = commandWaitDelay
	stdout, cleanOut, err := captureFile()
	if err != nil {
		return "", err.Error()
	}
	defer cleanOut()
	stderr, cleanErr, err := captureFile()
	if err != nil {
		return "", err.Error()
	}
	defer cleanErr()
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	switch {
	case ctx.Err() != nil && errors.Is(context.Cause(ctx), context.DeadlineExceeded):
		return "", "gave up after " + human.Duration(c.timeout)
	case ctx.Err() != nil:
		return "", "stopped: the window went"
	case runErr != nil:
		return "", commandFailure(runErr, readCapture(stderr))
	}
	return strings.TrimRight(readCapture(stdout), "\n"), ""
}

// secrets are the command's declared secrets as its environment, from this machine's
// keyring, as the TUI's commandSecretEnv reads them: a missing one is an error naming
// it, so the program never runs with an empty variable.
func (c *commandRun) secrets() ([]string, error) {
	env := make([]string, 0, len(c.block.Secrets))
	for _, name := range c.block.Secrets {
		value, err := keystore.Secret(c.keyring, config.SecretRef(c.block.Name, name))
		if err != nil {
			return nil, fmt.Errorf("could not read its secret %s: %w", name, err)
		}
		if value == "" {
			return nil, fmt.Errorf("its secret %s is not set — the TUI's Settings → Advanced → Commands", name)
		}
		env = append(env, config.SecretEnv(name)+"="+value)
	}
	return env, nil
}

// outputName is out as a [[command]] block spells it.
func outputName(out domain.ScriptOutput) string {
	for _, name := range domain.OutputVocabulary() {
		if parsed, _ := domain.ParseScriptOutput(name); parsed == out {
			return name
		}
	}
	return ""
}

// captureFile is a temp file for a command's output, and its cleanup: a file, not a
// pipe, so a forked grandchild holding it open cannot hold the run (the TUI's).
func captureFile() (*os.File, func(), error) {
	file, err := os.CreateTemp("", "kith-cmd-*")
	if err != nil {
		return nil, nil, fmt.Errorf("could not capture the output: %w", err)
	}
	return file, func() {
		_ = file.Close()           // read, or abandoned: nothing to report
		_ = os.Remove(file.Name()) // as above
	}, nil
}

func readCapture(file *os.File) string {
	data, err := os.ReadFile(file.Name())
	if err != nil {
		return ""
	}
	return string(data)
}

// commandFailure is a failed command's first stderr line, else the TUI's hints.
func commandFailure(err error, stderr string) string {
	line, _, _ := strings.Cut(stderr, "\n")
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	if errors.Is(err, syscall.ENOEXEC) {
		return "cannot run it — does the file start with a #! line, like #!/bin/sh?"
	}
	if errors.Is(err, fs.ErrPermission) {
		return "cannot run it — is it executable? chmod +x the file"
	}
	return err.Error()
}
