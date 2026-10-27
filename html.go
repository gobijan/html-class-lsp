package main

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// stylesheet is CSS that applies to a document. For <style> blocks, text is
// the whole HTML document with everything outside the blocks blanked, so
// offsets in it are offsets in the document.
type stylesheet struct {
	uri   string
	label string // shown next to completions
	text  string
}

var (
	htmlComment   = regexp.MustCompile(`(?s)<!--.*?-->`)
	linkTag       = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	relStylesheet = regexp.MustCompile(`(?i)\brel\s*=\s*["']?[^"'>]*\bstylesheet\b`)
	hrefAttr      = regexp.MustCompile(`(?i)\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	styleBlock    = regexp.MustCompile(`(?is)<style\b[^>]*>(.*?)</style>`)
	cssImport     = regexp.MustCompile(`(?i)@import\s+(?:url\(\s*)?["']?([^"')\s;]+)`)
	remoteRef     = regexp.MustCompile(`(?i)^(?:[a-z][a-z0-9+.-]*:)?//|^data:`)

	// The value of a class attribute up to the end of the text, i.e. the
	// cursor. The leading group stands in for a lookbehind, which RE2 lacks,
	// so that data-class="…" doesn't count.
	classAttr = regexp.MustCompile(`(?i)(?:^|[^\w-])class\s*=\s*(?:"([^"]*)|'([^']*))$`)
)

// stylesheets returns the stylesheets that apply to an HTML document: local
// files from <link rel="stylesheet">, the files those @import, and the
// document's own <style> blocks. Root-relative hrefs ("/css/site.css") are
// resolved against root, or the document's directory if root is empty.
func stylesheets(uri, html, root string) []stylesheet {
	html = htmlComment.ReplaceAllStringFunc(html, func(c string) string {
		b := []byte(c)
		blank(b, 0, len(b))
		return string(b)
	})

	// Without a file path (an unsaved buffer) there's nothing to resolve
	// links and imports against, only the <style> blocks count.
	path := uriToPath(uri)
	dir := filepath.Dir(path)
	if root == "" {
		root = dir
	}
	var sheets []stylesheet
	seen := map[string]bool{}

	if path != "" {
		for _, tag := range linkTag.FindAllString(html, -1) {
			if !relStylesheet.MatchString(tag) {
				continue
			}
			if m := hrefAttr.FindStringSubmatch(tag); m != nil {
				if file, ok := resolve(strings.Join(m[1:], ""), dir, root); ok {
					sheets = readSheet(sheets, seen, file, root)
				}
			}
		}
	}

	if blocks := styleBlock.FindAllStringSubmatchIndex(html, -1); blocks != nil {
		b := []byte(html)
		last := 0
		for _, m := range blocks {
			blank(b, last, m[2])
			last = m[3]
		}
		blank(b, last, len(b))
		inline := string(b)
		sheets = append(sheets, stylesheet{uri: uri, label: "<style>", text: inline})
		if path != "" {
			sheets = readImports(sheets, seen, inline, dir, root)
		}
	}
	return sheets
}

func readSheet(sheets []stylesheet, seen map[string]bool, file, root string) []stylesheet {
	if seen[file] {
		return sheets
	}
	seen[file] = true
	data, err := os.ReadFile(file)
	if err != nil {
		return sheets
	}
	text := string(data)
	sheets = append(sheets, stylesheet{uri: pathToURI(file), label: filepath.Base(file), text: text})
	return readImports(sheets, seen, text, filepath.Dir(file), root)
}

// readImports reads the files that css pulls in with @import, resolving them
// against dir.
func readImports(sheets []stylesheet, seen map[string]bool, css, dir, root string) []stylesheet {
	for _, m := range cssImport.FindAllStringSubmatch(mask(css, false), -1) {
		if file, ok := resolve(m[1], dir, root); ok {
			sheets = readSheet(sheets, seen, file, root)
		}
	}
	return sheets
}

// resolve turns a stylesheet reference into a local file path. Remote and
// data: URLs can't be read and report false.
func resolve(ref, dir, root string) (string, bool) {
	if ref == "" || remoteRef.MatchString(ref) {
		return "", false
	}
	ref, _, _ = strings.Cut(ref, "#")
	ref, _, _ = strings.Cut(ref, "?")
	if unescaped, err := url.PathUnescape(ref); err == nil {
		ref = unescaped
	}
	if strings.HasPrefix(ref, "/") {
		return filepath.Join(root, ref), true
	}
	return filepath.Join(dir, ref), true
}

// classValueBefore reports whether offset lies inside a class attribute value,
// and returns the part of the value before it.
func classValueBefore(text string, offset int) (string, bool) {
	m := classAttr.FindStringSubmatch(text[max(0, offset-4096):offset])
	if m == nil {
		return "", false
	}
	return m[1] + m[2], true
}

func uriToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	return filepath.FromSlash(u.Path)
}

func pathToURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}
