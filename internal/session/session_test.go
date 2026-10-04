package session

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egzo-ai/egzo/internal/agentclient"
)

func startHolder(t *testing.T, script string) (*Holder, string) {
	t.Helper()
	holder, err := Start([]string{"sh", "-c", script}, os.Environ(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(holder.Terminate)
	path := filepath.Join(t.TempDir(), "s.sock")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go holder.Serve(listener)
	return holder, path
}

type testClient struct {
	t    *testing.T
	conn net.Conn
	mu   sync.Mutex
	out  bytes.Buffer
	exit chan int
}

func connect(t *testing.T, path string, readOnly bool, rows, cols int) *testClient {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	c := &testClient{t: t, conn: conn, exit: make(chan int, 1)}
	t.Cleanup(func() { conn.Close() })
	body, _ := json.Marshal(hello{ReadOnly: readOnly, Rows: rows, Cols: cols})
	writeFrame(conn, frameHello, body)
	go func() {
		for {
			kind, payload, err := readFrame(conn)
			if err != nil {
				return
			}
			switch kind {
			case frameOutput:
				c.mu.Lock()
				c.out.Write(payload)
				c.mu.Unlock()
			case frameExit:
				var status exitStatus
				json.Unmarshal(payload, &status)
				c.exit <- status.Code
				return
			}
		}
	}()
	return c
}

func itoa(n int) string { return strconv.Itoa(n) }

func waitForHumanInput(t *testing.T, h *Holder) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for h.LastHumanInput().IsZero() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.LastHumanInput().IsZero() {
		t.Fatal("the holder never saw the typing")
	}
}

func (c *testClient) send(text string) { writeFrame(c.conn, frameInput, []byte(text)) }

func (c *testClient) waitFor(text string) {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := c.out.String()
		c.mu.Unlock()
		if strings.Contains(got, text) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t.Fatalf("never saw %q, got %q", text, c.out.String())
}

func (c *testClient) has(text string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Contains(c.out.String(), text)
}

const rawCat = "stty raw -echo; printf READY; exec cat"

func TestInputReachesTheProgramAndOutputComesBackUntouched(t *testing.T) {
	_, path := startHolder(t, rawCat)
	c := connect(t, path, false, 24, 80)
	c.waitFor("READY")
	payload := "\x1b[200~line one\rline two\x1b[201~\x1b[A\x03"
	c.send(payload)
	c.waitFor(payload)
}

func TestAClientArrivingLateGetsWhatTheProgramWroteBefore(t *testing.T) {
	_, path := startHolder(t, rawCat)
	first := connect(t, path, false, 24, 80)
	first.waitFor("READY")
	first.send("history")
	first.waitFor("history")
	late := connect(t, path, false, 24, 80)
	late.waitFor("READYhistory")
}

func TestAReadOnlyClientSeesEverythingAndTypesNothing(t *testing.T) {
	_, path := startHolder(t, rawCat)
	writer := connect(t, path, false, 24, 80)
	observer := connect(t, path, true, 24, 80)
	writer.waitFor("READY")
	observer.waitFor("READY")
	observer.send("ignored")
	writer.send("real")
	observer.waitFor("real")
	if writer.has("ignored") || observer.has("ignored") {
		t.Error("a read-only client's input reached the program")
	}
}

func TestReadOnlyInputDoesNotCountAsAHumanTyping(t *testing.T) {
	holder, path := startHolder(t, rawCat)
	observer := connect(t, path, true, 24, 80)
	observer.waitFor("READY")
	observer.send("x")
	time.Sleep(100 * time.Millisecond)
	if !holder.LastHumanInput().IsZero() {
		t.Error("a read-only client counted as a human typing")
	}
	writer := connect(t, path, false, 24, 80)
	writer.send("x")
	writer.waitFor("x")
	if holder.LastHumanInput().IsZero() {
		t.Error("a read-write client's typing was not recorded")
	}
}

func TestTheWindowSizeFollowsTheClient(t *testing.T) {
	_, path := startHolder(t, `stty -echo; trap 'printf "size:%s\n" "$(stty size)"' WINCH; printf READY; while :; do sleep 0.1; done`)
	c := connect(t, path, false, 24, 80)
	c.waitFor("READY") // the program is listening for the signal from here on
	for _, s := range []size{{Rows: 30, Cols: 100}, {Rows: 50, Cols: 200}} {
		body, _ := json.Marshal(s)
		writeFrame(c.conn, frameResize, body)
		c.waitFor("size:" + itoa(s.Rows) + " " + itoa(s.Cols))
	}
}

func TestTheExitCodeIsTheLastFrame(t *testing.T) {
	holder, path := startHolder(t, "printf bye; exit 7")
	c := connect(t, path, false, 24, 80)
	select {
	case code := <-c.exit:
		if code != 7 {
			t.Errorf("exit code %d, want 7", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no exit frame")
	}
	<-holder.Done()
	if holder.ExitCode() != 7 || !strings.Contains(string(holder.Output()), "bye") {
		t.Errorf("code %d, output %q", holder.ExitCode(), holder.Output())
	}
	late := connect(t, path, false, 24, 80)
	select {
	case code := <-late.exit:
		if code != 7 {
			t.Errorf("late client got %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a client arriving after the exit was not told")
	}
}

func TestInjectTypesAnAtomicBracketedPasteThenEnter(t *testing.T) {
	holder, path := startHolder(t, rawCat)
	c := connect(t, path, false, 24, 80)
	c.waitFor("READY")
	if err := holder.Inject("[egzo msg m1 from operator] hello"); err != nil {
		t.Fatal(err)
	}
	c.waitFor("\x1b[200~[egzo msg m1 from operator] hello\x1b[201~\r")
}

func TestDetachKeys(t *testing.T) {
	for spec, want := range map[string]byte{"ctrl-]": 0x1d, "ctrl-a": 1, "ctrl-Q": 0x11, "ctrl-\\": 0x1c, "ctrl-_": 0x1f} {
		got, err := DetachKeys(spec)
		if err != nil || got != want {
			t.Errorf("%s = %#x, %v; want %#x", spec, got, err, want)
		}
	}
	for _, bad := range []string{"", "ctrl-", "ctrl-ab", "q", "alt-x", "ctrl-1"} {
		if _, err := DetachKeys(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// fakeControl is the part of the control sidecar's agent API the delivery loop talks to.
type fakeControl struct {
	mu        sync.Mutex
	claims    int
	reply     claimed
	activity  []string
	acks      [][]string
	claimBody []byte
}

func (f *fakeControl) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/activity", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ State string }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.activity = append(f.activity, body.State)
		f.mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /v1/claim", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.claims++
		f.claimBody, _ = io.ReadAll(r.Body)
		json.NewEncoder(w).Encode(f.reply)
		f.reply = claimed{}
	})
	mux.HandleFunc("POST /v1/ack", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ IDs []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.acks = append(f.acks, body.IDs)
		f.mu.Unlock()
		w.WriteHeader(204)
	})
	return mux
}

func (f *fakeControl) queue(from, id, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reply.Messages = append(f.reply.Messages, struct {
		ID   string `json:"id"`
		From string `json:"from"`
		Text string `json:"text"`
	}{id, from, text})
}

func deliveryRig(t *testing.T, cfg Delivery) (*Holder, string, *fakeControl) {
	t.Helper()
	holder, path := startHolder(t, rawCat)
	fake := &fakeControl{}
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	api := &agentclient.Client{URL: server.URL, Agent: "coder", Token: "t", HTTP: server.Client()}
	cfg.Interval = 20 * time.Millisecond
	if cfg.Settle == 0 {
		cfg.Settle = 50 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go holder.RunDelivery(ctx, api, cfg)
	return holder, path, fake
}

func TestDeliveryTypesTheClaimedMessagesWithTheirHeadersAsOnePaste(t *testing.T) {
	_, path, fake := deliveryRig(t, Delivery{HumanQuiet: 0, AckTimeout: 7 * time.Second, IdleSignal: "hook"})
	c := connect(t, path, false, 24, 80)
	c.waitFor("READY")
	fake.queue("operator", "m1", "first")
	fake.queue("user:cedric", "m2", "second")
	c.waitFor("\x1b[200~[egzo msg m1 from operator] first\n\n[egzo msg m2 from user:cedric] second\x1b[201~\r")
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !strings.Contains(string(fake.claimBody), `"ack_timeout_ms":7000`) {
		t.Errorf("claim body = %s", fake.claimBody)
	}
	if fake.activity[0] != "starting" {
		t.Errorf("the holder should announce itself first: %v", fake.activity)
	}
}

func TestDeliveryWaitsWhileAHumanIsTyping(t *testing.T) {
	holder, path, fake := deliveryRig(t, Delivery{HumanQuiet: 700 * time.Millisecond, AckTimeout: time.Minute, IdleSignal: "hook"})
	c := connect(t, path, false, 24, 80)
	c.waitFor("READY")
	c.send("t")
	waitForHumanInput(t, holder)
	typed := holder.LastHumanInput()
	fake.queue("operator", "m1", "wait for me")
	c.waitFor("wait for me")
	if waited := time.Since(typed); waited < 600*time.Millisecond {
		t.Errorf("the message was typed %s after the human typed, want at least 700ms", waited)
	}
}

func TestDeliveryDoesNotClaimWhileAHumanIsTyping(t *testing.T) {
	holder, path, fake := deliveryRig(t, Delivery{HumanQuiet: time.Hour, AckTimeout: time.Minute, IdleSignal: "hook"})
	c := connect(t, path, false, 24, 80)
	c.waitFor("READY")
	c.send("t")
	waitForHumanInput(t, holder)
	fake.mu.Lock()
	fake.claims = 0
	fake.mu.Unlock()
	time.Sleep(1500 * time.Millisecond)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.claims != 0 {
		t.Errorf("%d claims while a human was typing", fake.claims)
	}
}

func TestDeliverySendsTheInterruptKey(t *testing.T) {
	_, path, fake := deliveryRig(t, Delivery{HumanQuiet: 0, AckTimeout: time.Minute, IdleSignal: "hook", InterruptKey: "\x1b"})
	c := connect(t, path, false, 24, 80)
	c.waitFor("READY")
	fake.mu.Lock()
	fake.reply.Interrupt = true
	fake.mu.Unlock()
	c.waitFor("\x1b")
}

func TestQuiescenceReportsBusyWhileOutputFlowsAndIdleWhenItStops(t *testing.T) {
	_, path, fake := deliveryRig(t, Delivery{HumanQuiet: time.Hour, IdleSignal: "quiescence", Quiescence: 300 * time.Millisecond})
	c := connect(t, path, false, 24, 80)
	c.waitFor("READY")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		got := strings.Join(fake.activity, ",")
		fake.mu.Unlock()
		if strings.HasSuffix(got, "idle") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.send("noise")
	c.waitFor("noise")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		got := strings.Join(fake.activity, ",")
		fake.mu.Unlock()
		if strings.Contains(got, "idle,busy,idle") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	t.Errorf("activity = %v", fake.activity)
}

func TestQuiescenceAcknowledgesAMessageByItsEchoedHeader(t *testing.T) {
	_, path, fake := deliveryRig(t, Delivery{HumanQuiet: 0, AckTimeout: time.Minute, IdleSignal: "quiescence", Quiescence: time.Hour})
	c := connect(t, path, false, 24, 80)
	c.waitFor("READY")
	fake.queue("operator", "m9", "echo me")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		acked := len(fake.acks) > 0 && fake.acks[0][0] == "m9"
		fake.mu.Unlock()
		if acked {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the echoed header did not acknowledge the message")
}

func TestFramesRoundTripAndRejectOversizedOnes(t *testing.T) {
	var buffer bytes.Buffer
	writeFrame(&buffer, frameOutput, []byte("abc"))
	kind, payload, err := readFrame(&buffer)
	if err != nil || kind != frameOutput || string(payload) != "abc" {
		t.Errorf("round trip = %c %q %v", kind, payload, err)
	}
	huge := []byte{frameInput, 0xff, 0xff, 0xff, 0xff}
	if _, _, err := readFrame(bytes.NewReader(huge)); err == nil {
		t.Error("an oversized frame was accepted")
	}
}

func TestNothingIsTypedBeforeTheProgramHasSpokenAndGoneQuiet(t *testing.T) {
	holder, err := Start([]string{"sh", "-c", "sleep 0.6; printf first; sleep 0.3; printf second; sleep 5"}, os.Environ(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(holder.Terminate)
	fake := &fakeControl{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	api := &agentclient.Client{URL: server.URL, Agent: "coder", Token: "t", HTTP: server.Client()}
	started := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go holder.RunDelivery(ctx, api, Delivery{HumanQuiet: 0, AckTimeout: time.Minute, IdleSignal: "hook", Interval: 20 * time.Millisecond, Settle: 400 * time.Millisecond})
	time.Sleep(100 * time.Millisecond)
	if fake.claimCount() != 0 {
		t.Fatalf("claimed %s after the start, before the program said anything", time.Since(started))
	}
	deadline := time.Now().Add(5 * time.Second)
	for fake.claimCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if waited := time.Since(started); waited < 1300*time.Millisecond {
		t.Errorf("claimed after %s: the program was still drawing until about 1.3s", waited)
	}
}

func (f *fakeControl) claimCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claims
}

func TestRawReportsWhetherTheProgramHasTakenTheTerminalOver(t *testing.T) {
	holder, _ := startHolder(t, "sleep 0.5; stty raw -echo; printf READY; sleep 5")
	if holder.Raw() {
		t.Error("a program that has not touched the terminal is already raw")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !holder.Raw() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !holder.Raw() {
		t.Error("the program went raw but Raw() never said so")
	}
}

func TestADeliveryThatRequiresRawWaitsForTheTerminalTakeoverNotForQuiet(t *testing.T) {
	holder, err := Start([]string{"sh", "-c", "printf boot; sleep 1.2; stty raw -echo; printf ready; sleep 5"}, os.Environ(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(holder.Terminate)
	fake := &fakeControl{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	api := &agentclient.Client{URL: server.URL, Agent: "coder", Token: "t", HTTP: server.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := time.Now()
	go holder.RunDelivery(ctx, api, Delivery{HumanQuiet: 0, AckTimeout: time.Minute, IdleSignal: "hook", Interval: 20 * time.Millisecond, Settle: 100 * time.Millisecond, RequireRaw: true})
	deadline := time.Now().Add(5 * time.Second)
	for fake.claimCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if waited := time.Since(started); waited < 1400*time.Millisecond {
		t.Errorf("claimed after %s: the quiet gap while booting must not count as ready (raw at 1.2s, plus a moment)", waited)
	}
}

func TestADeliveryWithReadyMarkersWaitsForThePromptToBeDrawn(t *testing.T) {
	holder, err := Start([]string{"sh", "-c", "stty raw -echo; printf boot; sleep 1.3; printf 'Ask anything'; sleep 5"}, os.Environ(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(holder.Terminate)
	fake := &fakeControl{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	api := &agentclient.Client{URL: server.URL, Agent: "coder", Token: "t", HTTP: server.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := time.Now()
	go holder.RunDelivery(ctx, api, Delivery{
		HumanQuiet: 0, AckTimeout: time.Minute, IdleSignal: "hook", Interval: 20 * time.Millisecond,
		RequireRaw: true, ReadyMarkers: []string{"Ask anything"},
	})
	deadline := time.Now().Add(5 * time.Second)
	for fake.claimCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if waited := time.Since(started); waited < 1290*time.Millisecond {
		t.Errorf("claimed after %s, before the prompt was drawn at 1.3s", waited)
	}
}

func TestADeliveryGoesAheadWhenTheMarkerNeverShowsUp(t *testing.T) {
	holder, err := Start([]string{"sh", "-c", "stty raw -echo; printf 'a different screen'; sleep 5"}, os.Environ(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(holder.Terminate)
	fake := &fakeControl{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	api := &agentclient.Client{URL: server.URL, Agent: "coder", Token: "t", HTTP: server.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go holder.RunDelivery(ctx, api, Delivery{
		HumanQuiet: 0, AckTimeout: time.Minute, IdleSignal: "hook", Interval: 20 * time.Millisecond,
		RequireRaw: true, ReadyMarkers: []string{"Ask anything"}, ReadyTimeout: 500 * time.Millisecond,
	})
	deadline := time.Now().Add(5 * time.Second)
	for fake.claimCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fake.claimCount() == 0 {
		t.Error("a harness whose screen changed stranded its messages")
	}
}
