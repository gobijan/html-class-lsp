package main

import (
	"slices"
	"testing"
)

func TestClasses(t *testing.T) {
	tests := []struct {
		name string
		css  string
		want []string
	}{
		{"simple", ".card { color: red }", []string{"card"}},
		{"no space", ".a{}.b{}", []string{"a", "b"}},
		{"compound and lists", ".btn:hover, .x.y > :not(.z) {}", []string{"btn", "x", "y", "z"}},
		{"comments", "/* .gone {} */ .kept {}", []string{"kept"}},
		{"unterminated comment", ".kept {} /* .gone {}", []string{"kept"}},
		{"strings", `.a { content: ".gone" } [data-x=".gone"] .b {}`, []string{"a", "b"}},
		{"escaped quote in string", `.a { content: "x\".gone" } .b {}`, []string{"a", "b"}},
		{"unterminated string", ".a { content: \"x\n} .b {}", []string{"a", "b"}},
		{"declaration values", ".a { background: url(img/bg.png); margin: .5em }", []string{"a"}},
		{"last declaration without semicolon", ".a { background: url(x.png) } .b {}", []string{"a", "b"}},
		{"at-rule preludes", "@media (min-width: 40.5em) { .inside { margin: .5em } }", []string{"inside"}},
		{"keyframe selectors", "@keyframes pulse { 50.5% { opacity: .5 } }", nil},
		{"import", `@import url("x.css"); .a {}`, []string{"a"}},
		{"nesting", ".nav { color: red; &.active { color: blue } .child {} }", []string{"nav", "active", "child"}},
		{"escapes", `.sm\:flex, .w-1\.5, .\31 0 {}`, []string{"sm:flex", "w-1.5", "10"}},
		{"escaped null", `.\0 a {}`, []string{"\ufffda"}},
		{"escaped newline ends a name", ".a\\\nb {}", []string{"a"}},
		{"lone dash", ".-{}", nil},
		{"non-ascii", ".größe, .café {}", []string{"größe", "café"}},
		{"dashes", ".-neg, .--custom, .- {}", []string{"-neg", "--custom"}},
		{"numbers aren't classes", ".5col, .a {}", []string{"a"}},
		{"no rule", ".dangling", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, c := range classes(tt.css) {
				got = append(got, c.name)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("classes(%q) = %q, want %q", tt.css, got, tt.want)
			}
		})
	}
}

func TestClassOffsets(t *testing.T) {
	css := "/* x */\n.sm\\:flex {}"
	got := classes(css)
	if len(got) != 1 {
		t.Fatalf("got %d classes, want 1", len(got))
	}
	if src := css[got[0].start:got[0].end]; src != `.sm\:flex` {
		t.Errorf("class spans %q, want %q", src, `.sm\:flex`)
	}
}
