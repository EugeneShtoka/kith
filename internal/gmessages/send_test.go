package gmessages

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// How handing a message to the phone can end.
type sendOutcome int

const (
	// tookIt: the phone answered that it took the message.
	tookIt sendOutcome = iota
	// silentPhone: Google took it for the phone, which did not answer.
	silentPhone
	// refused: the phone answered that it did not take it (a transient or lasting status).
	refused
	// failedBefore: the request failed before reaching the phone.
	failedBefore
	// failedAfter: the request failed (a timeout, a library panic) after the phone took it.
	failedAfter
	outcomes
)

func (o sendOutcome) String() string {
	return [...]string{"took it", "silent phone", "refused", "failed before", "failed after"}[o]
}

// reached reports whether the phone has the message after o.
func (o sendOutcome) reached() bool { return o == tookIt || o == silentPhone || o == failedAfter }

// told reports whether o says the phone has it.
func (o sendOutcome) told() bool { return o == tookIt || o == silentPhone }

func (o sendOutcome) answer() (*gmproto.SendMessageResponse, error) {
	switch o {
	case tookIt:
		return &gmproto.SendMessageResponse{Status: gmproto.SendMessageResponse_SUCCESS}, nil
	case silentPhone:
		return nil, libgm.ErrPhoneNotResponding
	case refused:
		return &gmproto.SendMessageResponse{Status: gmproto.SendMessageResponse_FAILURE_2}, nil
	case failedAfter:
		return nil, context.DeadlineExceeded
	case failedBefore, outcomes:
	}
	return nil, errors.New("connection reset")
}

// A send and its retries (the send queue tries every failure again, with the same
// TxnID), interleaved with each other and with the phone's echo in every order: the
// message is never handed to the phone again once it said it has it, sent it back, or
// while a try is under way; a send says it went only when the phone has it, and every
// try after the echo says so; a try that left the phone without it never blocks the next.
func TestASendIsHandedToThePhoneOnce(t *testing.T) {
	t.Parallel()

	for seed := range uint64(5000) {
		rng := rand.New(rand.NewPCG(seed, 7))
		o := newOutbox()
		tmp := tmpFor("kith-txn")
		now := time.Unix(1_700_000_000, 0)
		var (
			inFlight []sendOutcome // tries begun, not ended, with how each will end
			told     bool          // a try ended saying the phone has it
			reached  bool          // the phone has it (it may not have said so)
			echo     bool          // the phone sent it back
			log      []string
		)
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: "+format+"\n%v", append(append([]any{seed}, args...), log)...)
		}
		for range 12 {
			now = now.Add(time.Second)
			switch k := rng.IntN(3); {
			case k == 0: // a try begins
				due, err := o.begin(tmp, now)
				log = append(log, fmt.Sprintf("begin=%v,%v", due, err))
				switch {
				case due && (told || echo || len(inFlight) > 0):
					fail("handed over again (told %v, echoed %v, %d under way)", told, echo, len(inFlight))
				case !due && err == nil && !echo:
					fail("a try said it went with no echo")
				case !due && err != nil && !told && len(inFlight) == 0 && !echo:
					fail("blocked after only failures: %v", err)
				case echo && (due || err != nil):
					fail("a try after the echo: %v, %v", due, err)
				}
				if due {
					inFlight = append(inFlight, sendOutcome(rng.IntN(int(outcomes))))
				}
			case k == 1 && len(inFlight) > 0: // the oldest try ends
				out := inFlight[0]
				inFlight = inFlight[1:]
				reached = reached || out.reached()
				resp, sendErr := out.answer()
				err := o.end(tmp, resp, sendErr, now)
				log = append(log, fmt.Sprintf("end(%s)=%v", out, err))
				if err == nil && !out.told() && !echo {
					fail("%s said it went", out)
				}
				if err != nil && echo {
					fail("%s after the echo: %v", out, err)
				}
				told = told || out.told()
			case k == 2 && (reached || len(inFlight) > 0 && inFlight[0].reached()): // the phone sends it back
				reached = true
				o.echoed(tmp)
				echo = true
				log = append(log, "echo")
			}
		}
	}
}

// The outbox forgets a send long past, and a send's temporary ID is its TxnID's, the
// same after a restart, and new without one.
func TestTheOutboxForgets(t *testing.T) {
	t.Parallel()

	again, fresh := tmpFor("a"), tmpFor("")
	if tmpFor("a") != again || tmpFor("a") == tmpFor("b") || tmpFor("") == fresh {
		t.Error("temporary IDs do not follow TxnIDs")
	}
	o := newOutbox()
	now := time.Unix(1_700_000_000, 0)
	o.echoed("old")
	o.sends["old"] = outgoing{state: echoed, at: now}
	if due, _ := o.begin("new", now.Add(outboxSpan+time.Minute)); !due {
		t.Error("a new send was not handed over")
	}
	if _, kept := o.sends["old"]; kept {
		t.Error("a send long past is still remembered")
	}
}

// What the phone is handed: the text (an emote as /me), the participant you are there,
// the SIM (the conversation's, else the phone's for that participant), a reply, and RCS
// forced only in an RCS chat sent to as the phone chooses that switched to encryption,
// whatever order its notices came in. A conversation not listed is not sent to.
func TestTheRequestIsWhatThePhoneNeeds(t *testing.T) {
	t.Parallel()

	_, c, _, _ := cachedAdapter(t)
	rcs := active(danaDM, "", false, 10, false, pMe, pDana)
	rcs.Type, rcs.DefaultOutgoingID = gmproto.ConversationType_RCS, "1"
	sms := active(group, "G", true, 10, false, pMe, pDana, pEli)
	sms.Type, sms.DefaultOutgoingID = gmproto.ConversationType_SMS, "1"
	sms.SimCard = &gmproto.SIMCard{SIMData: &gmproto.SIMData{SIMPayload: &gmproto.SIMPayload{SIMNumber: 2}}}
	c.inbox.take(rcs)
	c.inbox.take(sms)
	c.keepSIMs([]*gmproto.SIMCard{{
		SIMData:        &gmproto.SIMData{SIMPayload: &gmproto.SIMPayload{SIMNumber: 1}},
		SIMParticipant: &gmproto.SIMParticipant{ID: "1"},
	}})

	req, err := c.request(danaDM, "t1", domain.Draft{Body: "waves", Emote: true, ReplyTo: eventID(self, danaDM, "m9")})
	if err != nil {
		t.Fatal(err)
	}
	p := req.GetMessagePayload()
	if got := p.GetMessageInfo()[0].GetMessageContent().GetContent(); got != "/me waves" {
		t.Errorf("text %q", got)
	}
	if p.GetParticipantID() != "1" || p.GetTmpID() != "t1" || req.GetTmpID() != "t1" || req.GetReply().GetMessageID() != "m9" {
		t.Errorf("request %v", req)
	}
	if req.GetSIMPayload().GetSIMNumber() != 1 || req.GetForceRCS() {
		t.Errorf("an RCS chat never encrypted: SIM %d, force %v", req.GetSIMPayload().GetSIMNumber(), req.GetForceRCS())
	}
	if req, _ = c.request(group, "t2", domain.Draft{Body: "hi"}); req.GetSIMPayload().GetSIMNumber() != 2 {
		t.Errorf("the conversation's own SIM not used: %v", req.GetSIMPayload())
	}

	notice := func(status gmproto.MessageStatusType, ms int64) *gmproto.Message {
		return &gmproto.Message{
			MessageID: fmt.Sprint(ms), ConversationID: danaDM, Timestamp: ms * 1000,
			MessageStatus: &gmproto.MessageStatus{Status: status},
		}
	}
	on := gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_PROTOCOL_SWITCH_RCS_TO_E2EE
	off := gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_PROTOCOL_SWITCH_E2EE_TO_RCS
	forced := func() bool {
		t.Helper()
		req, err := c.request(danaDM, "t", domain.Draft{Body: "x"})
		if err != nil {
			t.Fatal(err)
		}
		return req.GetForceRCS()
	}
	for _, tc := range []struct {
		notices []*gmproto.Message
		want    bool
	}{
		{[]*gmproto.Message{notice(on, 20)}, true},
		{[]*gmproto.Message{notice(off, 30)}, false},
		{[]*gmproto.Message{notice(on, 25)}, false},                 // older than the switch back
		{[]*gmproto.Message{notice(on, 50), notice(off, 40)}, true}, // newest first, as a listing
	} {
		for _, m := range tc.notices {
			c.inbox.switched(m)
		}
		if got := forced(); got != tc.want {
			t.Errorf("after %v: forced %v, want %v", tc.notices, got, tc.want)
		}
	}
	c.inbox.convs[danaDM].mode = gmproto.ConversationSendMode_SEND_MODE_XMS
	if forced() {
		t.Error("RCS forced in a chat held to SMS")
	}

	if _, err := c.request("99", "t", domain.Draft{Body: "x"}); !errors.Is(err, errNotListed) {
		t.Errorf("an unlisted conversation: %v", err)
	}
}

// A chat whose messages go as SMS says so in the cache, after a restart too, and stops
// when it becomes RCS; a message the phone could not send says so.
func TestAnSMSChatSaysSo(t *testing.T) {
	t.Parallel()

	a, c, cache, _ := cachedAdapter(t)
	ctx := context.Background()
	sms := active(danaDM, "", false, 10, false, pMe, pDana)
	sms.Type = gmproto.ConversationType_SMS
	held := active(group, "G", true, 10, false, pMe, pDana, pEli)
	held.Type, held.SendMode = gmproto.ConversationType_RCS, gmproto.ConversationSendMode_SEND_MODE_XMS_LATCH
	rcs := active("13", "R", true, 10, false, pMe, pDana)
	rcs.Type = gmproto.ConversationType_RCS
	a.takeConversations(ctx, c, []*gmproto.Conversation{sms, held, rcs})
	flags := func() map[string]bool {
		t.Helper()
		rooms, err := cache.Rooms(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for i := range rooms {
			out[rooms[i].Name] = rooms[i].SMS
		}
		return out
	}
	if got := flags(); !got["Dana Reyes"] || !got["G"] || got["R"] {
		t.Errorf("SMS chats: %v", got)
	}
	sms.Type = gmproto.ConversationType_RCS
	a.takeConversations(ctx, c, []*gmproto.Conversation{sms})
	if got := flags(); got["Dana Reyes"] || !got["G"] {
		t.Errorf("after one became RCS: %v", got)
	}

	in := c.inbox
	failed := text(danaDM, "m1", "1", "see you", 20, gmproto.MessageStatusType_OUTGOING_FAILED_GENERIC)
	if msg, _ := in.message(failed); msg.Body != "see you\n"+notSentLabel {
		t.Errorf("a failed send reads %q", msg.Body)
	}
	sent := text(danaDM, "m1", "1", "see you", 20, gmproto.MessageStatusType_OUTGOING_DELIVERED)
	if msg, _ := in.message(sent); msg.Body != "see you" {
		t.Errorf("a delivered send reads %q", msg.Body)
	}
}

// The echo the phone sends back settles a retry, live or from a listing.
func TestTheEchoSettlesARetry(t *testing.T) {
	t.Parallel()

	a, c, _, _ := cachedAdapter(t)
	ctx := context.Background()
	a.takeConversations(ctx, c, []*gmproto.Conversation{active(danaDM, "", false, 10, false, pMe, pDana)})
	tmp := tmpFor("kith-1")
	if due, _ := a.outbox.begin(tmp, time.Now()); !due {
		t.Fatal("not handed over")
	}
	if err := a.outbox.end(tmp, nil, libgm.ErrPhoneNotResponding, time.Now()); !errors.Is(err, errAwaitingPhone) {
		t.Fatalf("a silent phone: %v", err)
	}
	echo := text(danaDM, "m1", "1", "hi", 20, gmproto.MessageStatusType_OUTGOING_COMPLETE)
	echo.TmpID = tmp
	a.takeMessages(ctx, c, []*gmproto.Message{echo}, false)
	if due, err := a.outbox.begin(tmp, time.Now()); due || err != nil {
		t.Errorf("a retry after the echo: %v, %v", due, err)
	}
}
