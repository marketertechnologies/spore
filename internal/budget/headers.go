// API-mode spool reader for spore budget.
//
// Anthropic API responses carry rolling rate-limit state on every
// response (`anthropic-ratelimit-*` headers). An external producer
// appends them to a spool; the readers here fold the latest line into
// state.json under api mode. spore itself never writes the spool.
//
// Spool format (`$AGENT_BUDGET_STATE_DIR/api-headers.jsonl`, 0600):
//
//	{"ts":"2026-04-26T10:30:00Z","identity":"runner-a",
//	 "model":"claude-haiku-4-5",
//	 "headers":{"anthropic-ratelimit-tokens-remaining":"40000", ...}}
//
// One JSON object per line.

package budget

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	apiHeadersFilename  = "api-headers.jsonl"
	apiAutoDetectMaxAge = 30 * time.Minute
)

type headerLine struct {
	Timestamp time.Time         `json:"ts"`
	Identity  string            `json:"identity"`
	Model     string            `json:"model,omitempty"`
	Headers   map[string]string `json:"headers"`
}

func apiHeadersPath() (string, error) {
	d, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, apiHeadersFilename), nil
}

// readLatestHeaderLine returns the most recent header line in the
// spool, optionally filtered by identity. Returns (nil, nil) when the
// spool is missing or empty.
func readLatestHeaderLine(identity string) (*headerLine, error) {
	p, err := apiHeadersPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() == 0 {
		return nil, nil
	}

	const tailWindow int64 = 64 * 1024
	start := fi.Size() - tailWindow
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, fi.Size()-start)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}

	for end := len(buf); end > 0; {
		nl := lastNewlineBefore(buf, end)
		lineStart := nl + 1
		raw := buf[lineStart:end]
		if len(strings.TrimSpace(string(raw))) > 0 {
			var hl headerLine
			if err := json.Unmarshal(raw, &hl); err == nil {
				if identity == "" || hl.Identity == identity {
					return &hl, nil
				}
			}
		}
		if nl < 0 {
			break
		}
		end = nl
	}
	return nil, nil
}

func lastNewlineBefore(b []byte, end int) int {
	for i := end - 1; i >= 0; i-- {
		if b[i] == '\n' {
			return i
		}
	}
	return -1
}

// rateLimitReading collapses the multi-bucket headers into one frac /
// reset pair: pick the bucket with the lowest remaining/limit ratio
// (the one closest to 429). The chosen bucket name lands in
// TokensBucket for diagnostics.
type rateLimitReading struct {
	Frac            float64
	TokensRemaining int64
	TokensLimit     int64
	Bucket          string
	ResetAt         *time.Time
}

var rateLimitBuckets = []string{
	"input-tokens",
	"output-tokens",
	"tokens",
	"requests",
}

func parseRateLimitReading(headers map[string]string) (rateLimitReading, bool) {
	best := rateLimitReading{Frac: -1}
	for _, bucket := range rateLimitBuckets {
		remStr, ok := headers["anthropic-ratelimit-"+bucket+"-remaining"]
		if !ok {
			continue
		}
		limStr, ok := headers["anthropic-ratelimit-"+bucket+"-limit"]
		if !ok {
			continue
		}
		rem, err := strconv.ParseInt(remStr, 10, 64)
		if err != nil {
			continue
		}
		lim, err := strconv.ParseInt(limStr, 10, 64)
		if err != nil || lim <= 0 {
			continue
		}
		frac := float64(lim-rem) / float64(lim)
		if frac < 0 {
			frac = 0
		}
		if frac > best.Frac {
			best = rateLimitReading{
				Frac:            frac,
				TokensRemaining: rem,
				TokensLimit:     lim,
				Bucket:          bucket,
			}
			if rs := headers["anthropic-ratelimit-"+bucket+"-reset"]; rs != "" {
				if t, err := time.Parse(time.RFC3339, rs); err == nil {
					t = t.UTC()
					best.ResetAt = &t
				}
			}
		}
	}
	if best.Frac < 0 {
		return rateLimitReading{}, false
	}
	return best, true
}

// resolveMode picks subscription vs api. Explicit env wins; otherwise
// auto-detect: if the spool has a header line within
// apiAutoDetectMaxAge, use api. Falls back to subscription.
func resolveMode(now time.Time) (string, error) {
	if m := strings.ToLower(os.Getenv("AGENT_BUDGET_MODE")); m != "" {
		switch m {
		case "api", "subscription":
			return m, nil
		default:
			return "", fmt.Errorf("AGENT_BUDGET_MODE=%q invalid (want api|subscription)", m)
		}
	}
	hl, err := readLatestHeaderLine(os.Getenv("AGENT_BUDGET_IDENTITY"))
	if err != nil {
		return "subscription", nil
	}
	if hl == nil {
		return "subscription", nil
	}
	if now.Sub(hl.Timestamp) <= apiAutoDetectMaxAge {
		return "api", nil
	}
	return "subscription", nil
}
