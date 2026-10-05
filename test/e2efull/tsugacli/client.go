package tsugacli

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultAttempts is how many times Run will issue a call that comes
	// back rate limited, counting the first. Tier C fires roughly 150
	// create+get pairs back to back and Tier A's negatives issue on the
	// order of a thousand calls, so a throttled response is an expected
	// condition rather than a suite failure.
	defaultAttempts = 9
	// defaultRetryDelay is the first backoff; each further attempt doubles it,
	// giving 5s through 20m — about 40 minutes in total. A throttled read is
	// worth waiting out: the resource exists, only the verification is blocked,
	// and the API's window can take tens of minutes to drain.
	//
	// Sized from a measurement, not a guess: after the suite creates Tier C's
	// ~150 resources the API stays rate limited for minutes, and read-only list
	// calls were still returning 429 well after a run had finished. The earlier
	// 1s/2s/4s ladder gave up after ~7 seconds and turned an expected throttle
	// into a suite failure.
	defaultRetryDelay = 5 * time.Second
)

// Client invokes the tsuga CLI. Every invocation carries both the operation
// key and the cluster id: passing the key alone silently reuses whatever
// cluster the local config points at, which 404s against the test org.
type Client struct {
	// Bin is the executable to invoke. Tests point it at a stub.
	Bin string

	cfg Config
	log io.Writer

	// attempts and retryDelay bound the rate-limit retry. They are fields
	// rather than constants so tests can drive the retry without sleeping.
	attempts   int
	retryDelay time.Duration
}

func New(cfg Config, log io.Writer) *Client {
	return &Client{
		Bin:        "tsuga",
		cfg:        cfg,
		log:        log,
		attempts:   defaultAttempts,
		retryDelay: defaultRetryDelay,
	}
}

func (c *Client) Config() Config { return c.cfg }

// CLIError is what Run returns when the CLI exits non-zero. It carries the
// redacted output and the exit code so a caller can tell a missing resource
// from a server error or an expired token.
//
// This matters most on the delete-propagation assertions. Asserting only
// that a `get` failed passes just as readily on a 429, a 500, a network blip
// or an expired token as on the 404 that actually proves the resource is
// gone - a false pass on the one assertion that proves cleanup works.
type CLIError struct {
	// Args is the redacted argument list, without the global flags.
	Args string
	// Output is the CLI's redacted combined output.
	Output string
	// ExitCode is the process exit status, or -1 when the process never ran.
	ExitCode int
	Err      error
}

func (e *CLIError) Error() string {
	return fmt.Sprintf("tsuga %s failed: %v: %s", e.Args, e.Err, e.Output)
}

func (e *CLIError) Unwrap() error { return e.Err }

// The CLI's exact wording for an API error is not documented, so both the
// bare status code and the usual prose spellings are accepted. Matching the
// status code is the reliable half; the prose is the fallback for a CLI that
// renders errors in words.
var (
	notFoundPattern  = regexp.MustCompile(`(?i)\b404\b|not[ _-]?found|no such`)
	rateLimitPattern = regexp.MustCompile(`(?i)\b429\b|rate[ _-]?limit|too many requests`)
)

// IsNotFound reports whether err is a CLI failure describing a missing
// resource, as opposed to any other failure.
//
// Only a *CLIError qualifies: an error from somewhere else in the suite must
// never be read as proof that a Tsuga resource was deleted.
func IsNotFound(err error) bool {
	var cliErr *CLIError
	if !errors.As(err, &cliErr) {
		return false
	}
	return notFoundPattern.MatchString(cliErr.Output)
}

// Run invokes the CLI with the global flags prepended and returns stdout.
// A rate-limited response is retried with backoff; every other failure is
// returned as a *CLIError on the first attempt.
func (c *Client) Run(args ...string) ([]byte, error) {
	full := append([]string{
		"--operation-api-key", c.cfg.APIToken,
		"--cluster", c.cfg.ClusterID,
	}, args...)

	_, _ = fmt.Fprintf(c.log, "tsuga %s\n", c.redact(strings.Join(full, " ")))

	attempts := c.attempts
	if attempts < 1 {
		attempts = 1
	}
	delay := c.retryDelay
	for attempt := 1; ; attempt++ {
		out, err := exec.Command(c.Bin, full...).CombinedOutput()
		if err == nil {
			return out, nil
		}
		cliErr := &CLIError{
			Args:     c.redact(strings.Join(args, " ")),
			Output:   c.redact(strings.TrimSpace(string(out))),
			ExitCode: exitCode(err),
			Err:      err,
		}
		if attempt >= attempts || !rateLimitPattern.MatchString(cliErr.Output) {
			if attempt > 1 && rateLimitPattern.MatchString(cliErr.Output) {
				return out, fmt.Errorf("still rate limited after %d attempts: %w", attempt, cliErr)
			}
			return out, cliErr
		}
		_, _ = fmt.Fprintf(c.log, "tsuga %s was rate limited; retrying in %s (attempt %d of %d)\n",
			cliErr.Args, delay, attempt+1, attempts)
		time.Sleep(delay)
		delay *= 2
	}
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// redact removes the API token from text destined for logs or errors.
func (c *Client) redact(text string) string {
	if c.cfg.APIToken == "" {
		return text
	}
	return strings.ReplaceAll(text, c.cfg.APIToken, "***")
}

// versionToken matches the first dotted-numeric run in a version string, so
// both a bare "1.5.0" and "tsuga version 1.5.0" compare correctly. Comparing
// the whole line makes "tsuga" parse as segment 0, which rejects a current
// CLI as too old.
var versionToken = regexp.MustCompile(`\d+(?:\.\d+)*`)

// CheckVersion fails when the installed CLI is older than minimum. The suite
// depends on -o json and on the kubernetes and traces subcommands, so an old
// CLI must be an explicit failure rather than a confusing assertion error.
func (c *Client) CheckVersion(minimum string) error {
	out, err := exec.Command(c.Bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("tsuga --version failed: %w: %s", err, string(out))
	}
	printed := strings.TrimSpace(string(out))
	installed := versionToken.FindString(printed)
	if installed == "" {
		return fmt.Errorf("tsuga --version printed %q, which carries no version number", printed)
	}
	if compareVersions(installed, minimum) < 0 {
		return fmt.Errorf("tsuga CLI %s is older than the required %s", printed, minimum)
	}
	return nil
}

// compareVersions returns -1, 0 or 1 comparing dotted numeric versions.
// Non-numeric segments sort as 0, which is enough for the CLI's scheme.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		x, y := versionPart(as, i), versionPart(bs, i)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionPart(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(parts[i], "v"))
	return n
}
