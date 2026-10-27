package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

const (
	kindClass       = 7 // CompletionItemKind.Class
	severityWarning = 2 // DiagnosticSeverity.Warning
)

type server struct {
	docs     map[string]string // open documents by URI
	root     string            // workspace root, for root-relative hrefs
	utf8     bool              // positions count bytes instead of UTF-16 units
	watch    bool              // the client can watch files for us
	outbox   []message         // for the client, sent after the current message
	shutdown bool
}

func newServer() *server {
	return &server{docs: map[string]string{}}
}

type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type span struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

type location struct {
	URI   string `json:"uri"`
	Range span   `json:"range"`
}

type textEdit struct {
	Range   span   `json:"range"`
	NewText string `json:"newText"`
}

type completionItem struct {
	Label    string   `json:"label"`
	Kind     int      `json:"kind"`
	Detail   string   `json:"detail,omitempty"`
	TextEdit textEdit `json:"textEdit"`
}

type completionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []completionItem `json:"items"`
}

type diagnostic struct {
	Range    span   `json:"range"`
	Severity int    `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

type publishParams struct {
	URI         string       `json:"uri"`
	Diagnostics []diagnostic `json:"diagnostics"`
}

type textDocument struct {
	URI  string `json:"uri"`
	Text string `json:"text"`
}

type documentParams struct {
	TextDocument textDocument `json:"textDocument"`
}

type changeParams struct {
	TextDocument   textDocument `json:"textDocument"`
	ContentChanges []struct {
		Text string `json:"text"`
	} `json:"contentChanges"`
}

type positionParams struct {
	TextDocument textDocument `json:"textDocument"`
	Position     position     `json:"position"`
}

type initializeParams struct {
	RootURI          string `json:"rootUri"`
	WorkspaceFolders []struct {
		URI string `json:"uri"`
	} `json:"workspaceFolders"`
	Capabilities struct {
		General struct {
			PositionEncodings []string `json:"positionEncodings"`
		} `json:"general"`
		Workspace struct {
			DidChangeWatchedFiles struct {
				DynamicRegistration bool `json:"dynamicRegistration"`
			} `json:"didChangeWatchedFiles"`
		} `json:"workspace"`
	} `json:"capabilities"`
}

type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

type errorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

// message is a request or notification from the server to the client.
type message struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// serve answers LSP messages from r on w until the client sends exit or
// closes the stream.
func (s *server) serve(r io.Reader, w io.Writer) error {
	in := bufio.NewReader(r)
	for {
		body, err := readMessage(in)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			return fmt.Errorf("invalid message: %w", err)
		}
		if req.Method == "exit" {
			if !s.shutdown {
				return errors.New("exit without shutdown")
			}
			return nil
		}
		if req.Method == "" {
			continue // the client's response to one of our requests
		}

		result, err := s.handle(req.Method, req.Params)
		if req.ID != nil {
			if err := writeMessage(w, reply(req.ID, result, err)); err != nil {
				return err
			}
		}
		for _, m := range s.outbox {
			if err := writeMessage(w, m); err != nil {
				return err
			}
		}
		s.outbox = nil
	}
}

// reply is the response to the request with the given id.
func reply(id json.RawMessage, result any, err error) any {
	if err == nil {
		return response{JSONRPC: "2.0", ID: id, Result: result}
	}
	var rerr *rpcError
	if !errors.As(err, &rerr) {
		rerr = &rpcError{Code: -32602, Message: err.Error()} // InvalidParams
	}
	return errorResponse{JSONRPC: "2.0", ID: id, Error: rerr}
}

// send queues a message for the client, a request if id is set and a
// notification otherwise.
func (s *server) send(id any, method string, params any) {
	s.outbox = append(s.outbox, message{JSONRPC: "2.0", ID: id, Method: method, Params: params})
}

func (s *server) handle(method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		return call(params, s.initialize)
	case "initialized":
		s.initialized()
		return nil, nil
	case "workspace/didChangeWatchedFiles":
		// A stylesheet changed, which can change the diagnostics of any
		// open document.
		for uri := range s.docs {
			s.publish(uri)
		}
		return nil, nil
	case "textDocument/didOpen":
		return call(params, s.didOpen)
	case "textDocument/didChange":
		return call(params, s.didChange)
	case "textDocument/didClose":
		return call(params, s.didClose)
	case "textDocument/completion":
		return call(params, s.completion)
	case "textDocument/definition":
		return call(params, s.definition)
	case "shutdown":
		s.shutdown = true
		return nil, nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not found: " + method}
}

// call decodes params for the handler f and returns its result.
func call[P, R any](params json.RawMessage, f func(P) R) (any, error) {
	var p P
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	return f(p), nil
}

func (s *server) initialize(p initializeParams) any {
	root := p.RootURI
	if len(p.WorkspaceFolders) > 0 {
		root = p.WorkspaceFolders[0].URI
	}
	s.root = uriToPath(root)
	s.utf8 = slices.Contains(p.Capabilities.General.PositionEncodings, "utf-8")
	s.watch = p.Capabilities.Workspace.DidChangeWatchedFiles.DynamicRegistration
	encoding := "utf-16"
	if s.utf8 {
		encoding = "utf-8"
	}
	return map[string]any{
		"capabilities": map[string]any{
			"positionEncoding":   encoding,
			"textDocumentSync":   1, // full document on every change
			"completionProvider": map[string]any{"triggerCharacters": []string{`"`, "'", " "}},
			"definitionProvider": true,
		},
		"serverInfo": map[string]string{"name": "html-class-lsp", "version": version()},
	}
}

// initialized asks the client to report changed stylesheets, since classes
// added to or removed from them change the diagnostics of open documents.
func (s *server) initialized() {
	if !s.watch {
		return
	}
	s.send("watch", "client/registerCapability", map[string]any{
		"registrations": []any{map[string]any{
			"id":     "css",
			"method": "workspace/didChangeWatchedFiles",
			"registerOptions": map[string]any{
				"watchers": []any{map[string]any{"globPattern": "**/*.css"}},
			},
		}},
	})
}

func (s *server) didOpen(p documentParams) any {
	s.docs[p.TextDocument.URI] = p.TextDocument.Text
	s.publish(p.TextDocument.URI)
	return nil
}

func (s *server) didChange(p changeParams) any {
	if n := len(p.ContentChanges); n > 0 {
		s.docs[p.TextDocument.URI] = p.ContentChanges[n-1].Text
		s.publish(p.TextDocument.URI)
	}
	return nil
}

func (s *server) didClose(p documentParams) any {
	delete(s.docs, p.TextDocument.URI)
	s.send(nil, "textDocument/publishDiagnostics", publishParams{URI: p.TextDocument.URI, Diagnostics: []diagnostic{}})
	return nil
}

// publish sends the client the diagnostics of an open document.
func (s *server) publish(uri string) {
	s.send(nil, "textDocument/publishDiagnostics", publishParams{URI: uri, Diagnostics: s.diagnostics(uri, s.docs[uri])})
}

// diagnostics warns about the class names in a document that none of its
// stylesheets define. When it can't see all of the page's CSS, or there is
// none at all, there's nothing to go by and it reports nothing.
func (s *server) diagnostics(uri, text string) []diagnostic {
	found := []diagnostic{}
	sheets, complete := stylesheets(uri, text, s.root)
	if !complete || len(sheets) == 0 {
		return found
	}
	defined := map[string]bool{}
	for _, sheet := range sheets {
		for _, c := range classes(sheet.text) {
			defined[c.name] = true
		}
	}
	for _, c := range classNames(text) {
		if defined[c.name] {
			continue
		}
		found = append(found, diagnostic{
			Range:    span{s.position(text, c.start), s.position(text, c.end)},
			Severity: severityWarning,
			Source:   "html-class-lsp",
			Message:  fmt.Sprintf("class %q isn't defined in the stylesheets this page loads", c.name),
		})
	}
	return found
}

func (s *server) completion(p positionParams) completionList {
	list := completionList{Items: []completionItem{}}
	text, ok := s.docs[p.TextDocument.URI]
	if !ok {
		return list
	}
	offset := s.offset(text, p.Position)
	value, ok := classValueBefore(text, offset)
	if !ok {
		return list
	}

	// Class names are separated by whitespace, so the one being typed is
	// everything after the last whitespace. Those before it aren't offered
	// again.
	partial := value[strings.LastIndexAny(value, " \t\r\n\f")+1:]
	present := map[string]bool{}
	for _, name := range strings.Fields(value[:len(value)-len(partial)]) {
		present[name] = true
	}
	replace := span{s.position(text, offset-len(partial)), s.position(text, offset)}

	sheets, _ := stylesheets(p.TextDocument.URI, text, s.root)
	for _, sheet := range sheets {
		for _, c := range classes(sheet.text) {
			if present[c.name] {
				continue
			}
			present[c.name] = true
			list.Items = append(list.Items, completionItem{
				Label:    c.name,
				Kind:     kindClass,
				Detail:   sheet.label,
				TextEdit: textEdit{Range: replace, NewText: c.name},
			})
		}
	}
	return list
}

func (s *server) definition(p positionParams) []location {
	locations := []location{}
	text, ok := s.docs[p.TextDocument.URI]
	if !ok {
		return locations
	}
	start := s.offset(text, p.Position)
	end := start
	for start > 0 && !isSeparator(text[start-1]) {
		start--
	}
	for end < len(text) && !isSeparator(text[end]) {
		end++
	}
	name := text[start:end]
	if _, ok := classValueBefore(text, start); !ok || name == "" {
		return locations
	}

	sheets, _ := stylesheets(p.TextDocument.URI, text, s.root)
	for _, sheet := range sheets {
		for _, c := range classes(sheet.text) {
			if c.name == name {
				locations = append(locations, location{
					URI:   sheet.uri,
					Range: span{s.position(sheet.text, c.start), s.position(sheet.text, c.end)},
				})
			}
		}
	}
	return locations
}

func isSeparator(c byte) bool {
	return c == '"' || c == '\'' || c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f'
}

// offset converts an LSP position into a byte offset in text, clamped to the
// end of the line.
func (s *server) offset(text string, p position) int {
	start := 0
	for range p.Line {
		i := strings.IndexByte(text[start:], '\n')
		if i < 0 {
			return len(text)
		}
		start += i + 1
	}
	line := text[start:]
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if s.utf8 {
		return start + min(p.Character, len(line))
	}
	units := 0
	for i, r := range line {
		if units >= p.Character {
			return start + i
		}
		units += utf16.RuneLen(r)
	}
	return start + len(line)
}

// position converts a byte offset in text into an LSP position.
func (s *server) position(text string, offset int) position {
	before := text[:offset]
	lineStart := strings.LastIndexByte(before, '\n') + 1
	p := position{Line: strings.Count(before, "\n"), Character: offset - lineStart}
	if !s.utf8 {
		p.Character = 0
		for _, r := range before[lineStart:] {
			p.Character += utf16.RuneLen(r)
		}
	}
	return p
}

func readMessage(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			value = strings.TrimSpace(value)
			if length, err = strconv.Atoi(value); err != nil {
				return nil, fmt.Errorf("invalid Content-Length %q", value)
			}
		}
	}
	if length < 0 {
		return nil, errors.New("missing Content-Length header")
	}
	body := make([]byte, length)
	_, err := io.ReadFull(r, body)
	return body, err
}

func writeMessage(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}
