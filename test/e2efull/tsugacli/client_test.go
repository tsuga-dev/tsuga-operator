package tsugacli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stubCLI writes a fake `tsuga` executable that echoes its own arguments as
// JSON, so tests can assert on exactly what the wrapper invoked.
func stubCLI(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tsuga")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func testConfig() Config {
	return Config{
		APIToken:     "secret-token",
		OTLPEndpoint: "https://intake.example.tsuga.com",
		IngestionKey: "ingest-key",
		ClusterID:    "abc-123",
		BaseURL:      "https://api.tsuga.com",
	}
}

func TestRunInjectsKeyAndClusterFlags(t *testing.T) {
	bin := stubCLI(t, `printf '{"args":"%s"}' "$*"`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	out, err := c.Run("logs", "search")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{
		"--operation-api-key secret-token",
		"--cluster abc-123",
		"logs search",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("invocation should contain %q, got: %s", want, got)
		}
	}
}

func TestRunRedactsTokenFromLog(t *testing.T) {
	bin := stubCLI(t, `echo '{}'`)
	var logged bytes.Buffer
	c := New(testConfig(), &logged)
	c.Bin = bin

	if _, err := c.Run("dashboards", "list"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logged.String(), "secret-token") {
		t.Fatalf("log must not contain the API token, got: %s", logged.String())
	}
	if !strings.Contains(logged.String(), "dashboards list") {
		t.Fatalf("log should record the command, got: %s", logged.String())
	}
}

func TestRunReturnsErrorWithStderrOnFailure(t *testing.T) {
	bin := stubCLI(t, `echo "boom" >&2; exit 3`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	_, err := c.Run("logs", "search")
	if err == nil {
		t.Fatal("want an error when the CLI exits non-zero")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error should carry the CLI output, got: %v", err)
	}
}

func TestRunRedactsTokenFromErrorOutput(t *testing.T) {
	bin := stubCLI(t, `echo "auth failed for token secret-token" >&2; exit 1`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	_, err := c.Run("dashboards", "list")
	if err == nil {
		t.Fatal("want an error when the CLI exits non-zero")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error must not contain the API token, got: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("error should contain redaction marker, got: %v", err)
	}
}

func TestCheckVersionRejectsTooOld(t *testing.T) {
	bin := stubCLI(t, `echo "1.2.0"`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	if err := c.CheckVersion("1.3.0"); err == nil {
		t.Fatal("want an error when the CLI is older than the minimum")
	}
}

func TestCheckVersionAcceptsEqualOrNewer(t *testing.T) {
	for _, version := range []string{"1.3.0", "1.4.2", "2.0.0"} {
		bin := stubCLI(t, "echo "+version)
		c := New(testConfig(), &bytes.Buffer{})
		c.Bin = bin
		if err := c.CheckVersion("1.3.0"); err != nil {
			t.Errorf("version %s should satisfy minimum 1.3.0: %v", version, err)
		}
	}
}

// TestCheckVersionAcceptsAPrefixedVersionLine covers the shape that broke
// the previous comparison: comparing the whole trimmed line parses "tsuga"
// as segment 0, so a current CLI is rejected as older than the minimum.
func TestCheckVersionAcceptsAPrefixedVersionLine(t *testing.T) {
	for _, printed := range []string{
		"1.5.0",
		"tsuga version 1.5.0",
		"tsuga v1.5.0",
		"tsuga version 1.5.0 (build abc1234)",
	} {
		bin := stubCLI(t, "echo '"+printed+"'")
		c := New(testConfig(), &bytes.Buffer{})
		c.Bin = bin
		if err := c.CheckVersion("1.3.0"); err != nil {
			t.Errorf("output %q should satisfy minimum 1.3.0: %v", printed, err)
		}
	}
}

func TestCheckVersionRejectsAPrefixedVersionThatIsTooOld(t *testing.T) {
	bin := stubCLI(t, `echo "tsuga version 1.2.9"`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	if err := c.CheckVersion("1.3.0"); err == nil {
		t.Fatal("want an error when the printed version is older than the minimum")
	}
}

func TestCheckVersionReportsOutputCarryingNoVersion(t *testing.T) {
	bin := stubCLI(t, `echo "tsuga"`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	err := c.CheckVersion("1.3.0")
	if err == nil {
		t.Fatal("want an error when the output carries no version number")
	}
	if !strings.Contains(err.Error(), "no version number") {
		t.Fatalf("want an error saying no version was found, got: %v", err)
	}
}

// countingStubCLI writes a stub that replays one line of `responses` per
// invocation, tracking which call it is on through a file in its own
// directory. The final response repeats once the list is exhausted.
func countingStubCLI(t *testing.T, responses []struct {
	Output string
	Exit   int
}) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncount=$(cat " + dir + "/count 2>/dev/null || echo 0)\n" +
		"count=$((count + 1))\necho $count > " + dir + "/count\n"
	for i, response := range responses {
		operator := "-eq"
		if i == len(responses)-1 {
			operator = "-ge"
		}
		script += "if [ $count " + operator + " " + itoa(i+1) + " ]; then\n" +
			"  echo '" + response.Output + "'\n  exit " + itoa(response.Exit) + "\nfi\n"
	}
	path := filepath.Join(dir, "tsuga")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestRunRetriesARateLimitedCallAndThenSucceeds(t *testing.T) {
	bin := countingStubCLI(t, []struct {
		Output string
		Exit   int
	}{
		{"429 Too Many Requests", 1},
		{"429 Too Many Requests", 1},
		{`{"dashboards":[]}`, 0},
	})
	var logged bytes.Buffer
	c := New(testConfig(), &logged)
	c.Bin = bin
	c.retryDelay = time.Millisecond

	out, err := c.Run("dashboards", "list")
	if err != nil {
		t.Fatalf("want the third attempt to succeed, got: %v", err)
	}
	if !strings.Contains(string(out), "dashboards") {
		t.Fatalf("want the successful response, got: %s", out)
	}
	if !strings.Contains(logged.String(), "rate limited") {
		t.Fatalf("want the retries recorded in the log, got: %s", logged.String())
	}
}

func TestRunGivesUpOnAPersistentRateLimitWithAClearError(t *testing.T) {
	bin := stubCLI(t, `echo "429 Too Many Requests" >&2; exit 1`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin
	c.attempts = 2
	c.retryDelay = time.Millisecond

	_, err := c.Run("dashboards", "list")
	if err == nil {
		t.Fatal("want an error when every attempt is rate limited")
	}
	if !strings.Contains(err.Error(), "still rate limited after 2 attempts") {
		t.Fatalf("want an error naming the exhausted retries, got: %v", err)
	}
}

func TestRunDoesNotRetryANonRateLimitFailure(t *testing.T) {
	bin := countingStubCLI(t, []struct {
		Output string
		Exit   int
	}{
		{"500 Internal Server Error", 1},
		{`{"dashboards":[]}`, 0},
	})
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin
	c.retryDelay = time.Millisecond

	if _, err := c.Run("dashboards", "list"); err == nil {
		t.Fatal("want a 500 to fail on the first attempt rather than be retried")
	}
}

func TestIsNotFoundDistinguishesA404FromOtherFailures(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		stub     string
		wantFlag bool
	}{
		{"404 status", `echo "Error: request failed with status 404" >&2; exit 1`, true},
		{"prose", `echo "Error: dashboard not found" >&2; exit 1`, true},
		{"500 status", `echo "Error: request failed with status 500" >&2; exit 1`, false},
		{"rate limited", `echo "Error: 429 too many requests" >&2; exit 1`, false},
		{"expired token", `echo "Error: 401 unauthorized" >&2; exit 1`, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			c := New(testConfig(), &bytes.Buffer{})
			c.Bin = stubCLI(t, testCase.stub)
			c.attempts = 1

			_, err := c.Run("dashboards", "get", "abc")
			if err == nil {
				t.Fatal("want the stub to fail")
			}
			if got := IsNotFound(err); got != testCase.wantFlag {
				t.Fatalf("want IsNotFound %v for %q, got %v", testCase.wantFlag, testCase.stub, got)
			}
		})
	}
}

// A plain error must never read as proof that a resource is gone.
func TestIsNotFoundRejectsAnErrorThatIsNotACLIFailure(t *testing.T) {
	if IsNotFound(errors.New("dashboard not found")) {
		t.Fatal("want IsNotFound to be false for an error the CLI did not produce")
	}
	if IsNotFound(nil) {
		t.Fatal("want IsNotFound to be false for a nil error")
	}
}

func TestRunRecordsTheExitCodeOnTheError(t *testing.T) {
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = stubCLI(t, `echo "nope" >&2; exit 7`)
	c.attempts = 1

	_, err := c.Run("dashboards", "list")
	var cliErr *CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("want a *CLIError, got %T: %v", err, err)
	}
	if cliErr.ExitCode != 7 {
		t.Fatalf("want exit code 7, got %d", cliErr.ExitCode)
	}
}
