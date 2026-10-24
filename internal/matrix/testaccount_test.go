package matrix

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/db"
)

// The tests' account is @me:x: testAccount is its AccountKey, and mx the prefix of its
// rooms and events in kith's form.
const (
	me          = "@me:x"
	testAccount = "c1c0b5f34c2bbc30"
	mx          = "matrix:" + testAccount + "/"
)

// testIDs is the seam for the tests' account.
var testIDs = accountIDs(me)

func TestTheTestAccountIsMe(t *testing.T) {
	t.Parallel()
	if testIDs.account != testAccount {
		t.Fatalf("AccountKey(%s) = %s, but the tests' fixtures say %s", me, testIDs.account, testAccount)
	}
}

// newBackend is New for the tests' account.
func newBackend(cache *db.Cache) *InProc {
	b := New(cache)
	b.useAccount(me)
	return b
}
