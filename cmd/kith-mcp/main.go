// Command kith-mcp exposes this account's Matrix history to an AI assistant over the Model
// Context Protocol, as a third client of the kithd daemon: no Matrix code of its own,
// decrypted rooms from the daemon's cache, never a key.
//
// Writing: send_message(room, text) sends only to rooms `[agent.write] send` names; any
// other writable room gets a draft in its composer. The model cannot choose. A per-room
// cooldown and a ledger (internal/agent; `kith --agent-log`) ride with it.
//
// Scope: `[agent.read]` is least privilege (empty rooms shares nothing); `[agent.write]`
// reads empty as "every readable room". Each narrows the one before: send ⊆ write ⊆ read.
//
//	                        [agent.read]             [agent.write]
//	rooms non-empty         those, minus except      only those
//	rooms empty, except set nothing                  every readable room but those
//	both empty              nothing                  every readable room
//	encrypted = false       …never an encrypted one  …never an encrypted one
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/agent"
	"github.com/EugeneShtoka/kith/internal/buildinfo"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/setup"
)

func main() {
	showVersion := flag.Bool("version", false, "print version information and exit")
	configPath := flag.String("config", "", "path to config file (default: XDG config dir)")
	profile := flag.String("profile", "", "which [[profile]] account to serve (default: the first one)")
	logLevel := flag.String("log-level", "", "how much to log to stderr ("+logging.Levels+"); overrides $"+logging.EnvLevel+" and `[log] level`")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.String("kith-mcp"))
		return
	}
	// stderr, never stdout: stdout is the protocol.
	level := &slog.LevelVar{}
	if parsed, err := logging.Resolve(*logLevel, ""); err == nil {
		level.Set(parsed)
	}
	log := logging.New(os.Stderr, logging.Options{Level: level, NoTime: logging.UnderJournald()})
	slog.SetDefault(log)
	if err := run(log, level, *logLevel, *configPath, *profile); err != nil {
		log.Error("kith-mcp stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, level *slog.LevelVar, flagLevel, configPath, profile string) error {
	cfg, user, err := loadAccount(log, configPath, profile)
	if err != nil {
		return err
	}
	resolved, err := logging.Resolve(flagLevel, cfg.Log.Level)
	if err != nil {
		return fmt.Errorf("log level: %w", err)
	}
	level.Set(resolved)
	socket, err := daemon.SocketPath(user)
	if err != nil {
		return fmt.Errorf("finding the daemon's socket: %w", err)
	}
	cooldown, err := setup.AgentCooldown(cfg.Agent)
	if err != nil {
		return err
	}
	server := &server{
		places:   setup.PlacesOf(cfg.Display),
		backend:  daemon.NewRemote(socket),
		scope:    setup.AgentReadScope(cfg.Agent),
		write:    setup.AgentWriteScope(cfg.Agent),
		user:     user,
		send:     cfg.Agent.Write.Send,
		cooldown: cooldown,
		log:      log,
	}
	// Without a ledger the read tools still work; send_message refuses.
	if path, ledgerErr := agent.DefaultPath(user); ledgerErr == nil {
		if ledger, openErr := agent.Open(path); openErr == nil {
			server.ledger = ledger
		} else {
			log.Error("no agent ledger; send_message will refuse", "err", openErr)
		}
	} else {
		log.Error("no agent ledger; send_message will refuse", "err", ledgerErr)
	}
	// Concurrently, so the handshake does not wait on the cache reads.
	go warnAboutScope(log, server.backend, cfg)
	return server.serve(os.Stdin, os.Stdout)
}

// warnAboutScope says, on stderr, which `[agent.write]` entries `[agent.read]` rules out.
func warnAboutScope(log *slog.Logger, places setup.AgentPlaces, cfg config.Config) {
	ctx, cancel := context.WithTimeout(context.Background(), scopeCheckTimeout)
	defer cancel()
	for _, warning := range setup.AgentWarnings(ctx, places, setup.PlacesOf(cfg.Display), cfg.Agent) {
		log.Warn("agent scope: " + warning)
	}
}

// scopeCheckTimeout bounds the startup warning's three cache reads.
const scopeCheckTimeout = 5 * time.Second

// loadAccount loads and validates the config for the selected profile.
func loadAccount(log *slog.Logger, configPath, profile string) (config.Config, string, error) {
	path := configPath
	if path == "" {
		resolved, err := config.Path()
		if err != nil {
			return config.Config{}, "", fmt.Errorf("finding the config: %w", err)
		}
		path = resolved
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, "", fmt.Errorf("reading %s: %w", path, err)
	}
	if warning := config.ModeWarning(path); warning != "" {
		log.Warn(warning)
	}
	cfg, err = cfg.Profile(profile)
	if err != nil {
		return config.Config{}, "", fmt.Errorf("choosing the profile: %w", err)
	}
	if cfg.User == "" {
		return config.Config{}, "", errors.New("no account in the config — run `kith login` first")
	}
	if err := setup.Validate(cfg); err != nil {
		return config.Config{}, "", fmt.Errorf("reading %s: %w", path, err)
	}
	return cfg, cfg.User, nil
}

// reader is the read half of what this server asks the daemon.
type reader interface {
	Rooms(ctx context.Context) ([]domain.Room, error)
	Spaces(ctx context.Context) ([]domain.Space, error)
	CachedUnread(ctx context.Context) ([]domain.Unread, error)
	CachedTimeline(ctx context.Context, roomID domain.RoomID) ([]domain.Message, error)
	SearchMessages(ctx context.Context, req domain.SearchRequest) ([]domain.SearchHit, error)
	SearchSenders(ctx context.Context, rooms domain.RoomSet, limit int) ([]domain.Member, error)
	RoomsWith(ctx context.Context, userIDs []string, rooms domain.RoomSet, limit int) ([]domain.Room, error)
	MessagesAround(ctx context.Context, roomID domain.RoomID, event domain.EventID, before, after int) ([]domain.Message, error)
	RoomEncryption(ctx context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error)
}

// writer is everything this binary can change. Nothing here can edit or delete.
type writer interface {
	Send(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error
	Schedule(ctx context.Context, msg domain.ScheduledMessage) (string, error)
	Drafts(ctx context.Context) ([]domain.StoredDraft, error)
	ReplaceDraft(ctx context.Context, draft, over domain.StoredDraft) (bool, error)
}

// daemonCalls is both halves: everything this server asks of the daemon and nothing else.
type daemonCalls interface {
	reader
	writer
}

// server holds the one connection and the decisions the model does not get to make.
type server struct {
	backend daemonCalls
	// scope is `[agent.read]`, always asked as least privilege (see readScope).
	scope domain.ModelScope
	// write is `[agent.write]` rooms/except/encrypted, asked only inside scope.
	write domain.ModelScope
	user  string
	// send is `[agent.write] send`: rooms posted to rather than drafted into.
	send []string
	// cooldown is how long a room rests after a message goes out to it.
	cooldown time.Duration
	// ledger records every write and answers the cooldown; nil disables writing.
	ledger *agent.Ledger
	// crypto holds the rooms known to be encrypted (that never reverts); a room not
	// in it is asked again on each tool call.
	mu     sync.Mutex
	crypto map[domain.RoomID]bool
	// client is the assistant's name from initialize, recorded as the author.
	client string
	// places are the room names, space order and pins every scope reads a room with.
	places domain.Places
	// log is stderr; nil (a test) logs nothing.
	log *slog.Logger
}

// logger is s.log, or a discarding logger.
func (s *server) logger() *slog.Logger { return logging.OrDiscard(s.log) }

// callTimeout bounds one tool call against an unresponsive daemon.
const callTimeout = 20 * time.Second

// serve runs the protocol loop: one JSON-RPC object per line in, one per line out.
func (s *server) serve(in io.Reader, out io.Writer) error {
	lines := bufio.NewReaderSize(in, maxLine)
	encoder := json.NewEncoder(out)
	for {
		line, err := readLine(lines)
		if errors.Is(err, errLineTooLong) {
			s.logger().Warn("request line too long; skipped", "limit", maxLine)
			continue
		}
		if len(line) == 0 && err != nil {
			return nil //nolint:nilerr // EOF is the normal end of a stdio session
		}
		var req request
		if jsonErr := json.Unmarshal(line, &req); jsonErr != nil {
			// No id to answer to.
			s.logger().Warn("unreadable request", "err", jsonErr)
			continue
		}
		resp, answer := s.handle(&req)
		if !answer {
			continue
		}
		if encErr := encoder.Encode(resp); encErr != nil {
			return fmt.Errorf("writing a reply: %w", encErr)
		}
		if err != nil {
			return nil //nolint:nilerr // same EOF, after the reply went out
		}
	}
}

// maxLine bounds one request line: it is the reader's buffer, and never grows.
const maxLine = 1 << 20

// errLineTooLong is a request line longer than maxLine, already skipped.
var errLineTooLong = errors.New("request line longer than the limit")

// readLine reads one line, of at most maxLine bytes. A longer one is discarded up to
// its newline and reported as errLineTooLong, so one oversized request can neither
// grow memory nor end the session.
func readLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		// Copied: ReadSlice's bytes are overwritten by the next read.
		return append([]byte(nil), line...), err
	}
	for errors.Is(err, bufio.ErrBufferFull) {
		_, err = r.ReadSlice('\n')
	}
	if err != nil {
		return nil, err //nolint:wrapcheck // io.EOF must reach the caller as it is
	}
	return nil, errLineTooLong
}
