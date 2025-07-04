# tele-mcp

A WebSocket bridge for MCP (Model Context Protocol) servers that use stdio transport. This allows you to run local MCP servers over the network.

## Features

- WebSocket to stdio bridge for MCP servers
- Process pooling for faster connection response times
- One fresh process per WebSocket connection (never reused)
- Automatic process lifecycle management
- Built on Ubuntu 24.04 with Node.js/npm, Python/pip/uv, and Rust/Cargo pre-installed

## Usage

### Environment Variables

- `MCP_COMMAND`: Command to execute MCP server (required)
- `BOOT_COMMAND`: Command to run once on startup (optional)
- `PORT`: WebSocket server port (default: 8080)
- `WS_PATH`: WebSocket endpoint path (default: /ws)
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
```

### Local Binary

```bash
MCP_COMMAND="npx -y @modelcontextprotocol/server-filesystem" ./tele-mcp
```

### With LibreChat

Configure LibreChat to connect to the WebSocket endpoint at `ws://localhost:8080/ws`.

## Building

```bash
go build -o tele-mcp
```

## Docker Build

```bash
docker build -t tele-mcp .

# Multi-arch builds are handled by GitHub Actions using native ARM64 runners
```