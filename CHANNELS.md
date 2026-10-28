# Channels and Companions: WhatsApp and Telegram Guide

Termixgo provides native multi-channel messaging support for WhatsApp and Telegram.
Each channel connects directly to an autonomous agent running on your local machine
or remote Linux VPS, enabling 24/7 task steering, remote code execution, server
health audits, and conversational assistance from your phone.

---

## Architecture Overview

Termixgo connects messaging platforms directly to autonomous agents:

```
[ WhatsApp App ] <--- (whatsmeow / multi-device) ---> [ Termixgo Daemon ] ---> [ Agent Home ]
                                                             |                 |-- SOUL.md (Persona)
[ Telegram App ] <--- (Telegram Bot API)          ---> [ termixgo serve ]     |-- USER.md (Profile)
                                                                               `-- Workspace (Code)
```

- **Native Protocol:** WhatsApp uses native multi-device socket encryption via pure Go
  without requiring headless browsers, Selenium, or Puppeteer.
- **Enriched Formatting:** Messages are converted to native WhatsApp syntax with
  clean bold (`*text*`), bold-italic (`_*text*_`), unicode bullet dots, and clear headings.
- **Multimodal Vision:** Send photos, diagrams, and PDF documents directly in chat.
  Vision models inspect visual input directly, while documents on non-vision models
  are safely stored in `.termixgo/uploads/` for tool examination.
- **Anti-Silent Drop:** Errors and rate limits are dispatched through a detached
  timeout context, guaranteeing immediate notification instead of dropped messages.
- **Agent Binding:** Each channel account is attached to a specific agent (`default`,
  `sales`, `devops`, or custom). Each agent possesses its own isolated workspace,
  `SOUL.md` persona, and `USER.md` operator profile.
- **Safety Boundary:** Guest chats are automatically restricted to read tools and
  approval-gated writes. Shell execution, background worker orchestration, subagents,
  and credential access are strictly reserved for verified operator admins.
- **Bilingual Intelligence:** Interactive slash command outputs automatically adapt to
  sender locale (+62 numbers receive Indonesian; international numbers receive English).

---

## 1. WhatsApp Channel Setup Guide

Follow these steps to activate WhatsApp and connect it with your Termixgo agent:

### Step 1: Add the WhatsApp Account

Register a WhatsApp channel account tied to your desired agent (for example, `default`):

```sh
termixgo channel add whatsapp default
```

If the specified agent does not exist yet, Termixgo automatically initializes the
agent workspace and creates template persona files (`SOUL.md` and `USER.md`).

### Step 2: Login and Link Your Device

You can authenticate your WhatsApp account in one of two ways:

#### Option A: Terminal QR Code (Standard)

Run the login command in your terminal:

```sh
termixgo channel login whatsapp default
```

A half-block QR code will render directly in your terminal. On your smartphone:
1. Open WhatsApp.
2. Tap **Settings** (or three dots) > **Linked Devices**.
3. Tap **Link a Device** and point your camera at the terminal QR code.
4. Once scanned, the terminal confirms that the account is linked and enabled.

#### Option B: 8-Digit Pairing Code (Headless VPS without Camera)

If you are working on a remote VPS or cannot scan a QR code, use phone number pairing:

```sh
termixgo channel login whatsapp default --pair 628xxxxxxxxxx
```

Termixgo generates an 8-digit alphanumeric pairing code. On your smartphone:
1. Open WhatsApp > **Linked Devices** > **Link a Device**.
2. Select **Link with phone number instead**.
3. Type the 8-digit code displayed in your terminal.

### Step 3: Configure Admin Phone Number

Designate your personal phone number as the channel admin to obtain operator privileges:

```sh
termixgo channel admin add whatsapp default 628xxxxxxxxxx
```

Admins can execute shell tools, approve code edits, run refactorings, and switch AI models.

### Step 4: Configure Inbound DM Policy

Determine how your agent handles direct messages from unknown contacts:

```sh
termixgo channel dm whatsapp default open
```

Available DM policies:
- `open`: Anyone can message the agent. Guests are bounded by read-only and approval rules.
- `pairing`: Unknown senders receive a pairing code and wait for admin approval.
- `allowlist`: Only numbers explicitly added to the allowlist can talk to the agent.
- `deny`: Direct messages are completely ignored.

To add specific numbers to the allowlist:

```sh
termixgo channel allow add whatsapp default 628xxxxxxxxxx
```

### Step 5: Enable the Channel

Ensure the channel account is switched on:

```sh
termixgo channel on whatsapp default
```

### Step 6: Start the Runtime Daemon

Run the daemon in the foreground to begin receiving messages:

```sh
termixgo serve
```

To run Termixgo continuously in the background 24/7 on a Linux VPS, install the systemd
service:

```sh
termixgo service install
termixgo service status
```

### Step 7: Verify Connection with Agent

Open WhatsApp on your phone and send a message to the linked number (for example, "Halo"
or "/status"). The agent will stream its response using the personality defined in
your agent's `SOUL.md`.

### Step 8: Send Images, PDFs, and Use In-Chat Commands

- **Send Photos & Screenshots:** Attach an image with an optional caption asking the
  agent to inspect the error screenshot, UI mock, or diagram.
- **Send PDF Documents:** Upload a PDF document. Vision models parse the pages directly.
  Non-vision models store the file in `.termixgo/uploads/` so the agent can read and
  process the document with CLI tools.
- **Enriched Formatting:** Code snippets, bullet dots, bold terms, and headings are
  formatted cleanly using native WhatsApp text modifiers.
- **In-Chat Commands:** Type `/models` to view active models, `/model <name>` to switch
  the active model, `/status` for agent metrics, or `/diff` to inspect recent edits.

---

## 2. Telegram Channel Setup Guide

Follow these steps to activate Telegram and link it to your Termixgo agent:

### Step 1: Create a Bot via @BotFather

1. Open Telegram and search for `@BotFather`.
2. Send `/newbot`.
3. Provide a display name (e.g. `MyTermixgoBot`) and a unique username ending in `bot`
   (e.g. `my_termixgo_agent_bot`).
4. Copy the HTTP API token provided by BotFather (format: `123456789:ABCdefGHI...`).

### Step 2: Store Bot Token in Termixgo Secrets Vault

Store the bot token in Termixgo encrypted local secrets store:

```sh
termixgo secret telegram <YOUR_BOT_TOKEN>
```

### Step 3: Add and Login the Telegram Channel

Add the Telegram channel account:

```sh
termixgo channel add telegram default
```

Authenticate the token with Telegram servers:

```sh
termixgo channel login telegram default <YOUR_BOT_TOKEN>
```

Termixgo validates the token with Telegram `getMe` API and links the username.

### Step 4: Configure Policy and Admin

Set the inbound DM policy:

```sh
termixgo channel dm telegram default open
```

Or set to `allowlist` and add your numeric Telegram user ID:

```sh
termixgo channel allow add telegram default <TELEGRAM_USER_ID>
termixgo channel admin add telegram default <TELEGRAM_USER_ID>
```

### Step 5: Enable the Channel and Start Daemon

Turn on the Telegram channel:

```sh
termixgo channel on telegram default
```

Launch the server daemon:

```sh
termixgo serve
```

### Step 6: Authorize Operator Pairing

1. Open your bot in Telegram and tap **Start** (or send `/start`).
2. If pairing mode is enabled, send `/pair <PAIRING_CODE>` using the code provided in
   your terminal setup.
3. Send `/status` or `/models` to confirm the agent is fully responsive.

---

## 3. Direct Chat Slash Commands Reference

Once connected, you can type slash commands directly into your WhatsApp or Telegram
conversation. Responses automatically match your language preference:

| Command | Arguments | Description |
| --- | --- | --- |
| `/models` | none | Lists only active AI models with registered API keys or OAuth on the host. |
| `/model` | `<name>` | Dynamically switches active model (e.g. `stepfun`, `muse`, `antigravity`, `claude`). |
| `/status` | none | Displays active model, agent name, workspace trust, and session memory status. |
| `/new` | none | Clears session context memory and starts a fresh conversation turn. |
| `/stop` | none | Immediately cancels running tasks, subagents, or shell tool calls. |
| `/diff` | none | Displays unified git diff of the latest file edits made by the agent. |
| `/help` or `/menu` | none | Displays interactive help menu tailored to user locale. |

---

## 4. Complete CLI Channel Management Matrix

Manage your messaging accounts from the terminal with `termixgo channel`:

```sh
# Account Listing and Status
termixgo channel list                             # List all configured accounts and states
termixgo channel list --agent <name>              # Filter accounts for a specific agent
termixgo channel status [whatsapp|telegram]       # Inspect detailed account config and policies

# Adding and Removing Accounts
termixgo channel add <whatsapp|telegram> [agent]  # Register a new account
termixgo channel rm <whatsapp|telegram> [agent]   # Remove an account
termixgo channel rm whatsapp [agent] --purge      # Remove account and delete session state

# State Toggles
termixgo channel on <whatsapp|telegram> [agent]   # Enable channel account
termixgo channel off <whatsapp|telegram> [agent]  # Disable channel account

# Permissions and Security
termixgo channel admin add <type> [agent] <id>    # Add phone or user ID to admin list
termixgo channel admin rm <type> [agent] <id>     # Remove phone or user ID from admin list
termixgo channel allow add <type> [agent] <id>    # Add phone or user ID to allowlist
termixgo channel allow rm <type> [agent] <id>     # Remove phone or user ID from allowlist
termixgo channel dm <type> [agent] <policy>       # Set DM policy: open | pairing | allowlist | deny
termixgo channel groups <type> [agent] <policy>   # Set group policy: mention | all | disabled

# Guest Protection Limits
termixgo channel guest <type> [agent] --max-steps 15 --cost-budget 0.50 --rate 30

# Pairing Requests
termixgo channel pairing list                     # List pending pairing requests from unknown senders
termixgo channel pairing approve <code>           # Approve sender pairing code
termixgo channel pairing reject <code>            # Reject sender pairing code
```

---

## 5. 24/7 VPS Daemon Service

To ensure your WhatsApp and Telegram agents stay connected after closing your SSH terminal:

```sh
# Install background service (systemd user unit on Linux, Task Scheduler on Windows)
termixgo service install

# Check service status
termixgo service status

# Manage service via standard systemctl (Linux)
systemctl --user status termixgo
systemctl --user restart termixgo
journalctl --user -u termixgo -f
```

On Linux systems, `termixgo service install` automatically enables systemd user lingering
(`loginctl enable-linger`), ensuring your agents run continuously across server reboots.

---

## 6. Persona Customization and File Protection

Each channel routes to an agent whose identity is stored in:

- `~/.termixgo/agents/<agent>/SOUL.md`: Name, tone, communication conventions, and core instructions.
- `~/.termixgo/agents/<agent>/USER.md`: Operator preferences, project background, and constraints.

**Protection Guarantee:**
Termixgo preserves existing `SOUL.md` and `USER.md` files upon every upgrade and installation.
Your custom personas and operator instructions are never overwritten.
