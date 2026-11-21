# Screenshot kit

Regenerates the images in `docs/screenshots/` from the real terminal UI. Nothing
here edits a picture: it drives the program in tmux and renders what the
terminal shows.

What is real and what is staged:

- Real: the binary, its layout, the wizard, the pickers, the tool steps, the
  approval dialogs and diffs, the usage strip and its fetch path.
- Staged: the model is `mock.py`, a scripted OpenAI-compatible server that
  streams slowly so a frame can be caught mid-run, and the usage figures come
  from `mitm.py`, which answers the Claude and Codex usage requests with sample
  numbers. The workspace is a tiny made-up project. The logins are sample
  tokens that never leave the machine.

## Needs

A Linux or macOS host with `tmux`, `python3` and `openssl`, a built `termixgo`,
and, for the last step, `node` and any Chromium-based browser.

## Steps

```sh
# 1. Build the binary to photograph, stamped with the version for the title bar.
go build -ldflags "-X github.com/99apps-id/termixgo/internal/version.Version=0.2.10" \
  -o /tmp/tgx-shots/termixgo-demo ./cmd/termixgo

# 2. Create the demo workspace, state and the throwaway certificates.
sh scripts/screenshots/setup_demo.sh

# 3. Drive the program. Frames land in /tmp/tgx-shots/frames/*.ansi.
TERMIXGO_BIN=/tmp/tgx-shots/termixgo-demo python3 scripts/screenshots/drive.py
```

`setup_demo.sh` works under `/tmp/tgx-shots`, `/tmp/demo` and `/tmp/demo-new`,
and replaces whatever is there. It never touches your own `~/.termixgo`: the
demo runs with its own `HOME` and `TERMIXGO_HOME` and a clean environment (only
a sample key for the scripted model), and the proxy refuses every outgoing
connection except the two usage hosts.

## Rendering

`ansi2html.js` turns a frame into a terminal-window page; a browser turns the
page into a PNG:

```sh
node scripts/screenshots/ansi2html.js frames/04-welcome.ansi render/04-welcome.html termixgo 118
chromium --headless --force-device-scale-factor=1.5 --hide-scrollbars \
  --window-size=<width>,<height> --screenshot=docs/screenshots/04-welcome.png \
  file://$PWD/render/04-welcome.html
```

The script prints the window size to use. Put the JetBrains Mono `woff2` files
(SIL OFL) in `render/fonts/` to match the published pictures; without them the
page falls back to a system monospace font.

## Before publishing

Read every frame. Check that no real path, host, address or key appears, and
retake the set whenever a visible part of the UI changes.
