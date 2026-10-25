package db

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Marks made at once on one network all stay: each is its own row's change.
func TestMarksMadeTogetherKeepEachOther(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c := openTemp(t)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			v := domain.PersonVerdict{Person: fmt.Sprintf("whatsapp:1555000%04d@s.whatsapp.net", i), Tag: "Spam", Rule: domain.MoveAlways, At: time.Now()}
			if err := c.MarkPerson(ctx, v); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, err := c.PersonVerdicts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20 {
		t.Errorf("%d marks kept, want all 20", len(got))
	}
}

// An account's set replaces its own verdicts only, as Matrix's account data mirror
// does: another account's are its own mirror, and another network's people's are kept
// here alone.
func TestAnAccountsSetReplacesOnlyItsOwn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c := openTemp(t)
	at := time.Unix(1_700_000_000, 0)
	if err := c.MarkPerson(ctx, domain.PersonVerdict{Person: "telegram:1001", Tag: "Spam", Rule: domain.MoveAlways, At: at}); err != nil {
		t.Fatal(err)
	}
	work := domain.AccountRooms(domain.ProtocolMatrix, "fedcba9876543210")
	if err := c.SetPersonVerdicts(ctx, work, []domain.PersonVerdict{
		{Person: "matrix:@ann:example.org", Tag: "Spam", Rule: domain.MoveAlways, At: at},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPersonVerdicts(ctx, mxRooms, []domain.PersonVerdict{
		{Person: "matrix:@bob:example.org", Tag: "Spam", Released: true, At: at},
		{Person: "telegram:2002", Tag: "Spam", Rule: domain.MoveAlways, At: at}, // not Matrix's to set
	}); err != nil {
		t.Fatal(err)
	}
	got, err := c.PersonVerdicts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var people []string
	for _, v := range got {
		people = append(people, v.Person)
	}
	slices.Sort(people)
	if want := []string{"matrix:@ann:example.org", "matrix:@bob:example.org", "telegram:1001"}; !slices.Equal(people, want) {
		t.Errorf("after Matrix's set: %v, want %v", people, want)
	}
	if rerr := c.RetagPersonVerdicts(ctx, domain.ProtocolTelegram, "Spam", "Junk"); rerr != nil {
		t.Fatal(rerr)
	}
	got, err = c.PersonVerdicts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range got {
		if want := map[string]string{"telegram:1001": "junk", "matrix:@ann:example.org": "spam", "matrix:@bob:example.org": "spam"}[v.Person]; v.Tag != want {
			t.Errorf("after Telegram's retag %s is in %s, want %s", v.Person, v.Tag, want)
		}
	}
	if err := c.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := c.PersonVerdicts(ctx); err != nil || len(got) != 0 {
		t.Errorf("after Clear: %v, %v; want none", got, err)
	}
}
