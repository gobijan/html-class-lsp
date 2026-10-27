package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// client talks to a server over in-memory pipes, like an editor would.
type client struct {
	t         *testing.T
	in        *io.PipeWriter
	nextID    int
	responses chan wireMessage
	done      chan error

	mu       sync.Mutex
	received []wireMessage // requests and notifications from the server
}

type wireMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// startServer starts a server and initializes it with the given position
// encodings and initialize params.
func startServer(t *testing.T, encodings []string, params map[string]any) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, in: inW, responses: make(chan wireMessage), done: make(chan error, 1)}
	go func() {
		c.done <- newServer().serve(inR, outW)
		_ = outW.Close()
	}()
	t.Cleanup(func() { _ = inW.Close() })

	// Read whatever the server sends as soon as it does, so it's never stuck
	// writing a notification while the test is writing to it.
	go func() {
		out := bufio.NewReader(outR)
		for {
			body, err := readMessage(out)
			if err != nil {
				close(c.responses)
				return
			}
			var m wireMessage
			if err := json.Unmarshal(body, &m); err != nil {
				t.Error(err)
				continue
			}
			if m.Method == "" {
				c.responses <- m
				continue
			}
			c.mu.Lock()
			c.received = append(c.received, m)
			c.mu.Unlock()
		}
	}()

	if params == nil {
		params = map[string]any{}
	}
	capabilities, _ := params["capabilities"].(map[string]any)
	if capabilities == nil {
		capabilities = map[string]any{}
		params["capabilities"] = capabilities
	}
	capabilities["general"] = map[string]any{"positionEncodings": encodings}
	var init struct {
		Capabilities struct {
			PositionEncoding string `json:"positionEncoding"`
		} `json:"capabilities"`
	}
	if err := c.request("initialize", params, &init); err != nil {
		t.Fatal(err)
	}
	want := "utf-16"
	if len(encodings) > 0 && encodings[0] == "utf-8" {
		want = "utf-8"
	}
	if got := init.Capabilities.PositionEncoding; got != want {
		t.Fatalf("positionEncoding = %q, want %q", got, want)
	}
	c.notify("initialized", map[string]any{})
	return c
}

func (c *client) send(m map[string]any) {
	c.t.Helper()
	m["jsonrpc"] = "2.0"
	if err := writeMessage(c.in, m); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) notify(method string, params any) {
	c.t.Helper()
	c.send(map[string]any{"method": method, "params": params})
}

// request sends a request and decodes the result into result. It returns the
// error the server responded with, if any.
func (c *client) request(method string, params, result any) *rpcError {
	c.t.Helper()
	c.nextID++
	c.send(map[string]any{"id": c.nextID, "method": method, "params": params})

	resp, ok := <-c.responses
	if !ok {
		c.t.Fatal("server closed the connection")
	}
	if want := fmt.Sprint(c.nextID); string(resp.ID) != want {
		c.t.Fatalf("response id %s, want %s", resp.ID, want)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if result != nil {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			c.t.Fatal(err)
		}
	}
	return nil
}

// open opens a document whose text marks the cursor with "|" and returns the
// cursor position, counted in UTF-8 bytes or UTF-16 units.
func (c *client) open(uri, text string, utf8 bool) position {
	c.t.Helper()
	before, after, _ := strings.Cut(text, "|")
	c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": uri, "languageId": "html", "version": 1, "text": before + after},
	})
	return (&server{utf8: utf8}).position(before, len(before))
}

func (c *client) complete(uri string, pos position) []string {
	c.t.Helper()
	var list completionList
	if err := c.request("textDocument/completion", at(uri, pos), &list); err != nil {
		c.t.Fatal(err)
	}
	labels := []string{}
	for _, item := range list.Items {
		labels = append(labels, item.Label)
	}
	return labels
}

func (c *client) define(uri string, pos position) []location {
	c.t.Helper()
	var locations []location
	if err := c.request("textDocument/definition", at(uri, pos), &locations); err != nil {
		c.t.Fatal(err)
	}
	return locations
}

// sync waits until the server has handled everything sent to it so far. It
// answers in order, so once a request is answered, so is all before it.
func (c *client) sync() {
	c.t.Helper()
	_ = c.request("test/sync", nil, nil) // answered with MethodNotFound
}

// messages returns the params of the requests and notifications with the
// given method the server sent so far.
func (c *client) messages(method string) []json.RawMessage {
	c.t.Helper()
	c.sync()
	c.mu.Lock()
	defer c.mu.Unlock()
	var params []json.RawMessage
	for _, m := range c.received {
		if m.Method == method {
			params = append(params, m.Params)
		}
	}
	return params
}

// diagnostics returns the diagnostics the server last published for uri, or
// nil if it published none.
func (c *client) diagnostics(uri string) []diagnostic {
	c.t.Helper()
	var latest []diagnostic
	for _, raw := range c.messages("textDocument/publishDiagnostics") {
		var p publishParams
		if err := json.Unmarshal(raw, &p); err != nil {
			c.t.Fatal(err)
		}
		if p.URI == uri {
			latest = p.Diagnostics
		}
	}
	return latest
}

func at(uri string, pos position) map[string]any {
	return map[string]any{"textDocument": map[string]any{"uri": uri}, "position": pos}
}

func setupProject(t *testing.T) (dir, uri string) {
	t.Helper()
	dir = t.TempDir()
	css := "/* Größe */\n.card { padding: 1rem }\n.card-title, .button {}\n"
	if err := os.WriteFile(filepath.Join(dir, "style.css"), []byte(css), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, pathToURI(filepath.Join(dir, "index.html"))
}

const page = `<link rel="stylesheet" href="style.css">
<style>.inline {}</style>
<p>Größe 🙂</p><div class="card ca|"></div>`

func TestCompletion(t *testing.T) {
	for _, encoding := range []string{"utf-16", "utf-8"} {
		t.Run(encoding, func(t *testing.T) {
			_, uri := setupProject(t)
			c := startServer(t, []string{encoding}, nil)
			pos := c.open(uri, page, encoding == "utf-8")

			var list completionList
			if err := c.request("textDocument/completion", at(uri, pos), &list); err != nil {
				t.Fatal(err)
			}
			var labels []string
			for _, item := range list.Items {
				labels = append(labels, item.Label+" ("+item.Detail+")")
				if r := item.TextEdit.Range; r.End != pos || r.Start.Line != pos.Line || r.Start.Character != pos.Character-2 {
					t.Errorf("%s replaces %+v, want the 2 characters before %+v", item.Label, r, pos)
				}
			}
			want := "card-title (style.css), button (style.css), inline (<style>)"
			if got := strings.Join(labels, ", "); got != want {
				t.Errorf("completions = %s, want %s", got, want)
			}
		})
	}
}

func TestCompletionOutsideClassAttribute(t *testing.T) {
	_, uri := setupProject(t)
	c := startServer(t, nil, nil)
	pos := c.open(uri, `<link rel="stylesheet" href="style.css"><div id="|">`, false)
	if got := c.complete(uri, pos); len(got) != 0 {
		t.Errorf("got completions %q outside a class attribute, want none", got)
	}
}

func TestDefinition(t *testing.T) {
	dir, uri := setupProject(t)
	c := startServer(t, nil, nil)
	pos := c.open(uri, `<link rel="stylesheet" href="style.css"><p>Größe</p><div class="button ca|rd">`, false)

	want := location{URI: pathToURI(filepath.Join(dir, "style.css")), Range: span{position{1, 0}, position{1, 5}}}
	if got := c.define(uri, pos); len(got) != 1 || got[0] != want {
		t.Errorf("definition = %+v, want %+v", got, want)
	}
}

func TestDefinitionOutsideClassName(t *testing.T) {
	_, uri := setupProject(t)
	c := startServer(t, nil, nil)
	for _, text := range []string{
		`<link rel="stylesheet" href="style.css"><div id="ca|rd">`,
		`<link rel="stylesheet" href="style.css"><div class="card | button">`,
	} {
		if got := c.define(uri, c.open(uri, text, false)); len(got) != 0 {
			t.Errorf("definition in %q = %+v, want none", text, got)
		}
	}
}

func TestDocumentLifecycle(t *testing.T) {
	_, uri := setupProject(t)
	c := startServer(t, nil, nil)
	pos := c.open(uri, `<div class="|">`, false)
	if got := c.complete(uri, pos); len(got) != 0 {
		t.Fatalf("completions without a stylesheet = %q, want none", got)
	}

	// An empty change list leaves the document alone, the last full text wins.
	c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri}, "contentChanges": []any{}})
	c.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": uri},
		"contentChanges": []any{map[string]any{"text": "stale"}, map[string]any{"text": "<div class=\"\">\n<link rel=\"stylesheet\" href=\"style.css\">"}},
	})
	if got := c.complete(uri, pos); len(got) != 3 {
		t.Errorf("completions after the change = %q, want all 3 classes", got)
	}

	c.notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uri}})
	if got := c.complete(uri, pos); len(got) != 0 {
		t.Errorf("completions after closing = %q, want none", got)
	}
	if got := c.define(uri, pos); len(got) != 0 {
		t.Errorf("definition after closing = %+v, want none", got)
	}
}

func TestDiagnostics(t *testing.T) {
	for _, encoding := range []string{"utf-16", "utf-8"} {
		t.Run(encoding, func(t *testing.T) {
			_, uri := setupProject(t)
			c := startServer(t, []string{encoding}, nil)
			pos := c.open(uri, `<link rel="stylesheet" href="style.css">
<style>.inline {}</style>
<p>Größe 🙂</p><div class="card |crad inline"></div>`, encoding == "utf-8")

			want := []diagnostic{{
				Range:    span{pos, position{pos.Line, pos.Character + 4}},
				Severity: severityWarning,
				Source:   "html-class-lsp",
				Message:  `class "crad" isn't defined in the stylesheets this page loads`,
			}}
			if got := c.diagnostics(uri); !slices.Equal(got, want) {
				t.Errorf("diagnostics = %+v, want %+v", got, want)
			}
		})
	}
}

func TestDiagnosticsLifecycle(t *testing.T) {
	_, uri := setupProject(t)
	c := startServer(t, nil, nil)
	change := func(text string) {
		c.notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri},
			"contentChanges": []any{map[string]any{"text": text}},
		})
	}
	link := `<link rel="stylesheet" href="style.css">`

	c.open(uri, link+`<div class="nope">`, false)
	if got := c.diagnostics(uri); len(got) != 1 {
		t.Fatalf("diagnostics = %+v, want one for nope", got)
	}
	change(link + `<div class="card">`)
	if got := c.diagnostics(uri); len(got) != 0 {
		t.Errorf("diagnostics after the fix = %+v, want none", got)
	}

	// Without all of the page's CSS, or any, there's nothing to go by.
	change(link + `<link rel="stylesheet" href="https://cdn.example.com/x.css"><div class="nope">`)
	if got := c.diagnostics(uri); len(got) != 0 {
		t.Errorf("diagnostics with a remote stylesheet = %+v, want none", got)
	}
	change(`<div class="nope">`)
	if got := c.diagnostics(uri); len(got) != 0 {
		t.Errorf("diagnostics without stylesheets = %+v, want none", got)
	}

	change(link + `<div class="nope">`)
	c.notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uri}})
	if got := c.diagnostics(uri); len(got) != 0 {
		t.Errorf("diagnostics after closing = %+v, want them cleared", got)
	}
}

func TestWatchedStylesheets(t *testing.T) {
	dir, uri := setupProject(t)
	c := startServer(t, nil, map[string]any{"capabilities": map[string]any{
		"workspace": map[string]any{"didChangeWatchedFiles": map[string]any{"dynamicRegistration": true}},
	}})
	registrations := c.messages("client/registerCapability")
	if len(registrations) != 1 || !strings.Contains(string(registrations[0]), `"globPattern":"**/*.css"`) {
		t.Fatalf("registrations = %s, want one that watches CSS files", registrations)
	}
	// The client's answer to the registration gets no response.
	c.send(map[string]any{"id": "watch", "result": nil})

	c.open(uri, `<link rel="stylesheet" href="style.css"><div class="card fresh">`, false)
	if got := c.diagnostics(uri); len(got) != 1 {
		t.Fatalf("diagnostics = %+v, want one for fresh", got)
	}
	css := filepath.Join(dir, "style.css")
	if err := os.WriteFile(css, []byte(".card {} .fresh {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.notify("workspace/didChangeWatchedFiles", map[string]any{
		"changes": []any{map[string]any{"uri": pathToURI(css), "type": 2}},
	})
	if got := c.diagnostics(uri); len(got) != 0 {
		t.Errorf("diagnostics after defining fresh = %+v, want none", got)
	}
}

func TestNoWatchWithoutDynamicRegistration(t *testing.T) {
	c := startServer(t, nil, nil)
	if got := c.messages("client/registerCapability"); len(got) != 0 {
		t.Errorf("registrations = %s, want none", got)
	}
}

func TestRootRelativeHref(t *testing.T) {
	dir, _ := setupProject(t)
	uri := pathToURI(filepath.Join(dir, "sub", "page.html"))
	text := `<link rel="stylesheet" href="/style.css"><div class="but|">`
	for name, params := range map[string]map[string]any{
		"rootUri":          {"rootUri": pathToURI(dir)},
		"workspaceFolders": {"rootUri": pathToURI(filepath.Join(dir, "sub")), "workspaceFolders": []any{map[string]any{"uri": pathToURI(dir)}}},
	} {
		t.Run(name, func(t *testing.T) {
			c := startServer(t, nil, params)
			if got := c.complete(uri, c.open(uri, text, false)); len(got) != 3 {
				t.Errorf("completions = %q, want the 3 classes from /style.css", got)
			}
		})
	}
}

func TestInvalidParams(t *testing.T) {
	_, uri := setupProject(t)
	c := startServer(t, nil, nil)
	c.notify("textDocument/didOpen", 5) // ignored, it's a notification
	if err := c.request("textDocument/completion", 5, nil); err == nil || err.Code != -32602 {
		t.Errorf("completion with invalid params: error %v, want InvalidParams", err)
	}
	if got := c.complete(uri, c.open(uri, `<link rel="stylesheet" href="style.css"><div class="|">`, false)); len(got) != 3 {
		t.Errorf("completions after invalid params = %q, want the server to keep working", got)
	}
}

func TestUnknownMethod(t *testing.T) {
	c := startServer(t, nil, nil)
	if err := c.request("textDocument/hover", map[string]any{}, nil); err == nil || err.Code != -32601 {
		t.Errorf("hover error = %v, want MethodNotFound", err)
	}
	if _, err := newServer().handle("bogus", nil); err == nil || err.Error() != "method not found: bogus" {
		t.Errorf("handle(bogus) error = %v", err)
	}
}

func TestShutdownAndExit(t *testing.T) {
	c := startServer(t, nil, nil)
	if err := c.request("shutdown", nil, nil); err != nil {
		t.Fatal(err)
	}
	c.notify("exit", nil)
	if err := <-c.done; err != nil {
		t.Errorf("serve returned %v after shutdown and exit, want nil", err)
	}
}

func frame(body string) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
}

func TestServeErrors(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"end of input", "", ""},
		{"end of input in headers", "Content-Length: 2\r\n", ""},
		{"header case and spacing", "content-length:2\r\n\r\n{}" + frame(`{"method":"shutdown","id":1}`) + frame(`{"method":"exit"}`), ""},
		{"exit without shutdown", frame(`{"method":"exit"}`), "exit without shutdown"},
		{"invalid length", "Content-Length: many\r\n\r\n", `invalid Content-Length "many"`},
		{"missing length", "Content-Type: x\r\n\r\n", "missing Content-Length header"},
		{"short body", "Content-Length: 10\r\n\r\n{}", "unexpected EOF"},
		{"invalid JSON", frame("{"), "invalid message"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := newServer().serve(strings.NewReader(tt.input), io.Discard)
			if tt.want == "" {
				if err != nil {
					t.Errorf("serve error = %v, want none", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("serve error = %v, want %q", err, tt.want)
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestServeWriteError(t *testing.T) {
	for name, msg := range map[string]string{
		"response":     `{"id":1,"method":"shutdown"}`,
		"notification": `{"method":"textDocument/didOpen","params":{"textDocument":{"uri":"file:///x.html","text":""}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			err := newServer().serve(strings.NewReader(frame(msg)), failingWriter{})
			if err == nil || err.Error() != "disk full" {
				t.Errorf("serve error = %v, want the write error", err)
			}
		})
	}
}

func TestWriteMessageMarshalError(t *testing.T) {
	if err := writeMessage(io.Discard, make(chan int)); err == nil {
		t.Error("writeMessage of a channel succeeded, want an error")
	}
}

func TestOffsetAndPosition(t *testing.T) {
	text := "ab\nGrö🙂x\n"
	tests := []struct {
		utf8   bool
		pos    position
		offset int
	}{
		{false, position{1, 3}, 7},  // after "Grö"
		{false, position{1, 5}, 11}, // after the emoji, two UTF-16 units
		{true, position{1, 4}, 7},   // after "Grö", ö is two bytes
		{true, position{1, 8}, 11},  // after the emoji, four bytes
		{false, position{2, 0}, 13}, // empty last line
	}
	for _, tt := range tests {
		s := &server{utf8: tt.utf8}
		if got := s.offset(text, tt.pos); got != tt.offset {
			t.Errorf("utf8=%v: offset(%+v) = %d, want %d", tt.utf8, tt.pos, got, tt.offset)
		}
		if got := s.position(text, tt.offset); got != tt.pos {
			t.Errorf("utf8=%v: position(%d) = %+v, want %+v", tt.utf8, tt.offset, got, tt.pos)
		}
	}

	// Positions past the end of a line or the text are clamped.
	for _, tt := range []struct {
		utf8   bool
		pos    position
		offset int
	}{
		{false, position{0, 99}, 2},
		{true, position{0, 99}, 2},
		{false, position{9, 0}, len(text)},
	} {
		if got := (&server{utf8: tt.utf8}).offset(text, tt.pos); got != tt.offset {
			t.Errorf("utf8=%v: offset(%+v) = %d, want %d", tt.utf8, tt.pos, got, tt.offset)
		}
	}
}
