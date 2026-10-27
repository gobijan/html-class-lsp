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
	scriptOrStyle = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>|<style\b[^>]*>.*?</style>`)

	// Font services serve nothing but @font-face rules, except for icon
	// fonts, which come with classes like .material-icons.
	fontService = regexp.MustCompile(`(?i)^(?:https?:)?//fonts\.(?:googleapis\.com|bunny\.net)/`)
	iconFont    = regexp.MustCompile(`(?i)icon|symbol`)

	// Scripts that generate styles in the browser, like the Tailwind CDN.
	styleScript = regexp.MustCompile(`(?i)<script\b[^>]*\bsrc\s*=\s*["']?[^"'\s>]*(?:tailwind|unocss|twind)`)

	// The value of a class attribute up to the end of the text, i.e. the
	// cursor. The leading group stands in for a lookbehind, which RE2 lacks,
	// so that data-class="…" doesn't count.
	classAttr = regexp.MustCompile(`(?i)(?:^|[^\w-])class\s*=\s*(?:"([^"]*)|'([^']*))$`)

	// A whole class attribute. Bound ones like :class="…" in Vue or Alpine
	// hold an expression rather than class names, so they don't count.
	classAttrs = regexp.MustCompile(`(?i)(?:^|[^\w:-])class\s*=\s*(?:"([^"]*)"|'([^']*)')`)
)

// stylesheets returns the stylesheets that apply to an HTML document: local
// files from <link rel="stylesheet">, the files those @import, and the
// document's own <style> blocks. Root-relative hrefs ("/css/site.css") are
// resolved against root, or the document's directory if root is empty.
//
// complete reports whether that's all the CSS the page gets. It isn't when a
// stylesheet is remote or can't be read, or when a script like the Tailwind
// CDN generates styles in the browser.
func stylesheets(uri, html, root string) (sheets []stylesheet, complete bool) {
	html = blankMatches(html, htmlComment)

	// Without a file path (an unsaved buffer) there's nothing to resolve
	// links and imports against, only the <style> blocks count.
	dir := ""
	if path := uriToPath(uri); path != "" {
		dir = filepath.Dir(path)
	}
	if root == "" {
		root = dir
	}
	c := &collector{root: root, seen: map[string]bool{}, complete: !styleScript.MatchString(html)}

	for _, tag := range linkTag.FindAllString(html, -1) {
		if !relStylesheet.MatchString(tag) {
			continue
		}
		if m := hrefAttr.FindStringSubmatch(tag); m != nil {
			c.add(strings.Join(m[1:], ""), dir)
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
		c.sheets = append(c.sheets, stylesheet{uri: uri, label: "<style>", text: inline})
		c.imports(inline, dir)
	}
	return c.sheets, c.complete
}

// collector gathers the stylesheets of a document.
type collector struct {
	root     string
	sheets   []stylesheet
	seen     map[string]bool
	complete bool
}

// add reads the stylesheet that ref points to from dir, and the files it
// imports.
func (c *collector) add(ref, dir string) {
	file, ok := resolve(ref, dir, c.root)
	if !ok {
		c.complete = c.complete && fontService.MatchString(ref) && !iconFont.MatchString(ref)
		return
	}
	if c.seen[file] {
		return
	}
	c.seen[file] = true
	data, err := os.ReadFile(file)
	if err != nil {
		c.complete = false
		return
	}
	text := string(data)
	c.sheets = append(c.sheets, stylesheet{uri: pathToURI(file), label: filepath.Base(file), text: text})
	c.imports(text, filepath.Dir(file))
}

// imports adds the files that css pulls in with @import, resolving them
// against dir.
func (c *collector) imports(css, dir string) {
	for _, m := range cssImport.FindAllStringSubmatch(mask(css, false), -1) {
		c.add(m[1], dir)
	}
}

// resolve turns a stylesheet reference into a local file path. Remote and
// data: URLs can't be read and report false, and so does everything when
// there's no directory to resolve against.
func resolve(ref, dir, root string) (string, bool) {
	if ref == "" || dir == "" || remoteRef.MatchString(ref) {
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

// classNames returns the class names in the class attributes of html, in
// source order. Only markup counts, not comments, scripts or <style> blocks.
// A value with template tags like {{ … }}, {% … %} or <?= … ?> is skipped
// entirely, its classes are only known once the page is rendered.
func classNames(html string) []class {
	html = blankMatches(blankMatches(html, htmlComment), scriptOrStyle)
	var found []class
	for _, m := range classAttrs.FindAllStringSubmatchIndex(html, -1) {
		start, end := m[2], m[3]
		if start < 0 {
			start, end = m[4], m[5]
		}
		if strings.ContainsAny(html[start:end], "{<") {
			continue
		}
		for i := start; i < end; i++ {
			j := i
			for j < end && !isSeparator(html[j]) {
				j++
			}
			if j > i {
				found = append(found, class{name: html[i:j], start: i, end: j})
			}
			i = j
		}
	}
	return found
}

// blankMatches blanks out every match of re in s, keeping offsets the same.
func blankMatches(s string, re *regexp.Regexp) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		b := []byte(m)
		blank(b, 0, len(b))
		return string(b)
	})
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
