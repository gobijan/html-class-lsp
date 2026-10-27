# html-class-lsp

A small language server that completes CSS class names inside HTML
`class="…"` attributes, jumps from a class to the rule that defines it, and
warns about classes that nothing defines.

It only suggests classes the page actually loads: the stylesheets it links,
what those import, and its own `<style>` blocks. A class from some other
stylesheet in the project won't show up, because it wouldn't apply.

It's a single static binary with no runtime dependencies and no
configuration, and works with any LSP client.

## Features

- **Completion** of class names in `class="…"` and `class='…'`, taken from
  - local stylesheets linked with `<link rel="stylesheet" href="…">`
    (relative and root-relative paths, query strings and `%20` are handled),
  - the document's own `<style>` blocks,
  - the files any of these pull in with `@import`.
- **Go to definition** on a class name jumps to its rule, or lists all of
  them if the class is defined more than once.
- **Warnings** for class names that none of the page's stylesheets define,
  like a typo such as `crad` for `card`. They're only given when the server
  sees all of the page's CSS, so a page gets none if it loads a remote
  stylesheet (fonts from Google Fonts or Bunny Fonts are fine), a stylesheet
  that's missing, or a script that generates styles in the browser, like the
  Tailwind CDN. Neither does a page without any stylesheet. Bound attributes
  like `:class="…"` and values with template tags (`{{ … }}`, `<?= … ?>`)
  are skipped, and so are comments and scripts.
- Classes already in the attribute aren't suggested again.
- Selectors are actually scanned, not grepped: comments, strings,
  declaration values (`.5em`, `url(bg.png)`), at-rule preludes and keyframe
  selectors are ignored, while CSS nesting and escapes (`.sm\:flex` →
  `sm:flex`) work.
- Stylesheets are read from disk on every request, so a class you just added
  shows up as soon as you save the CSS file. There's no index to build.
  Clients that can watch files are asked to report changed CSS files, so the
  warnings in open documents update as well. Classes in `<style>` blocks come
  from the editor buffer and are available right away, without saving.

Remote stylesheets (CDNs) are skipped; the server never touches the network.

## Install

With Go 1.23 or newer:

```sh
go install github.com/gobijan/html-class-lsp@latest
```

Or from a checkout:

```sh
go build -o ~/.local/bin/html-class-lsp .
```

## Editor setup

Tested with Helix 25.07, Neovim 0.12 and Claude Code 2.1.311.

### Helix

In `~/.config/helix/languages.toml`:

```toml
[language-server.html-class-lsp]
command = "html-class-lsp"

[[language]]
name = "html"
language-servers = ["vscode-html-language-server", "superhtml", "html-class-lsp"]
```

Setting `language-servers` replaces Helix's default list for HTML, so keep the
servers you already use in it. `hx --health html` shows whether all of them
are found.

### Neovim (0.11+)

```lua
vim.lsp.config('html_class_lsp', {
  cmd = { 'html-class-lsp' },
  filetypes = { 'html' },
})
vim.lsp.enable('html_class_lsp')
```

### Claude Code

Claude Code doesn't complete as you type, but after every edit to an HTML
file it hands Claude the warnings, and Claude can use go to definition. This
repository is a plugin marketplace with a plugin that sets this up:

```
/plugin install html-class-lsp --marketplace gobijan/html-class-lsp
```

Before Claude Code 2.1.275, add the marketplace first with
`/plugin marketplace add gobijan/html-class-lsp`, then run
`/plugin install html-class-lsp@html-class-lsp`. The plugin only tells Claude
Code to start `html-class-lsp` for `.html` and `.htm` files, so the binary has
to be on the `PATH` of the shell you start `claude` from. From a checkout,
`claude --plugin-dir ./claude-plugin` loads it for one session.

### Other editors

Start `html-class-lsp` for HTML files and talk to it over stdin and stdout.
`--stdio` is accepted for clients that always pass it.

## Limitations

- Only stylesheets linked from the document itself are found. In templates
  where the `<link>` lives in a separate layout file, only the `<style>`
  blocks of the current file are used. If there are any, classes from the
  layout's stylesheets get warnings.
- A class that's only there for scripts or tests, with no CSS rule at all,
  gets a warning too.
- HTML is scanned with patterns rather than a full parser, so `class="` in
  plain text or inside a script also counts as a class attribute.

## Similar projects

These take classes from every stylesheet in the workspace and do a lot more,
like diagnostics, hover and find references:

- [css-lens](https://github.com/JoshuaErney/css-lens), a language server
  built for Zed
- [css-class-lsp](https://github.com/MasterTemple/css-class-lsp), a language
  server with a Neovim plugin, also aimed at JSX and Rust frameworks

Within a single editor, there's
[HTML CSS Support](https://github.com/ecmel/vscode-html-css) for VS Code and
[nvim-html-css](https://github.com/Jezda1337/nvim-html-css) for Neovim.

html-class-lsp does less on purpose: completion and go to definition for the
classes the page loads, with nothing to set up.

## License

[MIT](LICENSE)
