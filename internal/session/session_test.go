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
	got := c.out.String()
	if len(got) > 300 {
		got = "..." + got[len(got)-300:]
	}
	c.t.Fatalf("never saw %q, got %q", text, got)
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
	return mux
}

// announce is what the control sidecar will answer to the next claim: a line to type.
func (f *fakeControl) announce(line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reply.Line = line
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

func TestDeliveryTypesTheLineTheControlSidecarComposesAsOnePaste(t *testing.T) {
	_, path, fake := deliveryRig(t, Delivery{HumanQuiet: 0, AckTimeout: 7 * time.Second, IdleSignal: "hook"})
	c := connect(t, path, false, 24, 80)
	c.waitFor("READY")
	fake.announce("check egzo message m0123 and handle the request for me.")
	c.waitFor("\x1b[200~check egzo message m0123 and handle the request for me.\x1b[201~\r")
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
	fake.announce("wait for me")
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

func TestQuiescenceReportsWorkingWhileOutputFlowsAndIdleWhenItStops(t *testing.T) {
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
		if strings.Contains(got, "idle,working,idle") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	t.Errorf("activity = %v", fake.activity)
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

func TestSanitizeKeepsTextAndRemovesWhatCouldLeaveAPaste(t *testing.T) {
	cases := map[string]string{
		"plain text, tabs\tand\nnewlines":  "plain text, tabs\tand\nnewlines",
		"unicode: café ☕ 你好":               "unicode: café ☕ 你好",
		"end the paste\x1b[201~ then type": "end the paste[201~ then type",
		"carriage\rreturn":                 "carriage\nreturn",
		"bell\x07 nul\x00 del\x7f":         "bell nul del",
		"c1 \u009b[31m csi":                "c1 [31m csi",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
	if strings.ContainsRune(Sanitize("a\x1b[201~b"), 0x1b) {
		t.Error("an escape survived")
	}
}

func TestDeliveryNeverTypesAnEscapeFromTheLine(t *testing.T) {
	_, path, fake := deliveryRig(t, Delivery{HumanQuiet: 0, AckTimeout: time.Minute, IdleSignal: "hook"})
	c := connect(t, path, false, 24, 80)
	c.waitFor("READY")
	fake.announce("end it\x1b[201~ and press y\r")
	c.waitFor("\x1b[200~end it[201~ and press y\n\x1b[201~\r")
}

// A client that never reads must not freeze the program or the other clients: the holder drops it.
func TestAClientThatStopsReadingDoesNotFreezeTheProgram(t *testing.T) {
	_, path := startHolder(t, "stty raw -echo; printf READY; i=0; while [ $i -lt 400 ]; do head -c 65536 /dev/zero | tr '\\0' x; i=$((i+1)); done; printf DONE; sleep 30")
	stuck, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer stuck.Close()
	body, _ := json.Marshal(hello{Rows: 24, Cols: 80})
	writeFrame(stuck, frameHello, body) // and never reads again
	healthy, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer healthy.Close()
	writeFrame(healthy, frameHello, body)
	seen := make(chan struct{})
	go func() {
		var tail []byte
		for {
			kind, payload, err := readFrame(healthy)
			if err != nil {
				return
			}
			if kind == frameOutput {
				tail = append(tail, payload...)
				if len(tail) > 16 {
					tail = tail[len(tail)-16:]
				}
				if bytes.Contains(tail, []byte("DONE")) {
					close(seen)
					return
				}
			}
		}
	}()
	select {
	case <-seen:
	case <-time.After(20 * time.Second):
		t.Fatal("a client that stopped reading froze the program")
	}
}

func TestTheProgramsExitReachesAClientThatIsSlowToRead(t *testing.T) {
	h, path := startHolder(t, "printf 'bye'; exit 3")
	c := connect(t, path, false, 24, 80)
	select {
	case code := <-c.exit:
		if code != 3 {
			t.Errorf("exit code = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no exit frame")
	}
	<-h.Done()
}

func TestAClientThatNeverSaysHelloIsDropped(t *testing.T) {
	h, path := startHolder(t, rawCat)
	h.helloTimeout.Store(int64(200 * time.Millisecond))
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil || strings.Contains(err.Error(), "timeout") {
		t.Fatalf("the holder kept a silent client: %v", err)
	}
}

// Resizing and attaching while the program exits must be safe (go test -race).
func TestAttachingWhileTheProgramExitsIsRaceFree(t *testing.T) {
	for i := 0; i < 30; i++ {
		h, path := startHolder(t, "exit 0")
		var wg sync.WaitGroup
		for j := 0; j < 3; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				conn, err := net.Dial("unix", path)
				if err != nil {
					return
				}
				defer conn.Close()
				body, _ := json.Marshal(hello{Rows: 30 + j, Cols: 100})
				writeFrame(conn, frameHello, body)
				rb, _ := json.Marshal(size{Rows: 40, Cols: 90})
				writeFrame(conn, frameResize, rb)
				conn.SetReadDeadline(time.Now().Add(time.Second))
				io.Copy(io.Discard, conn)
			}()
		}
		wg.Wait()
		<-h.Done()
	}
}

func TestInjectedTextIsNotInterleavedWithTyping(t *testing.T) {
	h, path := startHolder(t, rawCat)
	c := connect(t, path, false, 24, 80)
	c.waitFor("READY")
	done := make(chan struct{})
	go func() { h.Inject("ANNOUNCED"); close(done) }()
	time.Sleep(50 * time.Millisecond) // inside the pause between the paste and Enter
	c.send("typed")
	<-done
	c.waitFor("\x1b[200~ANNOUNCED\x1b[201~\rtyped")
}

func TestResetOnDetachAfterTheProgramChangedTheTerminalModes(t *testing.T) {
	ptmx, tty, err := ptyPair()
	if err != nil {
		t.Skip("no pty:", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	_, path := startHolder(t, "printf 'READY\\033[?1000h'; sleep 30")
	var out syncBuffer
	done := make(chan struct{})
	go func() { Attach(path, false, 0x1d, tty, &out); close(done) }()
	waitUntil(t, func() bool { return strings.Contains(out.String(), "READY") })
	ptmx.Write([]byte{0x1d})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("attach did not return after the detach key")
	}
	if !strings.Contains(out.String(), "\x1b[?1000l") {
		t.Errorf("no reset after detach: %q", out.String())
	}
}

func TestNoResetIsWrittenWhenInputIsNotATerminal(t *testing.T) {
	_, path := startHolder(t, "printf READY; exit 0")
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	var out syncBuffer
	if _, err := Attach(path, true, 0x1d, r, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b[?") {
		t.Errorf("escape sequences written to a pipe: %q", out.String())
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !condition() {
		t.Fatal("condition never held")
	}
}
