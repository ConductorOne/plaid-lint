// Package gocacheprog implements a client for the GOCACHEPROG wire
// protocol (cmd/go/internal/cacheprog). One long-lived helper
// subprocess per plaid-lint run; JSON-per-line over stdin/stdout,
// multiplexed by Request.ID.
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
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

type Cmd string

const (
	CmdGet   = Cmd("get")
	CmdPut   = Cmd("put")
	CmdClose = Cmd("close")
)

type request struct {
	ID       int64  `json:"ID"`
	Command  Cmd    `json:"Command"`
	ActionID []byte `json:"ActionID,omitempty"`
	OutputID []byte `json:"OutputID,omitempty"`
	BodySize int64  `json:"BodySize,omitempty"`
}

type response struct {
	ID            int64      `json:"ID"`
	Err           string     `json:"Err,omitempty"`
	KnownCommands []Cmd      `json:"KnownCommands,omitempty"`
	Miss          bool       `json:"Miss,omitempty"`
	OutputID      []byte     `json:"OutputID,omitempty"`
	Size          int64      `json:"Size,omitempty"`
	Time          *time.Time `json:"Time,omitempty"`
	DiskPath      string     `json:"DiskPath,omitempty"`
}

type Client struct {
	cmd          *exec.Cmd // nil in tests
	stdin        io.WriteCloser
	bw           *bufio.Writer
	jenc         *json.Encoder
	known        map[Cmd]bool
	closing      atomic.Bool
	closeOnce    sync.Once
	closeErr     error
	readLoopDone chan struct{}

	mu       sync.Mutex
	nextID   int64
	inFlight map[int64]chan *response

	writeMu sync.Mutex

	transportMu sync.Mutex
	transportEr error

	// sf coalesces concurrent same-action Gets to a single wire
	// round-trip. L2 reads for shared transitive deps see
	// duplicate Gets when multiple analyzers fan out across the same
	// import graph; collapsing them halves the protocol pressure for
	// hot keys.
	sf            singleflight.Group
	sfTotalGets   atomic.Uint64
	sfCoalesced   atomic.Uint64
	sfStatTotal   atomic.Uint64
	sfStatCoalesc atomic.Uint64
}

// SingleflightStats reports cumulative Get + Stat duplicate-coalescing
// counts. coalesced/total ≈ fraction of calls that did NOT issue a
// wire round-trip because a same-key call was already in flight. Used
// by tests and by the probe to confirm singleflight is paying its
// keep on cascade workloads.
type SingleflightStats struct {
	GetTotal      uint64
	GetCoalesced  uint64
	StatTotal     uint64
	StatCoalesced uint64
}

func (c *Client) SingleflightStats() SingleflightStats {
	return SingleflightStats{
		GetTotal:      c.sfTotalGets.Load(),
		GetCoalesced:  c.sfCoalesced.Load(),
		StatTotal:     c.sfStatTotal.Load(),
		StatCoalesced: c.sfStatCoalesc.Load(),
	}
}

// New forks binary and waits for its capability handshake.
func New(binary string, args []string) (*Client, error) {
	cmd := exec.Command(binary, args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("gocacheprog: stdin pipe: %w", err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("gocacheprog: stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("gocacheprog: start %q: %w", binary, err)
	}
	return newOverPipes(cmd, in, out)
}

// newOverPipes is the testable constructor: caller supplies pipes
// already wired up. cmd may be nil when the peer is not a subprocess.
func newOverPipes(cmd *exec.Cmd, stdin io.WriteCloser, stdout io.ReadCloser) (*Client, error) {
	c := &Client{
		cmd:          cmd,
		stdin:        stdin,
		bw:           bufio.NewWriter(stdin),
		inFlight:     make(map[int64]chan *response),
		readLoopDone: make(chan struct{}),
	}
	c.jenc = json.NewEncoder(c.bw)
	// Reserve ID 0 for the handshake so readLoop dispatches it.
	capCh := make(chan *response, 1)
	c.inFlight[0] = capCh
	go c.readLoop(stdout)

	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case res := <-capCh:
		if res == nil {
			return nil, c.transportError(errors.New("helper closed before handshake"))
		}
		if len(res.KnownCommands) == 0 {
			return nil, errors.New("gocacheprog: helper declared no supported commands")
		}
		c.known = make(map[Cmd]bool, len(res.KnownCommands))
		for _, k := range res.KnownCommands {
			c.known[k] = true
		}
		return c, nil
	case <-timer.C:
		return nil, errors.New("gocacheprog: helper did not send handshake within 10s")
	}
}

func (c *Client) readLoop(stdout io.Reader) {
	defer close(c.readLoopDone)
	jd := json.NewDecoder(stdout)
	for {
		res := new(response)
		if err := jd.Decode(res); err != nil {
			c.failPending(err)
			return
		}
		c.mu.Lock()
		ch, ok := c.inFlight[res.ID]
		delete(c.inFlight, res.ID)
		c.mu.Unlock()
		if ok {
			ch <- res
		}
		// Unknown IDs dropped silently.
	}
}

func (c *Client) failPending(err error) {
	c.transportMu.Lock()
	if c.transportEr == nil {
		c.transportEr = err
	}
	c.transportMu.Unlock()
	c.mu.Lock()
	for id, ch := range c.inFlight {
		close(ch)
		delete(c.inFlight, id)
	}
	c.mu.Unlock()
}

func (c *Client) transportError(fallback error) error {
	c.transportMu.Lock()
	defer c.transportMu.Unlock()
	if c.transportEr != nil {
		return c.transportEr
	}
	return fallback
}

func (c *Client) send(ctx context.Context, req *request, body []byte) (*response, error) {
	if c.closing.Load() {
		return nil, c.transportError(errors.New("gocacheprog: client closed"))
	}
	resc := make(chan *response, 1)
	c.mu.Lock()
	if c.inFlight == nil {
		c.mu.Unlock()
		return nil, c.transportError(errors.New("gocacheprog: client closed"))
	}
	c.nextID++
	req.ID = c.nextID
	c.inFlight[req.ID] = resc
	c.mu.Unlock()

	if err := c.writeRequest(req, body); err != nil {
		c.mu.Lock()
		delete(c.inFlight, req.ID)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case res, ok := <-resc:
		if !ok || res == nil {
			return nil, c.transportError(errors.New("gocacheprog: helper closed mid-request"))
		}
		if res.Err != "" {
			return nil, fmt.Errorf("gocacheprog: helper error: %s", res.Err)
		}
		return res, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// writeRequest serialises one JSON header + optional base64 body line
// to the helper. writeMu keeps two concurrent senders from
// interleaving bytes on the wire.
func (c *Client) writeRequest(req *request, body []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.jenc.Encode(req); err != nil {
		return fmt.Errorf("gocacheprog: encode request: %w", err)
	}
	if req.BodySize > 0 {
		if int64(len(body)) != req.BodySize {
			return fmt.Errorf("gocacheprog: body size mismatch: have %d want %d", len(body), req.BodySize)
		}
		if err := c.bw.WriteByte('"'); err != nil {
			return err
		}
		enc := base64.NewEncoder(base64.StdEncoding, c.bw)
		if _, err := enc.Write(body); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
		if _, err := c.bw.WriteString("\"\n"); err != nil {
			return err
		}
	}
	return c.bw.Flush()
}

// getResult is the singleflight payload for the wire-level Get. It
// records only the protocol response — outputID, diskPath, hit — and
// NOT the body bytes. Each shared caller does its own os.Open on the
// DiskPath so the body read can stream, and so peers do
// not block waiting for the leader's full os.ReadFile to complete
// before they can begin decode.
type getResult struct {
	outputID [32]byte
	diskPath string
	hit      bool
}

// getWire performs the singleflight-coalesced wire round-trip for
// action and returns the protocol response. Body read is intentionally
// NOT done here: callers open the DiskPath themselves.
func (c *Client) getWire(ctx context.Context, action [32]byte) (*getResult, error) {
	c.sfTotalGets.Add(1)
	// Singleflight key is the raw 32-byte action ID. Using the bytes
	// directly (not hex) keeps the allocation to one fixed-size string
	// per call; same-action concurrent callers share the flight.
	key := string(action[:])
	val, doErr, shared := c.sf.Do(key, func() (any, error) {
		res, err := c.send(ctx, &request{Command: CmdGet, ActionID: action[:]}, nil)
		if err != nil {
			return nil, err
		}
		gr := &getResult{}
		if res.Miss {
			return gr, nil
		}
		if res.DiskPath == "" {
			return nil, errors.New("gocacheprog: hit without DiskPath")
		}
		copy(gr.outputID[:], res.OutputID)
		gr.diskPath = res.DiskPath
		gr.hit = true
		return gr, nil
	})
	if shared {
		c.sfCoalesced.Add(1)
	}
	if doErr != nil {
		return nil, doErr
	}
	return val.(*getResult), nil
}

// GetReader returns the cached body for action as an io.ReadCloser
// streaming directly from the helper's on-disk cache file. The
// returned reader is an *os.File; closing it releases the descriptor.
// On miss, ReadCloser is nil and hit is false. Streaming
// lets large L2 bodies (gcexportdata, often hundreds of KB) decode
// incrementally instead of going through a full io.ReadAll into a
// pre-allocated slice.
func (c *Client) GetReader(ctx context.Context, action [32]byte) (outputID [32]byte, rc io.ReadCloser, hit bool, err error) {
	if !c.known[CmdGet] {
		return outputID, nil, false, errors.New("gocacheprog: helper does not support get")
	}
	gr, err := c.getWire(ctx, action)
	if err != nil {
		return outputID, nil, false, err
	}
	if !gr.hit {
		return outputID, nil, false, nil
	}
	f, err := os.Open(gr.diskPath)
	if err != nil {
		return outputID, nil, false, fmt.Errorf("gocacheprog: open DiskPath: %w", err)
	}
	return gr.outputID, f, true, nil
}

// Get is a convenience wrapper around GetReader that loads the entire
// body into a byte slice. Callers that decode streaming (or that
// already have a streaming sink) should prefer GetReader. Callers
// that need bytes (small L1 entries, error paths) get the same
// behavior as before — exact-bytes equivalence preserved.
func (c *Client) Get(ctx context.Context, action [32]byte) (outputID [32]byte, body []byte, hit bool, err error) {
	outputID, rc, hit, err := c.GetReader(ctx, action)
	if err != nil || !hit {
		return outputID, nil, hit, err
	}
	defer func() { _ = rc.Close() }()
	body, err = io.ReadAll(rc)
	if err != nil {
		return outputID, nil, false, fmt.Errorf("gocacheprog: read DiskPath: %w", err)
	}
	return outputID, body, true, nil
}

// Stat issues the protocol Get for action and reports hit/miss only,
// skipping the os.ReadFile(DiskPath) body read that Get performs. It
// is the cheap-Has primitive: the backend's Has contract
// (internal/cache/backend.go:30-33) says Has must be cheaper than Get
// — no decode, no touch. Round-trip cost is identical to Get; only the
// post-protocol disk read is elided, which is the part that dominates
// for L2 entries (gcexportdata, often hundreds of KB).
func (c *Client) Stat(ctx context.Context, action [32]byte) (hit bool, err error) {
	if !c.known[CmdGet] {
		return false, errors.New("gocacheprog: helper does not support get")
	}
	c.sfStatTotal.Add(1)
	// Separate keyspace from Get ("s\x00" prefix) so a Stat in flight
	// and a Get in flight for the same action do not share a result —
	// they need different return shapes.
	key := "s\x00" + string(action[:])
	val, doErr, shared := c.sf.Do(key, func() (any, error) {
		res, err := c.send(ctx, &request{Command: CmdGet, ActionID: action[:]}, nil)
		if err != nil {
			return false, err
		}
		return !res.Miss, nil
	})
	if shared {
		c.sfStatCoalesc.Add(1)
	}
	if doErr != nil {
		return false, doErr
	}
	return val.(bool), nil
}

func (c *Client) Put(ctx context.Context, action [32]byte, outputID [32]byte, body []byte) error {
	if !c.known[CmdPut] {
		return errors.New("gocacheprog: helper does not support put")
	}
	_, err := c.send(ctx, &request{
		Command:  CmdPut,
		ActionID: action[:],
		OutputID: outputID[:],
		BodySize: int64(len(body)),
	}, body)
	return err
}

// PidForTest returns the helper subprocess pid, or -1 if the client
// was not wired to a subprocess (the newOverPipes test path). Used by
// shutdown-deadlock tests to assert the child has actually exited
// after Close returns.
func (c *Client) PidForTest() int {
	if c.cmd == nil || c.cmd.Process == nil {
		return -1
	}
	return c.cmd.Process.Pid
}

// Close sends a close request, waits for the read loop, and reaps the
// subprocess. The close request goes out BEFORE flipping c.closing so
// send() does not refuse it.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		if c.known[CmdClose] {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, c.closeErr = c.send(ctx, &request{Command: CmdClose}, nil)
			cancel()
		}
		c.closing.Store(true)
		_ = c.stdin.Close()
		<-c.readLoopDone
		if c.cmd != nil {
			_ = c.cmd.Wait()
		}
	})
	return c.closeErr
}
