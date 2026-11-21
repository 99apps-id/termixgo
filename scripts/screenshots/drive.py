#!/usr/bin/env python3
"""Drives the real termixgo TUI in tmux and saves ANSI frames.

Run scripts/screenshots/setup_demo.sh first. See README.md in this folder."""
import os
import subprocess
import sys
import time

B = "/tmp/tgx-shots"
FRAMES = f"{B}/frames"
# The binary to photograph. Build one with the version you want on the title.
BIN = os.environ.get("TERMIXGO_BIN", f"{B}/termixgo-demo")
COLS, ROWS = 118, 34
ENV = ("env -i PATH=/usr/local/bin:/usr/bin:/bin TERM=tmux-256color COLORTERM=truecolor LANG=C.UTF-8 "
       "HTTPS_PROXY=http://127.0.0.1:18701 HTTP_PROXY=http://127.0.0.1:18701 NO_PROXY=127.0.0.1 "
       "SSL_CERT_FILE=/tmp/tgx-shots/ca.pem")


def tmux(*args):
    return subprocess.run(["tmux", "-f", "/dev/null", *args], capture_output=True, text=True)


def start(name, home, cwd, extra_env="", rows=ROWS, command=None):
    tmux("kill-session", "-t", name)
    os.makedirs(f"{home}/.termixgo", exist_ok=True)
    program = BIN if command is None else f"{BIN} {command.split(None, 1)[1]}"
    cmd = f"cd {cwd} && {ENV} HOME={home} TERMIXGO_HOME={home}/.termixgo {extra_env} {program}"
    tmux("new-session", "-d", "-s", name, "-x", str(COLS), "-y", str(rows), cmd)
    tmux("set-option", "-t", name, "-g", "default-terminal", "tmux-256color")
    tmux("set-option", "-t", name, "-ga", "terminal-features", ",*:RGB")


def screen(name):
    return tmux("capture-pane", "-t", name, "-e", "-p").stdout


def plain(name):
    return tmux("capture-pane", "-t", name, "-p").stdout


def wait_for(name, needle, timeout=40):
    end = time.time() + timeout
    while time.time() < end:
        if needle in plain(name):
            return True
        time.sleep(0.25)
    print(f"TIMEOUT waiting for {needle!r} in {name}", file=sys.stderr)
    return False


def keys(name, *k):
    tmux("send-keys", "-t", name, *k)


def text(name, value):
    tmux("send-keys", "-t", name, "-l", value)


def frame(name, label):
    path = f"{FRAMES}/{label}.ansi"
    with open(path, "w") as handle:
        handle.write(screen(name))
    print("frame", label)


def main():
    P = "/tmp/demo/shop-api"
    W = "/tmp/demo-new/shop-api"

    # 1. First launch: the setup wizard and its Claude OAuth step (tall pane, banner uncut).
    w = "wiz"
    start(w, f"{B}/wiz", W, rows=46)
    if wait_for(w, "Provider"):
        time.sleep(1.0)
        frame(w, "01-setup-provider")
        text(w, "claude")
        time.sleep(0.6)
        keys(w, "Enter")
        time.sleep(2.0)
        frame(w, "02-setup-oauth")
    tmux("kill-session", "-t", w)

    # 2. The login command the wizard points to, run in a shell.
    c = "cli"
    start(c, f"{B}/cli", W, rows=22, command="termixgo login claude-oauth")
    if wait_for(c, "Open this URL", 20):
        time.sleep(1.5)
        frame(c, "03-login-cli")
    tmux("kill-session", "-t", c)

    # 3. A ready session against the scripted model.
    m = "main"
    mock = subprocess.Popen(["python3", f"{B}/mock.py", "18700"])
    proxy = subprocess.Popen(["python3", f"{B}/mitm.py", "18701"])
    time.sleep(0.8)
    try:
        start(m, f"{B}/home", P, extra_env="DEEPSEEK_API_KEY=sk-demo-not-a-real-key", rows=40)
        wait_for(m, "Tab", 30)
        time.sleep(1.5)
        frame(m, "04-welcome")

        text(m, "/model")
        keys(m, "Enter")
        time.sleep(1.2)
        frame(m, "05-model-provider")
        keys(m, "Enter")  # Claude (OAuth) is first in the list
        time.sleep(1.5)
        frame(m, "06-model-list")
        keys(m, "Escape")
        time.sleep(1.0)
        keys(m, "Escape")
        time.sleep(0.8)

        text(m, "/")
        keys(m, "Enter")  # a bare slash opens the command palette
        time.sleep(1.2)
        frame(m, "07-slash-palette")
        keys(m, "Escape")
        time.sleep(0.8)

        text(m, "Add rate limiting to the orders endpoint and run the tests")
        keys(m, "Enter")
        if wait_for(m, "Approval needed", 40):
            time.sleep(1.0)
            frame(m, "08-approval-edit")
            keys(m, "s")
        # run_command asks next; answer it, then watch the test run.
        if wait_for(m, "Approval needed", 40):
            time.sleep(0.8)
            frame(m, "09-approval-command")
            keys(m, "s")
            time.sleep(2.5)
            frame(m, "10-running")
        if wait_for(m, "Done.", 60):
            time.sleep(1.5)
            frame(m, "11-streaming")
        wait_for(m, "Next I would move", 40)
        time.sleep(4.0)
        frame(m, "12-done")

        # 4. Plan mode and the "Ready to code?" dialog.
        text(m, "/approval plan")
        keys(m, "Enter")
        time.sleep(1.2)
        text(m, "Add pagination to GET /orders")
        keys(m, "Enter")
        if wait_for(m, "Ready to code", 40):
            time.sleep(1.0)
            frame(m, "13-plan-review")
    finally:
        tmux("kill-session", "-t", m)
        mock.terminate()
        proxy.terminate()

if __name__ == "__main__":
    main()
