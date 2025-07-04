# tele-mcp

A WebSocket bridge for MCP (Model Context Protocol) servers that use stdio transport. This allows you to run local MCP servers over the network.

## Features

- WebSocket and HTTP transports for MCP servers
- HTTP support with Server-Sent Events (SSE) for streaming responses
- Process pooling for faster connection response times
- One fresh process per connection (never reused)
- Automatic process lifecycle management
- Built on Ubuntu 24.04 with Node.js/npm, Python/pip/uv, and Rust/Cargo pre-installed

## Usage

### Environment Variables

- `MCP_COMMAND`: Command to execute MCP server (required)
- `BOOT_COMMAND`: Command to run once on startup (optional)
- `PORT`: Server port (default: 8080)
- `TRANSPORT`: Transport mode - "websocket", "http", or "both" (default: websocket)
- `WS_PATH`: WebSocket endpoint path (default: /ws)
- `HTTP_PATH`: HTTP endpoint path (default: /mcp)
- `POOL_SIZE`: Number of pre-spawned buffer processes (default: 0, max: 10)

### Docker

```bash
docker run -p 8080:8080 \
  -e MCP_COMMAND="npx -y @modelcontextprotocol/server-filesystem" \
  -e POOL_SIZE=3 \
  ghcr.io/rsheasby/tele-mcp

# With boot command to install packages
docker run -p 8080:8080 \
  -e BOOT_COMMAND="npm install -g @modelcontextprotocol/server-filesystem" \
  -e MCP_COMMAND="mcp-server-filesystem" \
  ghcr.io/rsheasby/tele-mcp

# With HTTP transport for streamable clients
docker run -p 8080:8080 \
  -e MCP_COMMAND="npx -y @modelcontextprotocol/server-filesystem" \
  -e TRANSPORT="http" \
  ghcr.io/rsheasby/tele-mcp

# With both transports enabled
docker run -p 8080:8080 \
  -e MCP_COMMAND="npx -y @modelcontextprotocol/server-filesystem" \
  -e TRANSPORT="both" \
  ghcr.io/rsheasby/tele-mcp
```

### Local Binary

```bash
MCP_COMMAND="npx -y @modelcontextprotocol/server-filesystem" ./tele-mcp
```

### Transport Details

#### WebSocket Transport
- Connect to `ws://localhost:8080/ws`
- Full bidirectional communication
- Supports all MCP features including notifications

#### HTTP Transport (Streamable)
- POST requests to `http://localhost:8080/mcp`
- Supports Server-Sent Events (SSE) for streaming responses
- Add `Accept: text/event-stream` header for SSE streaming
- Without SSE header, returns single JSON response

### With LibreChat

Configure LibreChat to connect to the WebSocket endpoint at `ws://localhost:8080/ws` or HTTP endpoint at `http://localhost:8080/mcp`.

## Building

```bash
go build -o tele-mcp
```

## Docker Build

```bash
docker build -t tele-mcp .

# Multi-arch builds are handled by GitHub Actions using native ARM64 runners
```