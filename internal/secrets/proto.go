package secrets

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// Ops the broker serves.
const (
	OpList   = "list"
	OpRun    = "run"
	OpGrant  = "grant"
	OpRevoke = "revoke"
	OpGrants = "grants"
	OpGH     = "gh"
	OpPush   = "push"
	OpFetch  = "fetch"
	OpFleet  = "fleet"
)

// Request is one call. The first line on the wire is its JSON; when
// PayloadSize is set, that many raw bytes (a git bundle) follow.
type Request struct {
	Op string `json:"op"`
	// Project and Task are what the caller says it is (from
	// SPORE_PROJECT_ROOT and SPORE_TASK_SLUG). Agents share one uid,
	// so these scope access but are not proof of identity.
	Project string `json:"project,omitempty"`
	Task    string `json:"task,omitempty"`

	Profile string   `json:"profile,omitempty"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Stdin   string   `json:"stdin,omitempty"`

	// Grant: Task + Profile for For.
	For string `json:"for,omitempty"`

	// Push: Branch at SHA, leased to Lease when set. Fetch: Branch,
	// with Have an object the caller already has.
	Branch string `json:"branch,omitempty"`
	SHA    string `json:"sha,omitempty"`
	Lease  string `json:"lease,omitempty"`
	Have   string `json:"have,omitempty"`

	Enable bool `json:"enable,omitempty"`

	PayloadSize int64 `json:"payload_size,omitempty"`
}

// Response is the reply line; PayloadSize raw bytes may follow.
type Response struct {
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Exit      int    `json:"exit"`
	Error     string `json:"error,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// NeedFull asks a push to resend a bundle without prerequisites.
	NeedFull    bool            `json:"need_full,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	PayloadSize int64           `json:"payload_size,omitempty"`
}

const (
	maxLine    = 4 << 20
	maxPayload = 1 << 30
)

// ErrUnavailable means no broker socket is listening.
var ErrUnavailable = errors.New("secrets broker not available")

// Available reports whether a broker socket exists at SocketPath.
func Available() bool {
	fi, err := os.Stat(SocketPath())
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

// Call sends req (with payload when non-nil) and returns the response
// and its payload, if any. callTimeout bounds the whole exchange.
func Call(req Request, payload io.Reader, callTimeout time.Duration) (Response, []byte, error) {
	conn, err := net.DialTimeout("unix", SocketPath(), 5*time.Second)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Response{}, nil, ErrUnavailable
		}
		return Response{}, nil, fmt.Errorf("secrets broker: %w", err)
	}
	defer conn.Close()
	if callTimeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(callTimeout))
	}
	if err := writeFrame(conn, req, payload, req.PayloadSize); err != nil {
		return Response{}, nil, fmt.Errorf("secrets broker: send: %w", err)
	}
	br := bufio.NewReader(conn)
	var resp Response
	if err := readLine(br, &resp); err != nil {
		return Response{}, nil, fmt.Errorf("secrets broker: reply: %w", err)
	}
	var body []byte
	if resp.PayloadSize > 0 {
		if resp.PayloadSize > maxPayload {
			return Response{}, nil, fmt.Errorf("secrets broker: reply payload too large")
		}
		body = make([]byte, resp.PayloadSize)
		if _, err := io.ReadFull(br, body); err != nil {
			return Response{}, nil, fmt.Errorf("secrets broker: reply payload: %w", err)
		}
	}
	if resp.Error != "" {
		return resp, body, &BrokerError{Msg: resp.Error, NeedFull: resp.NeedFull}
	}
	return resp, body, nil
}

// BrokerError is a refusal or failure reported by the broker.
type BrokerError struct {
	Msg      string
	NeedFull bool
}

func (e *BrokerError) Error() string { return "secrets broker: " + e.Msg }

func writeFrame(w io.Writer, v any, payload io.Reader, size int64) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return err
	}
	if payload != nil && size > 0 {
		n, err := io.CopyN(w, payload, size)
		if err != nil {
			return err
		}
		if n != size {
			return io.ErrShortWrite
		}
	}
	return nil
}

func readLine(br *bufio.Reader, v any) error {
	var line []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			return err
		}
		line = append(line, chunk...)
		if len(line) > maxLine {
			return errors.New("line too long")
		}
		if !isPrefix {
			break
		}
	}
	return json.Unmarshal(line, v)
}
