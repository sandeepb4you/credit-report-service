package digitap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

// The point of logging a curl command is that someone can paste it into a shell
// and reproduce an upstream failure. A command that looks right but does not run
// — or that runs and sends something other than what the service sent — is worse
// than no command, because it sends the reader off diagnosing the wrong request.
// So the main test here actually executes the thing.

func TestShellQuote_EscapesApostrophes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`plain`, `'plain'`},
		{`{"pan":"ABCDE1234F"}`, `'{"pan":"ABCDE1234F"}'`},
		// A name like O'BRIEN is the realistic way this breaks: an unescaped
		// apostrophe closes the quoted run and the rest becomes shell syntax.
		{`O'BRIEN`, `'O'\''BRIEN'`},
		{`a'b'c`, `'a'\''b'\''c'`},
		{``, `''`},
	}
	for _, tc := range cases {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCurlCommand_Shape(t *testing.T) {
	cmd := curlCommand("https://api.digitap.ai/credit_analytics/request",
		"36537966", "sekret", []byte(`{"pan":"ABCDE1234F"}`))

	for _, want := range []string{
		"curl -X POST 'https://api.digitap.ai/credit_analytics/request'",
		"-u '36537966:sekret'",
		"-H 'Content-Type: application/json'",
		`-d '{"pan":"ABCDE1234F"}'`,
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("curlCommand() missing %q\ngot: %s", want, cmd)
		}
	}
}

// TestCurlCommand_RoundTripsThroughAShell runs the generated command for real
// and asserts the server saw exactly what the Go client would have sent: same
// body, same Basic credentials. This is what makes the logged command
// trustworthy as a reproduction.
func TestCurlCommand_RoundTripsThroughAShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX sh available")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl available")
	}

	// An apostrophe in the name and double quotes throughout — the combination
	// that a naive quoting scheme mangles.
	const body = `{"first_name":"O'BRIEN","last_name":"D\"SOUZA","pan":"ABCDE1234F"}`
	const id, secret = "36537966", "tKZ2Cpf'HZX"

	var (
		gotBody   string
		gotAuth   string
		gotCType  string
		gotMethod string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotAuth = r.Header.Get("Authorization")
		gotCType = r.Header.Get("Content-Type")
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cmd := curlCommand(srv.URL, id, secret, []byte(body))
	if out, err := exec.Command(sh, "-c", cmd+" -s -o /dev/null").CombinedOutput(); err != nil {
		t.Fatalf("generated command did not run: %v\ncmd: %s\nout: %s", err, cmd, out)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotBody != body {
		t.Errorf("body round-tripped wrong\n got: %s\nwant: %s", gotBody, body)
	}
	if gotCType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCType)
	}

	// The credentials must arrive as the same header SetBasicAuth would produce,
	// including a secret containing an apostrophe.
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))
	if gotAuth != wantAuth {
		t.Errorf("Authorization = %q, want %q", gotAuth, wantAuth)
	}
}

// TestCurlCommand_MatchesSetBasicAuth pins the claim made in curlCommand's doc
// comment: -u produces the identical header to req.SetBasicAuth, so the logged
// command authenticates the same way the service does.
func TestCurlCommand_MatchesSetBasicAuth(t *testing.T) {
	const id, secret = "36537966", "tKZ2CpfHZXOFWok5uXcqSMtGwsYsGg5a"

	req, err := http.NewRequest(http.MethodPost, "https://example.invalid/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(id, secret)

	fromGo := req.Header.Get("Authorization")
	fromCurl := "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))
	if fromGo != fromCurl {
		t.Errorf("SetBasicAuth = %q but curl -u would send %q", fromGo, fromCurl)
	}
}

// TestRequest_LogsCurlOnlyWhenEnabled drives the real Request path and asserts
// the flag actually gates the log line. The default must be silence: a PII-and-
// credential-bearing log line that appears without anyone asking is the failure
// mode worth a test.
func TestRequest_LogsCurlOnlyWhenEnabled(t *testing.T) {
	const body = `{"client_ref_num":"CA-1-abc","pan":"ABCDE1234F","first_name":"O'BRIEN","mobile_no":"9876543210"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"http_response_code":200,"result_code":101,"message":"success","result":{}}`))
	}))
	defer srv.Close()

	run := func(t *testing.T, enabled bool) string {
		t.Helper()
		var buf bytes.Buffer
		c := New(Config{
			BaseURL:        srv.URL,
			ClientID:       "36537966",
			ClientSecret:   "sekret",
			LogRequestCurl: enabled,
			CurlOut:        &buf,
		})
		if c.IsStub() {
			t.Fatal("client fell back to the stub; the test would prove nothing")
		}
		if _, _, err := c.Request(context.Background(), json.RawMessage(body)); err != nil {
			t.Fatalf("Request: %v", err)
		}
		return buf.String()
	}

	t.Run("enabled", func(t *testing.T) {
		out := run(t, true)
		if !strings.Contains(out, "curl -X POST") {
			t.Errorf("flag on but no curl command logged\ngot: %s", out)
		}
		// The whole point is that it is runnable, so the credentials and the
		// body have to be in there verbatim.
		if !strings.Contains(out, "36537966:sekret") {
			t.Errorf("logged curl carries no credentials\ngot: %s", out)
		}
		if !strings.Contains(out, "ABCDE1234F") {
			t.Errorf("logged curl carries no payload\ngot: %s", out)
		}
		// And it must announce what it contains, so nobody pastes it into a
		// ticket without noticing.
		if !strings.Contains(out, "CLIENT SECRET") {
			t.Errorf("logged curl does not warn about its contents\ngot: %s", out)
		}
		// The reason it bypasses slog: the body must survive verbatim, with
		// real double quotes, or the command is not pasteable.
		if !strings.Contains(out, `"pan":"ABCDE1234F"`) {
			t.Errorf("body was escaped; the command is not pasteable\ngot: %s", out)
		}
		if strings.Contains(out, `\"pan\"`) {
			t.Errorf("body came out escaped\ngot: %s", out)
		}
	})

	t.Run("disabled by default", func(t *testing.T) {
		out := run(t, false)
		if strings.Contains(out, "curl -X POST") {
			t.Errorf("flag off but a curl command was logged anyway\ngot: %s", out)
		}
		for _, secret := range []string{"sekret", "ABCDE1234F", "9876543210"} {
			if strings.Contains(out, secret) {
				t.Errorf("flag off but %q reached the log\ngot: %s", secret, out)
			}
		}
	})
}
