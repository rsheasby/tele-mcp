package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type Session struct {
	id         string
	stdioClient client.Client
	mutex      sync.RWMutex
}

type Bridge struct {
	bootCommand string
	mcpCommand  string
	sessions    sync.Map
	bootClient  client.Client
}

func main() {
	bootCommand := os.Getenv("BOOT_COMMAND")
	mcpCommand := os.Getenv("MCP_COMMAND")

	if mcpCommand == "" {
		log.Fatal("MCP_COMMAND environment variable is required")
	}

	bridge := &Bridge{
		bootCommand: bootCommand,
		mcpCommand:  mcpCommand,
	}

	// Run boot command if specified
	if bootCommand != "" {
		log.Printf("Running boot command: %s", bootCommand)
		cmd := exec.Command("sh", "-c", bootCommand)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Printf("Boot command failed: %v", err)
		}
	}

	// Create a boot client to get server info
	bootClient, err := bridge.createStdioClient()
	if err != nil {
		log.Fatalf("Failed to create boot client: %v", err)
	}
	bridge.bootClient = bootClient

	// Create HTTP server
	httpServer := server.NewStreamableHTTPServer(bridge)
	
	log.Println("Starting HTTP MCP bridge on :8080")
	if err := httpServer.Start(":8080"); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func (b *Bridge) createStdioClient() (client.Client, error) {
	// Parse command as shell command
	cmd := exec.Command("sh", "-c", b.mcpCommand)
	
	c, err := client.NewStdioClient(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to create stdio client: %w", err)
	}

	ctx := context.Background()
	_, err = c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeRequestParams{
			ProtocolVersion: "2024-11-05",
			Capabilities: mcp.ClientCapabilities{
				Tools:     &mcp.ToolsCapability{},
				Resources: &mcp.ResourcesCapability{},
				Prompts:   &mcp.PromptsCapability{},
			},
			ClientInfo: mcp.Implementation{
				Name:    "tele-mcp-bridge",
				Version: "1.0.0",
			},
		},
	})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("failed to initialize client: %w", err)
	}

	return c, nil
}

// ServerInfo returns the server information from the boot client
func (b *Bridge) ServerInfo() mcp.ServerInfo {
	if b.bootClient == nil {
		return mcp.ServerInfo{
			Name:    "tele-mcp-bridge",
			Version: "1.0.0",
		}
	}
	
	// Get server info from boot client
	// This is a placeholder - we need to store this from initialization
	return mcp.ServerInfo{
		Name:    "tele-mcp-bridge",
		Version: "1.0.0",
	}
}

// Initialize handles client initialization and creates a new session
func (b *Bridge) Initialize(ctx context.Context, req mcp.InitializeRequest) (*mcp.InitializeResult, error) {
	// Create new stdio client for this session
	stdioClient, err := b.createStdioClient()
	if err != nil {
		return nil, err
	}

	// Get session ID from context (set by HTTP transport)
	sessionID := ctx.Value("sessionID").(string)

	session := &Session{
		id:          sessionID,
		stdioClient: stdioClient,
	}

	b.sessions.Store(sessionID, session)

	// Forward the initialization to get actual server info
	result, err := stdioClient.Initialize(ctx, req)
	if err != nil {
		stdioClient.Close()
		b.sessions.Delete(sessionID)
		return nil, err
	}

	return result, nil
}

// CallTool forwards tool calls to the appropriate stdio client
func (b *Bridge) CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	session, err := b.getSession(ctx)
	if err != nil {
		return nil, err
	}

	return session.stdioClient.CallTool(ctx, req)
}

// ListTools forwards list tools requests
func (b *Bridge) ListTools(ctx context.Context) (*mcp.ListToolsResult, error) {
	session, err := b.getSession(ctx)
	if err != nil {
		return nil, err
	}

	return session.stdioClient.ListTools(ctx, mcp.ListToolsRequest{})
}

// ListResources forwards list resources requests
func (b *Bridge) ListResources(ctx context.Context) (*mcp.ListResourcesResult, error) {
	session, err := b.getSession(ctx)
	if err != nil {
		return nil, err
	}

	return session.stdioClient.ListResources(ctx, mcp.ListResourcesRequest{})
}

// ReadResource forwards read resource requests
func (b *Bridge) ReadResource(ctx context.Context, req mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	session, err := b.getSession(ctx)
	if err != nil {
		return nil, err
	}

	return session.stdioClient.ReadResource(ctx, req)
}

// ListPrompts forwards list prompts requests
func (b *Bridge) ListPrompts(ctx context.Context) (*mcp.ListPromptsResult, error) {
	session, err := b.getSession(ctx)
	if err != nil {
		return nil, err
	}

	return session.stdioClient.ListPrompts(ctx, mcp.ListPromptsRequest{})
}

// GetPrompt forwards get prompt requests
func (b *Bridge) GetPrompt(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	session, err := b.getSession(ctx)
	if err != nil {
		return nil, err
	}

	return session.stdioClient.GetPrompt(ctx, req)
}

// Cleanup handles session cleanup
func (b *Bridge) Cleanup(ctx context.Context) error {
	sessionID := ctx.Value("sessionID").(string)
	
	if val, ok := b.sessions.Load(sessionID); ok {
		session := val.(*Session)
		session.stdioClient.Close()
		b.sessions.Delete(sessionID)
	}

	return nil
}

func (b *Bridge) getSession(ctx context.Context) (*Session, error) {
	sessionID := ctx.Value("sessionID").(string)
	
	val, ok := b.sessions.Load(sessionID)
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	return val.(*Session), nil
}

// ServeHTTP implements http.Handler
func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The StreamableHTTPServer will handle the actual HTTP protocol
	// This is just here to satisfy the interface if needed
}