package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type Session struct {
	id          string
	stdioClient *client.Client
	mutex       sync.RWMutex
}

type BridgeServer struct {
	*server.MCPServer
	bootCommand string
	mcpCommand  string
	sessions    sync.Map
	
	// Template client to get server info
	templateClient *client.Client
	serverInfo     mcp.Implementation
	capabilities   mcp.ServerCapabilities
}

func main() {
	bootCommand := os.Getenv("BOOT_COMMAND")
	mcpCommand := os.Getenv("MCP_COMMAND")

	if mcpCommand == "" {
		log.Fatal("MCP_COMMAND environment variable is required")
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

	// Create a template client to introspect the child server
	log.Println("Introspecting child MCP server...")
	templateClient, serverInfo, capabilities, err := introspectChildServer(mcpCommand)
	if err != nil {
		log.Fatalf("Failed to introspect child server: %v", err)
	}
	defer templateClient.Close()

	// Create MCP server that mirrors the child server
	mcpServer := server.NewMCPServer(serverInfo.Name, serverInfo.Version)
	
	// Set capabilities based on child server
	if capabilities.Tools != nil {
		mcpServer = server.NewMCPServer(serverInfo.Name, serverInfo.Version,
			server.WithToolCapabilities(true))
	}
	if capabilities.Resources != nil {
		mcpServer = server.NewMCPServer(serverInfo.Name, serverInfo.Version,
			server.WithResourceCapabilities(capabilities.Resources.Subscribe, capabilities.Resources.ListChanged))
	}
	if capabilities.Prompts != nil {
		mcpServer = server.NewMCPServer(serverInfo.Name, serverInfo.Version,
			server.WithPromptCapabilities(capabilities.Prompts.ListChanged))
	}

	// Create bridge server
	bridge := &BridgeServer{
		MCPServer:      mcpServer,
		bootCommand:    bootCommand,
		mcpCommand:     mcpCommand,
		templateClient: templateClient,
		serverInfo:     serverInfo,
		capabilities:   capabilities,
	}

	// Set up hooks to manage sessions
	hooks := &server.Hooks{}
	hooks.AddOnRegisterSession(func(ctx context.Context, session server.ClientSession) {
		log.Printf("Client %s connected", session.SessionID())
		go bridge.createSessionForClient(ctx, session.SessionID())
	})

	hooks.AddOnUnregisterSession(func(ctx context.Context, session server.ClientSession) {
		log.Printf("Client %s disconnected", session.SessionID())
		bridge.cleanupSession(session.SessionID())
	})

	// Re-create server with all capabilities and hooks
	var opts []server.ServerOption
	opts = append(opts, server.WithHooks(hooks))
	
	if capabilities.Tools != nil {
		opts = append(opts, server.WithToolCapabilities(true))
	}
	if capabilities.Resources != nil {
		opts = append(opts, server.WithResourceCapabilities(
			capabilities.Resources.Subscribe,
			capabilities.Resources.ListChanged))
	}
	if capabilities.Prompts != nil {
		opts = append(opts, server.WithPromptCapabilities(
			capabilities.Prompts.ListChanged))
	}
	
	bridge.MCPServer = server.NewMCPServer(serverInfo.Name, serverInfo.Version, opts...)

	// Get initial tools/resources/prompts from template and register them
	ctx := context.Background()
	
	// Register all tools from child server
	if capabilities.Tools != nil {
		tools, err := templateClient.ListTools(ctx, mcp.ListToolsRequest{})
		if err == nil {
			for _, tool := range tools.Tools {
				// Register each tool with a handler that forwards to child
				toolCopy := tool // Capture tool in closure
				bridge.AddTool(
					mcp.NewTool(toolCopy.Name, 
						mcp.WithDescription(toolCopy.Description),
						// Note: We can't perfectly replicate input schemas without more introspection
					),
					func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						return bridge.handleToolCall(ctx, req)
					},
				)
			}
		}
	}

	// Register all resources from child server
	if capabilities.Resources != nil {
		resources, err := templateClient.ListResources(ctx, mcp.ListResourcesRequest{})
		if err == nil {
			for _, resource := range resources.Resources {
				// Register each resource with a handler that forwards to child
				resourceCopy := resource // Capture resource in closure
				var opts []mcp.ResourceOption
				if resourceCopy.Description != "" {
					opts = append(opts, mcp.WithResourceDescription(resourceCopy.Description))
				}
				if resourceCopy.MIMEType != "" {
					opts = append(opts, mcp.WithMIMEType(resourceCopy.MIMEType))
				}
				
				bridge.AddResource(
					mcp.NewResource(resourceCopy.URI, resourceCopy.Name, opts...),
					func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
						return bridge.handleResourceRead(ctx, req)
					},
				)
			}
		}
	}

	// Register all prompts from child server
	if capabilities.Prompts != nil {
		prompts, err := templateClient.ListPrompts(ctx, mcp.ListPromptsRequest{})
		if err == nil {
			for _, prompt := range prompts.Prompts {
				// Register each prompt with a handler that forwards to child
				promptCopy := prompt // Capture prompt in closure
				var opts []mcp.PromptOption
				if promptCopy.Description != "" {
					opts = append(opts, mcp.WithPromptDescription(promptCopy.Description))
				}
				
				bridge.AddPrompt(
					mcp.NewPrompt(promptCopy.Name, opts...),
					func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
						return bridge.handlePromptGet(ctx, req)
					},
				)
			}
		}
	}

	// Start HTTP server
	httpServer := server.NewStreamableHTTPServer(bridge.MCPServer)

	log.Printf("Starting HTTP MCP bridge on :8080 for '%s' v%s", serverInfo.Name, serverInfo.Version)
	if err := httpServer.Start(":8080"); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func introspectChildServer(mcpCommand string) (*client.Client, mcp.Implementation, mcp.ServerCapabilities, error) {
	// Parse command
	parts := strings.Fields(mcpCommand)
	if len(parts) == 0 {
		return nil, mcp.Implementation{}, mcp.ServerCapabilities{}, fmt.Errorf("invalid MCP_COMMAND")
	}

	// Create stdio client
	var stdioClient *client.Client
	var err error
	
	if len(parts) == 1 {
		stdioClient, err = client.NewStdioMCPClient(parts[0], nil)
	} else {
		stdioClient, err = client.NewStdioMCPClient(parts[0], nil, parts[1:]...)
	}
	
	if err != nil {
		return nil, mcp.Implementation{}, mcp.ServerCapabilities{}, err
	}

	// Initialize the client
	initReq := mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: "2024-11-05",
			Capabilities: mcp.ClientCapabilities{
				Experimental: map[string]interface{}{},
				Sampling:     &struct{}{},
			},
			ClientInfo: mcp.Implementation{
				Name:    "tele-mcp-introspector",
				Version: "1.0.0",
			},
		},
	}

	result, err := stdioClient.Initialize(context.Background(), initReq)
	if err != nil {
		stdioClient.Close()
		return nil, mcp.Implementation{}, mcp.ServerCapabilities{}, err
	}

	return stdioClient, result.ServerInfo, result.Capabilities, nil
}

func (b *BridgeServer) createSessionForClient(ctx context.Context, sessionID string) {
	// Parse command
	parts := strings.Fields(b.mcpCommand)
	if len(parts) == 0 {
		log.Printf("Invalid MCP_COMMAND for session %s", sessionID)
		return
	}

	// Create stdio client for this session
	var stdioClient *client.Client
	var err error
	
	if len(parts) == 1 {
		stdioClient, err = client.NewStdioMCPClient(parts[0], nil)
	} else {
		stdioClient, err = client.NewStdioMCPClient(parts[0], nil, parts[1:]...)
	}
	
	if err != nil {
		log.Printf("Failed to create stdio client for session %s: %v", sessionID, err)
		return
	}

	// Initialize the client
	initReq := mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: "2024-11-05",
			Capabilities: mcp.ClientCapabilities{
				Experimental: map[string]interface{}{},
				Sampling:     &struct{}{},
			},
			ClientInfo: mcp.Implementation{
				Name:    "tele-mcp-bridge",
				Version: "1.0.0",
			},
		},
	}

	_, err = stdioClient.Initialize(ctx, initReq)
	if err != nil {
		stdioClient.Close()
		log.Printf("Failed to initialize client for session %s: %v", sessionID, err)
		return
	}

	session := &Session{
		id:          sessionID,
		stdioClient: stdioClient,
	}

	b.sessions.Store(sessionID, session)
	log.Printf("Session %s created successfully", sessionID)
}

func (b *BridgeServer) cleanupSession(sessionID string) {
	if val, ok := b.sessions.Load(sessionID); ok {
		session := val.(*Session)
		session.stdioClient.Close()
		b.sessions.Delete(sessionID)
		log.Printf("Session %s cleaned up", sessionID)
	}
}

func (b *BridgeServer) getSessionFromContext(ctx context.Context) (*Session, error) {
	// Get session from server context
	clientSession := server.ClientSessionFromContext(ctx)
	if clientSession == nil {
		return nil, fmt.Errorf("no session in context")
	}

	val, ok := b.sessions.Load(clientSession.SessionID())
	if !ok {
		return nil, fmt.Errorf("session not found: %s", clientSession.SessionID())
	}

	return val.(*Session), nil
}

func (b *BridgeServer) handleToolCall(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	session, err := b.getSessionFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	// Forward the tool call to the child server
	return session.stdioClient.CallTool(ctx, req)
}

func (b *BridgeServer) handleResourceRead(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	session, err := b.getSessionFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	// Forward the resource read to the child server
	result, err := session.stdioClient.ReadResource(ctx, req)
	if err != nil {
		return nil, err
	}

	// ResourceContents is already the correct type from ReadResourceResult
	return result.Contents, nil
}

func (b *BridgeServer) handlePromptGet(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	session, err := b.getSessionFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	// Forward the prompt get to the child server
	return session.stdioClient.GetPrompt(ctx, req)
}