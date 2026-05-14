## The Finals Cafe Channel Bot (Go) - notes

### OneBot / NapCat current state (this server)

- **NapCat runs in Docker**: container name `napcat` (image `mlikiowa/napcat-docker:latest`).
- **WebUI**: exposed on **`6099`**.
- **OneBot (HTTP Server)**: exposed on **`3000`**.
- **OneBot (WebSocket Server)**: expose **`3001`** for reverse-WS client connection.

### What was broken and why

- When the HTTP server was configured to bind **`127.0.0.1` inside the container**, calls from the host via Docker port mapping failed (reset/empty reply).
- Binding **`0.0.0.0`** inside the container is required so Docker can forward traffic to the container network interface.

### Working OneBot HTTP details (verified)

- **Base URL**: `http://127.0.0.1:3000`
- **Auth**: `Authorization: Bearer <ONEBOT_TOKEN>`
- **API style**:
  - Works with **direct endpoints** like `GET /get_login_info`, `GET /get_group_list`
  - **Does NOT** work with `/onebot/v11/...` (returns "不支持的Api onebot")

Example:

```bash
curl -sS -m 10 'http://127.0.0.1:3000/get_login_info' \
  -H 'Authorization: Bearer <ONEBOT_TOKEN>'
```

Get group list:

```bash
curl -sS -m 20 'http://127.0.0.1:3000/get_group_list' \
  -H 'Authorization: Bearer <ONEBOT_TOKEN>'
```

### Notes about "seeing group content"

- OneBot HTTP can query things like **login info** and **group list**.
- **Chat content/history is not typically pullable** via OneBot HTTP. Usually you must configure **event push** (HTTP callback or WS/reverse-WS) and then your bot service receives messages in real time.
- NapCat container logs may show incoming messages, but that is not a reliable/complete way to build a bot.

### Minimal next steps for your Go bot

- Integration style selected:
  - **WS (NapCat WebSocket Server) for receiving events**
  - **HTTP API for sending messages**
- For The Finals Cafe Channel, group id:
  - `1026710563`

### Run the bot (local Go)

Environment variables:

- `ONEBOT_REVERSE_WS_URL`
- `ONEBOT_WS_TOKEN` (required for WS connect)
- `TARGET_GROUP_ID`
- `OPENAI_API_KEY`
- `OPENAI_BASE_URL`
- `OPENAI_MODEL`
- `.env` file is auto-loaded on startup (already supported by code)

Example:

```bash
# edit .env first, then run:
go run .
```

Quick start:

```bash
cp .env.example .env
# edit .env
go run .
```

Commands:

- `ping` -> `pong`
- `/help` or `help` or `帮助` -> command list
- Other plain text -> intelligent reply (when `OPENAI_API_KEY` is set)

### Important for Docker NapCat

If NapCat runs in Docker and bot runs on host, map both ports:

```bash
docker run -d --name napcat \
  -p 6099:6099 \
  -p 3000:3000 \
  -p 3001:3001 \
  mlikiowa/napcat-docker:latest
```

Also ensure NapCat WebSocket server binds `0.0.0.0` (not `127.0.0.1`) inside container.
