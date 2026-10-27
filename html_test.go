package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestClassValueBefore(t *testing.T) {
	tests := []struct {
		text      string
		want      string
		wantFound bool
	}{
		{`<div class="`, "", true},
		{`<div class="card ti`, "card ti", true},
		{`<div class='card `, "card ", true},
		{"<div\n  class = \"a\n  b", "a\n  b", true},
		{`<div CLASS="x`, "x", true},
		{`class="x`, "x", true},
		{`<div class="done">`, "", false},
		{`<div data-class="x`, "", false},
		{`<div subclass="x`, "", false},
		{`<a href="x`, "", false},
		{`<div class="a" id="x`, "", false},
	}
	for _, tt := range tests {
		got, found := classValueBefore(tt.text, len(tt.text))
		if got != tt.want || found != tt.wantFound {
			t.Errorf("classValueBefore(%q) = %q, %v; want %q, %v", tt.text, got, found, tt.want, tt.wantFound)
		}
	}
}

func TestStylesheets(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("css/main.css", `@import "parts/extra.css"; @import url(https://cdn.example.com/x.css); /* @import "gone.css"; */ .main {}`)
	write("css/parts/extra.css", `@import url(../main.css); .extra {}`) // cycle
	write("css/gone.css", ".gone {}")
	write("root.css", ".root {}")
	write("my styles.css", ".spaced {}")
	write("preload.css", ".preloaded {}")
	write("commented.css", ".commented {}")
	write("from-style.css", ".from-style {}")
	write("page/index.html", "")

	html := `<html><head>
<link href='../css/main.css?v=3' rel=stylesheet>
<link rel="stylesheet" href="/root.css">
<link rel="stylesheet" href="../my%20styles.css">
<link rel="stylesheet" href="https://cdn.example.com/remote.css">
<link rel="stylesheet" href="//cdn.example.com/remote.css">
<link rel="stylesheet" href="../missing.css">
<link rel="stylesheet">
<link rel="preload" href="../preload.css" as="style">
<!-- <link rel="stylesheet" href="../commented.css"> -->
<style>@import url("../from-style.css"); /* @import "../gone.css"; */ .inline {}</style>
</head></html>`

	uri := pathToURI(filepath.Join(dir, "page/index.html"))
	var labels, names []string
	sheets, complete := stylesheets(uri, html, dir)
	if complete {
		t.Error("complete = true with remote and missing stylesheets, want false")
	}
	for _, sheet := range sheets {
		labels = append(labels, sheet.label)
		for _, c := range classes(sheet.text) {
			names = append(names, c.name)
		}
	}
	wantLabels := []string{"main.css", "extra.css", "root.css", "my styles.css", "<style>", "from-style.css"}
	if !slices.Equal(labels, wantLabels) {
		t.Errorf("stylesheets = %q, want %q", labels, wantLabels)
	}
	wantNames := []string{"main", "extra", "root", "spaced", "inline", "from-style"}
	if !slices.Equal(names, wantNames) {
		t.Errorf("classes = %q, want %q", names, wantNames)
	}
}

func TestStylesheetsWithoutFile(t *testing.T) {
	sheets, complete := stylesheets("untitled:1", `<link rel="stylesheet" href="x.css"><style>@import "x.css"; .a {}</style>`, "")
	if len(sheets) != 1 || sheets[0].label != "<style>" {
		t.Errorf("got %+v, want only the <style> block", sheets)
	}
	if complete {
		t.Error("complete = true with links that can't be resolved, want false")
	}
	if _, complete := stylesheets("untitled:1", `<style>.a {}</style>`, ""); !complete {
		t.Error("complete = false with only a <style> block, want true")
	}
}

func TestStylesheetsComplete(t *testing.T) {
	dir := t.TempDir()
	for name, css := range map[string]string{
		"style.css":  ".a {}",
		"remote.css": `@import url("https://cdn.example.com/x.css"); .a {}`,
		"fonts.css":  `@import url("https://fonts.googleapis.com/css2?family=Inter"); .a {}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(css), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		html string
		want bool
	}{
		{``, true},
		{`<link rel="stylesheet" href="style.css">`, true},
		{`<link rel="stylesheet" href="missing.css">`, false},
		{`<link rel="stylesheet" href="https://cdn.example.com/x.css">`, false},
		{`<link rel="stylesheet" href="remote.css">`, false},
		{`<style>@import "missing.css";</style>`, false},
		{`<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Inter&display=swap">`, true},
		{`<style>@import url(//fonts.bunny.net/css?family=inter);</style>`, true},
		{`<link rel="stylesheet" href="fonts.css">`, true},
		{`<link rel="stylesheet" href="https://fonts.googleapis.com/icon?family=Material+Icons">`, false},
		{`<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Material+Symbols+Outlined">`, false},
		{`<script src="https://cdn.tailwindcss.com"></script>`, false},
		{`<script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>`, false},
		{`<script defer src=/js/unocss-runtime.js></script>`, false},
		{`<!-- <script src="https://cdn.tailwindcss.com"></script> -->`, true},
		{`<script src="https://unpkg.com/htmx.org@2.0.4"></script>`, true},
	}
	uri := pathToURI(filepath.Join(dir, "index.html"))
	for _, tt := range tests {
		if _, got := stylesheets(uri, tt.html, ""); got != tt.want {
			t.Errorf("stylesheets(%q) complete = %v, want %v", tt.html, got, tt.want)
		}
	}
}

func TestClassNames(t *testing.T) {
	tests := []struct {
		name string
		html string
		want []string
	}{
		{"quotes", `<a class="x y"><b class='z'>`, []string{"x", "y", "z"}},
		{"whitespace", "<a class=\" x\n\ty  \">", []string{"x", "y"}},
		{"case and spacing", `<a CLASS = "x">`, []string{"x"}},
		{"empty", `<a class="">`, nil},
		{"escaped characters", `<a class="sm:flex w-1.5 [&>*]:p-4">`, []string{"sm:flex", "w-1.5", "[&>*]:p-4"}},
		{"other attributes", `<a data-class="x" subclass="x" className="x" :class="{ x: on }" v-bind:class="x" x-bind:class="x">`, nil},
		{"comments", `<!-- <a class="x"> --><b class="y">`, []string{"y"}},
		{"scripts and styles", `<script>el.innerHTML = '<a class="x">'</script><style>[class="x"] {}</style><b class="y">`, []string{"y"}},
		{"templates", `<a class="{{ .Class }}"><a class="btn {% if on %}on{% endif %}"><a class="btn <?= $on ?>"><a class="btn {on}">`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, c := range classNames(tt.html) {
				got = append(got, c.name)
				if src := tt.html[c.start:c.end]; src != c.name {
					t.Errorf("class %q spans %q", c.name, src)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("classNames(%q) = %q, want %q", tt.html, got, tt.want)
			}
		})
	}
}
