// html-class-lsp is a language server that completes CSS class names in HTML
// class attributes, using the stylesheets the document actually links, jumps
// from a class to its definition, and warns about classes none of them
// define.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime/debug"
)

// exit is os.Exit, replaced in tests.
var exit = os.Exit

func main() {
	exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is the whole program, returning its exit code.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("html-class-lsp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	showVersion := flags.Bool("version", false, "print the version and exit")
	flags.Bool("stdio", true, "talk LSP over stdin and stdout (the only mode, accepted for compatibility)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if *showVersion {
		if _, err := fmt.Fprintln(stdout, version()); err != nil {
			return 1
		}
		return 0
	}
	if err := newServer().serve(stdin, stdout); err != nil {
		log.New(stderr, "html-class-lsp: ", 0).Println(err)
		return 1
	}
	return 0
}

func version() string {
	info, _ := debug.ReadBuildInfo()
	return moduleVersion(info)
}

// moduleVersion is the version the binary was installed with, as in
// `go install …@v0.1.0`, or "(devel)" when there is none.
func moduleVersion(info *debug.BuildInfo) string {
	if info == nil || info.Main.Version == "" {
		return "(devel)"
	}
	return info.Main.Version
}
