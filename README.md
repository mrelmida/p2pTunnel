# p2pTunnel — Cross-Platform P2P Port & IP Sharing (Windows & Linux)

**p2pTunnel** lets two or more people connect over a short shared room code (e.g. `K7X-92M`), establish direct **end-to-end encrypted QUIC tunnels** (with automatic fallback to your EU relay server when behind strict routers/CGNAT), and share any **TCP or UDP port**.

Every peer in a room is assigned a dedicated loopback IP (`127.0.0.2`, `127.0.0.3`, `127.0.0.4`, ...) that works out-of-the-box on both **Windows** and **Linux** with **no virtual network drivers and no Administrator/root privileges required**.

---

## How It Works

1. **Configure Your Relay in the GUI**:
   - Enter your **Relay IP**, **Relay Port** (default `4433`), and **Relay Secret** in the top bar of the GUI (these settings are saved automatically between launches).

2. **Create or Join a Room**:
   - **Peer 1** clicks **Create Room** (generates a short code like `K7X-92M`, or uses a custom room code + optional room password) and gets assigned **`127.0.0.2`**.
   - **Peer 2** enters `K7X-92M`, clicks **Join Room**, and gets assigned **`127.0.0.3`**.
   - Additional friends joining the same room get `127.0.0.4`, `127.0.0.5`, etc.

3. **Direct P2P Hole Punching + Automatic EU Relay Fallback**:
   - The EU server exchanges peers' public UDP endpoints and coordinates simultaneous UDP hole punching.
   - If direct P2P succeeds, traffic flows directly between the peers (`Direct P2P`).
   - If a strict firewall or Symmetric NAT / CGNAT blocks direct UDP hole punching, the connection automatically falls back to the EU server's UDP relay (`EU Relay`).
   - All connections use **Mutual TLS 1.3 with ephemeral Ed25519 certificate fingerprint verification**, so even relayed traffic is end-to-end encrypted and opaque to the EU server.

4. **Bidirectional TCP & UDP Port Sharing**:
   - Anyone in the room can share any local TCP, UDP, or TCP+UDP port (e.g., `25565` for Minecraft, `8080` for a web app).
   - Shared ports automatically bind on every other room member's machine at the sharing peer's assigned `127.0.0.x:PORT`.
   - Receiving peers can one-click **Copy Address**, pause/enable individual ports, or override the local bind IP/port (`Custom Port...`).
   - **Strict Default-Deny Security**: Only ports explicitly added to *My Shared Ports* are reachable by peers in the room.

---

## Deploying the EU Relay Server (`docker-compose.yml`)

Edit `RELAY_IP`, `RELAY_PORT`, and `RELAY_SECRET` in `docker-compose.yml` (or set them in a `.env` file):

```yaml
services:
  p2ptunnel-relay:
    image: ghcr.io/mrelmida/p2ptunnel-relay:latest
    container_name: p2ptunnel-relay
    restart: unless-stopped
    environment:
      RELAY_IP: "0.0.0.0"
      RELAY_PORT: "4433"
      RELAY_SECRET: "your-strong-relay-secret"
    ports:
      - "0.0.0.0:4433:4433/tcp"
      - "0.0.0.0:4433:4433/udp"
```

Start the relay server:

```bash
docker compose up -d
```

Or run the binary directly:

```bash
./bin/p2ptunnel-server -ip 0.0.0.0 -port 4433 -secret "your-strong-relay-secret"
```

---

## Building the Client & Server

### 1. Build Linux Server & Desktop GUI Client
```bash
make all
```
Produces:
- `bin/p2ptunnel-server` — Lightweight static binary for your EU VPS
- `bin/p2ptunnel` — Desktop GUI client for Linux

### 2. Build on Windows (or Cross-Compile from Linux)
On Windows (with Go and a C compiler like TDM-GCC / MSYS2 installed):
```powershell
go build -trimpath -ldflags="-s -w -H=windowsgui" -o bin/p2ptunnel.exe ./cmd/client
```
Or cross-compile from Linux (with `mingw-w64` installed):
```bash
make client-windows
```

---

## Headless / CLI Mode

The client binary also supports headless operation via `-headless`:

```bash
# Host a room and share TCP+UDP port 25565
./bin/p2ptunnel -headless -ip 203.0.113.10 -port 4433 -secret "your-strong-relay-secret" -name Alice -create -share both:25565:Minecraft

# Join a room from another machine
./bin/p2ptunnel -headless -ip 203.0.113.10 -port 4433 -secret "your-strong-relay-secret" -name Bob -join K7X-92M
```
