package main

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf8"
)

// class is a class selector found in a stylesheet, or a class name in an
// HTML class attribute. start and end are byte offsets in the source of
// ".name" or "name", so it can be turned back into a range for
// go-to-definition and diagnostics.
type class struct {
	name       string
	start, end int
}

// classes returns every class selector in css, in source order. Only selector
// preludes are scanned, so values like `.5em` or `url(bg.png)` in declarations
// never show up, and neither do comments, strings or at-rule preludes.
func classes(css string) []class {
	css = mask(css, true)
	var found []class
	start := 0
	for i := 0; i < len(css); i++ {
		switch css[i] {
		case '{':
			found = appendSelectorClasses(found, css[start:i], start)
			start = i + 1
		case '}', ';':
			start = i + 1
		}
	}
	return found
}

func appendSelectorClasses(found []class, prelude string, base int) []class {
	if strings.HasPrefix(strings.TrimSpace(prelude), "@") {
		return found
	}
	for i := 0; i < len(prelude); i++ {
		if prelude[i] != '.' {
			continue
		}
		if name, n := ident(prelude[i+1:]); n > 0 {
			found = append(found, class{name: name, start: base + i, end: base + i + 1 + n})
			i += n
		}
	}
	return found
}

// mask blanks out comments, and strings too if withStrings is set. Blanked
// bytes become spaces and newlines are kept, so offsets and line numbers stay
// the same as in the original.
func mask(css string, withStrings bool) string {
	b := []byte(css)
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			end := len(b)
			if j := bytes.Index(b[i+2:], []byte("*/")); j >= 0 {
				end = i + 2 + j + 2
			}
			blank(b, i, end)
			i = end - 1
		case c == '"' || c == '\'':
			end := i + 1
			for end < len(b) && b[end] != c && b[end] != '\n' {
				if b[end] == '\\' {
					end++
				}
				end++
			}
			end = min(end+1, len(b))
			if withStrings {
				blank(b, i, end)
			}
			i = end - 1
		}
	}
	return string(b)
}

func blank(b []byte, from, to int) {
	for i := from; i < to; i++ {
		if b[i] != '\n' {
			b[i] = ' '
		}
	}
}

// ident reads a CSS identifier from the start of s and returns it with escapes
// resolved (`sm\:flex` becomes "sm:flex"), along with the number of bytes it
// spans. It returns 0 if s doesn't start with an identifier.
func ident(s string) (string, int) {
	if !identStart(s) {
		return "", 0
	}
	var name strings.Builder
	i := 0
	for i < len(s) {
		switch c := s[i]; {
		case c == '\\':
			r, n := escape(s[i:])
			if n == 0 {
				return name.String(), i
			}
			name.WriteRune(r)
			i += n
		case isNameByte(c):
			name.WriteByte(c)
			i++
		default:
			return name.String(), i
		}
	}
	return name.String(), i
}

func identStart(s string) bool {
	if strings.HasPrefix(s, "-") {
		s = s[1:]
		if strings.HasPrefix(s, "-") {
			return true
		}
	}
	if s == "" {
		return false
	}
	c := s[0]
	return c == '_' || c >= 0x80 || isLetter(c) || (c == '\\' && len(s) > 1 && s[1] != '\n')
}

// escape decodes the CSS escape at the start of s (which begins with a
// backslash) and returns the rune and the number of bytes it spans.
func escape(s string) (rune, int) {
	if len(s) < 2 || s[1] == '\n' {
		return 0, 0
	}
	n := 1
	for n < len(s) && n <= 6 && isHex(s[n]) {
		n++
	}
	if n == 1 {
		r, size := utf8.DecodeRuneInString(s[1:])
		return r, 1 + size
	}
	code, _ := strconv.ParseUint(s[1:n], 16, 32)
	if n < len(s) && (s[n] == ' ' || s[n] == '\t' || s[n] == '\n') {
		n++
	}
	r := rune(code)
	if r == 0 || !utf8.ValidRune(r) {
		r = utf8.RuneError
	}
	return r, n
}

func isLetter(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }
func isHex(c byte) bool    { return c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'f' }
func isNameByte(c byte) bool {
	return isLetter(c) || c >= '0' && c <= '9' || c == '_' || c == '-' || c >= 0x80
}
