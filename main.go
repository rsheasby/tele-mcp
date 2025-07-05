package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type Session struct {
	id           string
	stdioClient  *client.Client
	mutex        sync.RWMutex
	restartCount int
	lastError    time.Time
}

type BridgeServer struct {
	*server.MCPServer
	bootCommand string
	mcpCommand  string
	sessions    sync.Map
	durableMode bool
	
	// Template client to get server info
	templateClient *client.Client
	serverInfo     mcp.Implementation
	capabilities   mcp.ServerCapabilities
}

// Custom errors for LLM-friendly messages
var (
	ErrTemporaryFailure = errors.New("Temporary connection issue. Please retry immediately with the same parameters.")
	ErrSessionRestarted = errors.New("MCP session restarted. Some context may be lost. Please retry with the same parameters.")
)

func main() {
	bootCommand := os.Getenv("BOOT_COMMAND")
	mcpCommand := os.Getenv("MCP_COMMAND")
	durableMode := os.Getenv("DURABLE_MODE") != "false" // Default to true

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
			log.Fatalf("Boot command failed: %v", err)
		}
		log.Println("Boot command completed successfully")
	}

	// Create a template client to introspect the child server
	log.Println("Introspecting child MCP server...")
	templateClient, serverInfo, capabilities, err := introspectChildServer(mcpCommand)
	if err != nil {
		log.Fatalf("Failed to introspect child server: %v", err)
	}
	defer templateClient.Close()

	// Build server options based on discovered capabilities
	var opts []server.ServerOption
	
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

	// Create MCP server that mirrors the child server
	mcpServer := server.NewMCPServer(serverInfo.Name, serverInfo.Version, opts...)

	// Create bridge server
	bridge := &BridgeServer{
		MCPServer:      mcpServer,
		bootCommand:    bootCommand,
		mcpCommand:     mcpCommand,
		durableMode:    durableMode,
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
	
	// Re-create server with hooks added
	opts = append(opts, server.WithHooks(hooks))
	bridge.MCPServer = server.NewMCPServer(serverInfo.Name, serverInfo.Version, opts...)

	// Get initial tools/resources/prompts from template and register them
	ctx := context.Background()
	
	// Register all tools from child server
	if capabilities.Tools != nil {
		tools, err := templateClient.ListTools(ctx, mcp.ListToolsRequest{})
		if err == nil {
			for _, tool := range tools.Tools {
				// The tool.InputSchema contains the full JSON schema for parameters
				// We need to pass this through to the new tool
				newTool := mcp.Tool{
					Name:        tool.Name,
					Description: tool.Description,
					InputSchema: tool.InputSchema, // This preserves all parameter definitions
				}
				
				bridge.AddTool(
					newTool,
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
				// Create a complete copy of the resource to preserve all fields
				newResource := mcp.Resource{
					Annotated:   resource.Annotated,
					URI:         resource.URI,
					Name:        resource.Name,
					Description: resource.Description,
					MIMEType:    resource.MIMEType,
				}
				
				bridge.AddResource(
					newResource,
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
				// Create a new prompt that replicates the original including arguments
				newPrompt := mcp.Prompt{
					Name:        prompt.Name,
					Description: prompt.Description,
					Arguments:   prompt.Arguments, // This preserves all argument definitions
				}
				
				bridge.AddPrompt(
					newPrompt,
					func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
						return bridge.handlePromptGet(ctx, req)
					},
				)
			}
		}
	}

	// Start HTTP server
	httpServer := server.NewStreamableHTTPServer(bridge.MCPServer)

	log.Printf("Starting HTTP MCP bridge on :8080 for '%s' v%s (durable mode: %v)", 
		serverInfo.Name, serverInfo.Version, durableMode)
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

func (b *BridgeServer) restartSession(ctx context.Context, sessionID string) error {
	// Clean up existing session
	b.cleanupSession(sessionID)
	
	// Create new session
	b.createSessionForClient(ctx, sessionID)
	
	// Check if session was created successfully
	if _, ok := b.sessions.Load(sessionID); !ok {
		return fmt.Errorf("failed to restart session")
	}
	
	// Update restart count
	if val, ok := b.sessions.Load(sessionID); ok {
		session := val.(*Session)
		session.restartCount++
		session.lastError = time.Now()
	}
	
	return nil
}

func (b *BridgeServer) getSessionFromContext(ctx context.Context) (*Session, error) {
	// Get session from server context
	clientSession := server.ClientSessionFromContext(ctx)
	if clientSession == nil {
		return nil, fmt.Errorf("no session in context")
	}

	val, ok := b.sessions.Load(clientSession.SessionID())
	if !ok {
		// In durable mode, try to create the session if it doesn't exist
		if b.durableMode {
			b.createSessionForClient(ctx, clientSession.SessionID())
			// Try again
			val, ok = b.sessions.Load(clientSession.SessionID())
			if !ok {
				return nil, ErrTemporaryFailure
			}
		} else {
			return nil, fmt.Errorf("session not found: %s", clientSession.SessionID())
		}
	}

	return val.(*Session), nil
}

func isRetriableError(err error) bool {
	if err == nil {
		return false
	}
	
	// Check for common patterns that indicate a dead process
	errStr := err.Error()
	return strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "EOF") ||
		strings.Contains(errStr, "process exited") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "i/o timeout")
}

func (b *BridgeServer) handleWithRetry(
	ctx context.Context,
	operation func(*Session) error,
) error {
	session, err := b.getSessionFromContext(ctx)
	if err != nil {
		return err
	}

	// Try the operation
	err = operation(session)
	
	// If no error or not in durable mode, return as-is
	if err == nil || !b.durableMode {
		return err
	}
	
	// Check if this is a retriable error
	if !isRetriableError(err) {
		return err
	}
	
	// Limit restart attempts
	if session.restartCount >= 3 && time.Since(session.lastError) < 5*time.Minute {
		log.Printf("Session %s exceeded restart limit", session.id)
		return err
	}
	
	// Try to restart the session
	log.Printf("Attempting to restart session %s due to error: %v", session.id, err)
	
	clientSession := server.ClientSessionFromContext(ctx)
	if clientSession == nil {
		return err
	}
	
	if restartErr := b.restartSession(ctx, clientSession.SessionID()); restartErr != nil {
		log.Printf("Failed to restart session %s: %v", clientSession.SessionID(), restartErr)
		return ErrTemporaryFailure
	}
	
	// Session was restarted
	return ErrSessionRestarted
}

func (b *BridgeServer) handleToolCall(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var result *mcp.CallToolResult
	err := b.handleWithRetry(ctx, func(session *Session) error {
		var callErr error
		result, callErr = session.stdioClient.CallTool(ctx, req)
		return callErr
	})
	
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (b *BridgeServer) handleResourceRead(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	var result *mcp.ReadResourceResult
	err := b.handleWithRetry(ctx, func(session *Session) error {
		var readErr error
		result, readErr = session.stdioClient.ReadResource(ctx, req)
		return readErr
	})
	
	if err != nil {
		return nil, err
	}
	return result.Contents, nil
}

func (b *BridgeServer) handlePromptGet(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	var result *mcp.GetPromptResult
	err := b.handleWithRetry(ctx, func(session *Session) error {
		var getErr error
		result, getErr = session.stdioClient.GetPrompt(ctx, req)
		return getErr
	})
	
	if err != nil {
		return nil, err
	}
	return result, nil
}