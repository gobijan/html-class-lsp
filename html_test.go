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
	for _, sheet := range stylesheets(uri, html, dir) {
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
	sheets := stylesheets("untitled:1", `<link rel="stylesheet" href="x.css"><style>@import "x.css"; .a {}</style>`, "")
	if len(sheets) != 1 || sheets[0].label != "<style>" {
		t.Errorf("got %+v, want only the <style> block", sheets)
	}
}
