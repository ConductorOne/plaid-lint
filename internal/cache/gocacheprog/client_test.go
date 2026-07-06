package gocacheprog

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type stub struct {
	t          *testing.T
	client     *Client
	helperRead *bufio.Reader
	helperW    io.WriteCloser
}

func newStub(t *testing.T, known []Cmd) *stub {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		b, _ := json.Marshal(response{ID: 0, KnownCommands: known})
		_, _ = outW.Write(append(b, '\n'))
	}()
	c, err := newOverPipes(nil, inW, outR)
	if err != nil {
		t.Fatalf("newOverPipes: %v", err)
	}
	return &stub{t, c, bufio.NewReader(inR), outW}
}

func (s *stub) readReq() (request, []byte) {
	s.t.Helper()
	line, err := s.helperRead.ReadBytes('\n')
	if err != nil {
		s.t.Fatalf("read request: %v", err)
	}
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		s.t.Fatalf("decode request %q: %v", line, err)
	}
	var body []byte
	if req.BodySize > 0 {
		bl, err := s.helperRead.ReadBytes('\n')
		if err != nil {
			s.t.Fatalf("read body: %v", err)
		}
		tr := strings.TrimRight(string(bl), "\n")
		if len(tr) < 2 || tr[0] != '"' || tr[len(tr)-1] != '"' {
			s.t.Fatalf("malformed body line: %q", bl)
		}
		body, err = base64.StdEncoding.DecodeString(tr[1 : len(tr)-1])
		if err != nil {
			s.t.Fatalf("base64: %v", err)
		}
	}
	return req, body
}

func (s *stub) reply(res response) {
	s.t.Helper()
	b, _ := json.Marshal(&res)
	if _, err := s.helperW.Write(append(b, '\n')); err != nil {
		s.t.Fatalf("write reply: %v", err)
	}
}

func TestClient_HandshakeAndClose(t *testing.T) {
	s := newStub(t, []Cmd{CmdGet, CmdPut, CmdClose})
	done := make(chan error, 1)
	go func() { done <- s.client.Close() }()
	req, _ := s.readReq()
	if req.Command != CmdClose {
		t.Fatalf("first command = %q, want close", req.Command)
	}
	s.reply(response{ID: req.ID})
	_ = s.helperW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Close did not return")
	}
}

func TestClient_GetMissAndHit(t *testing.T) {
	s := newStub(t, []Cmd{CmdGet, CmdPut, CmdClose})
	defer func() { _ = s.helperW.Close() }()
	type r struct {
		body []byte
		hit  bool
		err  error
	}

	miss := make(chan r, 1)
	go func() {
		_, body, hit, err := s.client.Get(context.Background(), [32]byte{1})
		miss <- r{body, hit, err}
	}()
	req, _ := s.readReq()
	s.reply(response{ID: req.ID, Miss: true})
	if got := <-miss; got.err != nil || got.hit {
		t.Fatalf("miss: err=%v hit=%v", got.err, got.hit)
	}

	tmp := filepath.Join(t.TempDir(), "obj")
	want := []byte("payload body")
	if err := os.WriteFile(tmp, want, 0o644); err != nil {
		t.Fatal(err)
	}
	hit := make(chan r, 1)
	go func() {
		_, body, h, err := s.client.Get(context.Background(), [32]byte{2})
		hit <- r{body, h, err}
	}()
	req, _ = s.readReq()
	s.reply(response{ID: req.ID, DiskPath: tmp, Size: int64(len(want))})
	got := <-hit
	if got.err != nil || !got.hit || string(got.body) != string(want) {
		t.Fatalf("hit: err=%v hit=%v body=%q", got.err, got.hit, got.body)
	}
}

func TestClient_PutRoundTrip(t *testing.T) {
	s := newStub(t, []Cmd{CmdGet, CmdPut, CmdClose})
	defer func() { _ = s.helperW.Close() }()
	want := []byte("hello put")
	action, output := [32]byte{0xab, 0xcd}, [32]byte{0x99}
	done := make(chan error, 1)
	go func() { done <- s.client.Put(context.Background(), action, output, want) }()
	req, body := s.readReq()
	if req.Command != CmdPut ||
		string(req.ActionID) != string(action[:]) ||
		string(req.OutputID) != string(output[:]) ||
		req.BodySize != int64(len(want)) ||
		string(body) != string(want) {
		t.Fatalf("put mismatch: req=%+v body=%q", req, body)
	}
	s.reply(response{ID: req.ID, DiskPath: "/tmp/x"})
	if err := <-done; err != nil {
		t.Fatalf("Put: %v", err)
	}
}

func TestClient_ConcurrentRequests(t *testing.T) {
	s := newStub(t, []Cmd{CmdGet, CmdPut, CmdClose})
	defer func() { _ = s.helperW.Close() }()
	const N = 10
	type res struct {
		i   int
		hit bool
	}
	results := make(chan res, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			var a [32]byte
			a[0] = byte(i)
			_, _, hit, err := s.client.Get(context.Background(), a)
			if err != nil {
				t.Errorf("Get[%d]: %v", i, err)
			}
			results <- res{i, hit}
		}()
	}
	type p struct{ id, idx int64 }
	got := make([]p, 0, N)
	for j := 0; j < N; j++ {
		req, _ := s.readReq()
		got = append(got, p{req.ID, int64(req.ActionID[0])})
	}
	// Reply in reverse to stress out-of-order dispatch.
	for k := len(got) - 1; k >= 0; k-- {
		if got[k].idx%2 == 0 {
			s.reply(response{ID: got[k].id, Miss: true})
		} else {
			tmp := filepath.Join(t.TempDir(), fmt.Sprintf("h-%d", got[k].idx))
			if err := os.WriteFile(tmp, []byte{byte(got[k].idx)}, 0o644); err != nil {
				t.Fatal(err)
			}
			s.reply(response{ID: got[k].id, DiskPath: tmp})
		}
	}
	wg.Wait()
	close(results)
	seen := make(map[int]bool, N)
	for r := range results {
		seen[r.i] = true
		if r.hit != (r.i%2 != 0) {
			t.Errorf("Get[%d]: hit=%v want %v", r.i, r.hit, r.i%2 != 0)
		}
	}
	if len(seen) != N {
		t.Errorf("only saw %d/%d", len(seen), N)
	}
}

// TestClient_StatSkipsBody pins the option-1 contract: Stat issues the
// protocol Get but does NOT read the DiskPath body. We point DiskPath
// at a path that does not exist; Get would error, Stat must succeed.
func TestClient_StatSkipsBody(t *testing.T) {
	s := newStub(t, []Cmd{CmdGet, CmdPut, CmdClose})
	defer func() { _ = s.helperW.Close() }()

	type r struct {
		hit bool
		err error
	}

	// Miss: Stat returns hit=false, no body read.
	miss := make(chan r, 1)
	go func() {
		hit, err := s.client.Stat(context.Background(), [32]byte{1})
		miss <- r{hit, err}
	}()
	req, _ := s.readReq()
	if req.Command != CmdGet {
		t.Fatalf("Stat must issue CmdGet on the wire, got %q", req.Command)
	}
	s.reply(response{ID: req.ID, Miss: true})
	if got := <-miss; got.err != nil || got.hit {
		t.Fatalf("Stat miss: err=%v hit=%v", got.err, got.hit)
	}

	// Hit: DiskPath points at a non-existent file. Get would fail
	// reading it; Stat must not touch DiskPath and so must succeed.
	hit := make(chan r, 1)
	go func() {
		h, err := s.client.Stat(context.Background(), [32]byte{2})
		hit <- r{h, err}
	}()
	req, _ = s.readReq()
	s.reply(response{ID: req.ID, DiskPath: "/this/path/does/not/exist/anywhere", Size: 99})
	got := <-hit
	if got.err != nil || !got.hit {
		t.Fatalf("Stat hit: err=%v hit=%v (must succeed without touching DiskPath)", got.err, got.hit)
	}
}

// TestClient_SingleflightCoalesces pins the singleflight-coalescing contract: N
// concurrent same-action Gets collapse to ONE wire round-trip. The
// test holds the wire reply until every peer has entered sf.Do, then
// releases the reply and asserts no extra requests were issued.
func TestClient_SingleflightCoalesces(t *testing.T) {
	s := newStub(t, []Cmd{CmdGet, CmdPut, CmdClose})
	defer func() { _ = s.helperW.Close() }()

	tmp := filepath.Join(t.TempDir(), "obj")
	want := []byte("singleflight-payload")
	if err := os.WriteFile(tmp, want, 0o644); err != nil {
		t.Fatal(err)
	}

	const N = 16
	var action [32]byte
	action[0] = 0xff

	type r struct {
		body []byte
		hit  bool
		err  error
	}
	results := make(chan r, N)

	// Launch all N goroutines at once. They race into sf.Do; one wins
	// and becomes the leader, the rest attach as peers. As long as we
	// do NOT reply to the leader's wire request until all N have
	// entered Do, the peers cannot start fresh flights.
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, body, hit, err := s.client.Get(context.Background(), action)
			results <- r{body, hit, err}
		}()
	}
	close(start)

	// The leader will issue exactly one wire request. Read it. Any
	// goroutine that entered Do before the leader's flight completes
	// joins as a peer.
	req, _ := s.readReq()

	// Hold the reply for long enough that all N goroutines have
	// definitely entered sf.Do. 100 ms is generous on this host.
	time.Sleep(100 * time.Millisecond)
	s.reply(response{ID: req.ID, DiskPath: tmp, Size: int64(len(want))})

	wg.Wait()
	close(results)
	for got := range results {
		if got.err != nil || !got.hit || string(got.body) != string(want) {
			t.Fatalf("coalesced Get: err=%v hit=%v body=%q", got.err, got.hit, got.body)
		}
	}

	stats := s.client.SingleflightStats()
	if stats.GetTotal != N {
		t.Errorf("GetTotal = %d, want %d", stats.GetTotal, N)
	}
	// Peers that joined the in-flight call must register as
	// coalesced; the leader is also flagged shared by sf once at
	// least one peer attached. Lower bound: N-1.
	if stats.GetCoalesced < N-1 {
		t.Errorf("GetCoalesced = %d, want >= %d (broken coalescing)", stats.GetCoalesced, N-1)
	}

	// Drain: confirm the stub received exactly ONE wire request. A
	// second request would mean a peer launched its own flight.
	extra := make(chan request, 1)
	go func() {
		r, _ := s.readReq()
		extra <- r
	}()
	select {
	case got := <-extra:
		t.Fatalf("unexpected second wire request: %+v (coalescing failed)", got)
	case <-time.After(100 * time.Millisecond):
		// expected: no second request issued.
	}
}

// TestClient_SingleflightSeparatesGetAndStat pins the keyspace split:
// a Stat for action X and a Get for action X must NOT collapse onto
// the same flight — they return different shapes.
func TestClient_SingleflightSeparatesGetAndStat(t *testing.T) {
	s := newStub(t, []Cmd{CmdGet, CmdPut, CmdClose})
	defer func() { _ = s.helperW.Close() }()

	var action [32]byte
	action[0] = 0x42

	tmp := filepath.Join(t.TempDir(), "obj")
	if err := os.WriteFile(tmp, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	type gr struct {
		body []byte
		hit  bool
		err  error
	}
	type sr struct {
		hit bool
		err error
	}
	getDone := make(chan gr, 1)
	statDone := make(chan sr, 1)

	go func() {
		_, body, hit, err := s.client.Get(context.Background(), action)
		getDone <- gr{body, hit, err}
	}()
	go func() {
		hit, err := s.client.Stat(context.Background(), action)
		statDone <- sr{hit, err}
	}()

	// Expect TWO independent requests (one for Get, one for Stat).
	// Read both, reply to both — order is non-deterministic, so we
	// match by request ID.
	req1, _ := s.readReq()
	req2, _ := s.readReq()
	s.reply(response{ID: req1.ID, DiskPath: tmp, Size: 7})
	s.reply(response{ID: req2.ID, DiskPath: tmp, Size: 7})

	g, st := <-getDone, <-statDone
	if g.err != nil || !g.hit || string(g.body) != "payload" {
		t.Fatalf("Get: err=%v hit=%v body=%q", g.err, g.hit, g.body)
	}
	if st.err != nil || !st.hit {
		t.Fatalf("Stat: err=%v hit=%v", st.err, st.hit)
	}
}

// TestClient_GetStreaming pins the streaming contract: GetReader
// yields an io.ReadCloser whose bytes equal the old []byte path's
// bytes. Streaming consumers (e.g. gcexportdata.Read) can begin
// decode without waiting for a full io.ReadAll.
func TestClient_GetStreaming(t *testing.T) {
	s := newStub(t, []Cmd{CmdGet, CmdPut, CmdClose})
	defer func() { _ = s.helperW.Close() }()

	tmp := filepath.Join(t.TempDir(), "obj")
	want := make([]byte, 16*1024) // L2-ish body
	for i := range want {
		want[i] = byte(i)
	}
	if err := os.WriteFile(tmp, want, 0o644); err != nil {
		t.Fatal(err)
	}

	type r struct {
		out [32]byte
		rc  io.ReadCloser
		hit bool
		err error
	}
	rch := make(chan r, 1)
	go func() {
		out, rc, hit, err := s.client.GetReader(context.Background(), [32]byte{0xa1})
		rch <- r{out, rc, hit, err}
	}()
	req, _ := s.readReq()
	s.reply(response{ID: req.ID, DiskPath: tmp, Size: int64(len(want)), OutputID: []byte{0xbe, 0xef}})
	got := <-rch
	if got.err != nil || !got.hit || got.rc == nil {
		t.Fatalf("GetReader: err=%v hit=%v rc=%v", got.err, got.hit, got.rc)
	}
	defer func() { _ = got.rc.Close() }()

	// The reader must be a real io.ReadCloser; reading in chunks is
	// permitted (streaming). Read in 1 KiB chunks to exercise the
	// streaming path.
	var read []byte
	buf := make([]byte, 1024)
	for {
		n, err := got.rc.Read(buf)
		if n > 0 {
			read = append(read, buf[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("rc.Read: %v", err)
		}
	}
	if string(read) != string(want) {
		t.Fatalf("streamed bytes mismatch: got %d bytes want %d", len(read), len(want))
	}

	// Closing must succeed; double-close MUST not panic (os.File
	// returns an error on second close but does not panic).
	if err := got.rc.Close(); err != nil {
		// First explicit close (defer above is second close).
		t.Logf("first Close: %v", err)
	}

	// And the bytes wrapper (Get) MUST still return identical bytes
	// — streaming preserves byte equivalence with the prior path.
	bch := make(chan struct {
		body []byte
		err  error
	}, 1)
	go func() {
		_, body, _, err := s.client.Get(context.Background(), [32]byte{0xa2})
		bch <- struct {
			body []byte
			err  error
		}{body, err}
	}()
	req, _ = s.readReq()
	s.reply(response{ID: req.ID, DiskPath: tmp, Size: int64(len(want))})
	b := <-bch
	if b.err != nil || string(b.body) != string(want) {
		t.Fatalf("Get bytes-wrapper: err=%v body-len=%d want-len=%d", b.err, len(b.body), len(want))
	}
}

func TestClient_TransportFailure(t *testing.T) {
	s := newStub(t, []Cmd{CmdGet, CmdPut, CmdClose})
	done := make(chan error, 1)
	go func() {
		_, _, _, err := s.client.Get(context.Background(), [32]byte{7})
		done <- err
	}()
	_, _ = s.readReq()
	_ = s.helperW.Close()
	select {
	case err := <-done:
		if err == nil || errors.Is(err, context.Canceled) {
			t.Fatalf("transport failure misreported: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Get hung after transport failure")
	}
}
