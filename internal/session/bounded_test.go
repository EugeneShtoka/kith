package session

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/keystore"
)

// hangingBusEnv names the private bus TestAKeyringThatNeverAnswersIsNotWaitedOn runs
// its child on.
const hangingBusEnv = "KITH_TEST_HANGING_KEYRING"

// On a session bus where nothing owns the Secret Service and starting it never
// finishes (the rehearsal's hang), reading a key returns ErrKeyringNotAnswering within
// keystore.Wait, again on the next try without asking again, and no key is made. The
// reads run in a child: the other tests swap go-keyring's real keyring for its mock.
func TestAKeyringThatNeverAnswersIsNotWaitedOn(t *testing.T) {
	if os.Getenv(hangingBusEnv) != "" {
		hangingChild(t)
		return
	}
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("no dbus-daemon to run a private bus on")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep for a Secret Service that never starts")
	}
	dir := t.TempDir()
	services := filepath.Join(dir, "services")
	if mkErr := os.Mkdir(services, 0o700); mkErr != nil {
		t.Fatal(mkErr)
	}
	// Starting it runs a program that never claims the name: the bus waits on it.
	if svcErr := os.WriteFile(filepath.Join(services, "secrets.service"),
		[]byte("[D-BUS Service]\nName=org.freedesktop.secrets\nExec="+sleep+" 45\n"), 0o600); svcErr != nil {
		t.Fatal(svcErr)
	}
	conf := filepath.Join(dir, "bus.conf")
	if confErr := os.WriteFile(conf, []byte(`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:path=`+filepath.Join(dir, "bus")+`</listen>
  <servicedir>`+services+`</servicedir>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`), 0o600); confErr != nil {
		t.Fatal(confErr)
	}
	busCtx, stopBus := context.WithCancel(context.Background())
	bus := exec.CommandContext(busCtx, daemon, "--config-file="+conf, "--nofork", "--print-address=1") // #nosec G204 -- the test's own bus
	out, err := bus.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if startErr := bus.Start(); startErr != nil {
		stopBus()
		t.Fatal(startErr)
	}
	t.Cleanup(func() { stopBus(); _ = bus.Wait() })
	address, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("the bus's address: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*keystore.Wait+5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAKeyringThatNeverAnswersIsNotWaitedOn$", "-test.v") // #nosec G204 -- this test's own binary
	child.Env = append(os.Environ(), hangingBusEnv+"=1", "DBUS_SESSION_BUS_ADDRESS="+strings.TrimSpace(address))
	report, err := child.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("reading a key from a keyring that never answers held the reader past %s:\n%s", 3*keystore.Wait+5*time.Second, report)
	}
	if err != nil {
		t.Fatalf("the child: %v\n%s", err, report)
	}
}

// hangingChild reads keys on the hanging bus it was started on.
func hangingChild(t *testing.T) {
	t.Helper()
	read := func() error {
		t.Helper()
		start := time.Now()
		key, err := LoadOrCreateStoreKey("kith-test-hanging", "test", "cache")
		if took := time.Since(start); took > keystore.Wait+2*time.Second {
			t.Errorf("a read took %s; want at most about %s", took, keystore.Wait)
		}
		if key != nil {
			t.Errorf("a key was made with no answer from the keyring")
		}
		return err
	}
	if err := read(); !errors.Is(err, ErrKeyringNotAnswering) {
		t.Fatalf("the first read = %v; want ErrKeyringNotAnswering", err)
	}
	waiting := runtime.NumGoroutine()
	if err := read(); !errors.Is(err, ErrKeyringNotAnswering) {
		t.Fatalf("the second read = %v; want ErrKeyringNotAnswering", err)
	}
	if now := runtime.NumGoroutine(); now > waiting {
		t.Errorf("goroutines went from %d to %d on a second try: the keyring was asked again", waiting, now)
	}
	if _, err := Secret("kith-test-hanging", "api-key"); !errors.Is(err, ErrKeyringNotAnswering) {
		t.Errorf("Secret() = %v; want ErrKeyringNotAnswering", err)
	}
}
