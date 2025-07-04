package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type HTTPBridge struct {
	pool       *ProcessPool
	mcpCommand string
}

func handleHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("New HTTP %s request from %s", r.Method, r.RemoteAddr)
	
	// Note: The MCP spec requires Origin validation, but we intentionally skip it
	// because the purpose of tele-mcp is to enable remote access from any origin.
	
	// MUST check MCP-Protocol-Version header
	if r.Header.Get("MCP-Protocol-Version") == "" {
		http.Error(w, "Missing MCP-Protocol-Version header", http.StatusBadRequest)
		return
	}
	
	// Handle GET requests for SSE streaming
	if r.Method == http.MethodGet {
		// MUST check Accept header for SSE
		accept := r.Header.Get("Accept")
		if !strings.Contains(accept, "text/event-stream") {
			http.Error(w, "Accept header must include text/event-stream for GET requests", http.StatusBadRequest)
			return
		}
		handleSSEStream(w, r)
		return
	}
	
	// Only accept POST requests for sending messages
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	// MUST check Accept header includes both types
	accept := r.Header.Get("Accept")
	if !strings.Contains(accept, "application/json") || !strings.Contains(accept, "text/event-stream") {
		http.Error(w, "Accept header must include both application/json and text/event-stream", http.StatusBadRequest)
		return
	}

	// Read the request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	
	// Parse the incoming message to determine type
	var incomingMsg map[string]interface{}
	if err := json.Unmarshal(body, &incomingMsg); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	
	// Check if this is a response or notification from client
	_, hasMethod := incomingMsg["method"]
	_, hasResult := incomingMsg["result"]
	_, hasError := incomingMsg["error"]
	
	if !hasMethod && (hasResult || hasError) {
		// This is a response or notification response from client
		// MUST return 202 Accepted
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Create a new bridge for this request
	bridge := &HTTPBridge{
		pool:       pool,
		mcpCommand: mcpCommand,
	}

	// Get or create a process
	process := bridge.pool.Get()
	if process == nil {
		var err error
		process, err = NewProcess(bridge.mcpCommand)
		if err != nil {
			http.Error(w, "Failed to create process", http.StatusInternalServerError)
			return
		}
		if err := process.Start(); err != nil {
			http.Error(w, "Failed to start process", http.StatusInternalServerError)
			return
		}
	}
	defer process.Kill()

	// Send the request to the process
	if _, err := process.stdin.Write(body); err != nil {
		http.Error(w, "Failed to write to process", http.StatusInternalServerError)
		return
	}
	if _, err := process.stdin.Write([]byte("\n")); err != nil {
		http.Error(w, "Failed to write newline", http.StatusInternalServerError)
		return
	}

	// Check if client accepts SSE
	acceptSSE := strings.Contains(r.Header.Get("Accept"), "text/event-stream")

	if acceptSSE {
		// Stream response using Server-Sent Events
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}

		// Stream responses from the process
		scanner := bufio.NewScanner(process.stdout)
		for scanner.Scan() {
			line := scanner.Text()
			
			// Check if this is a complete JSON response
			var msg json.RawMessage
			if err := json.Unmarshal([]byte(line), &msg); err == nil {
				// Valid JSON, send as SSE event
				fmt.Fprintf(w, "data: %s\n\n", line)
				flusher.Flush()
				
				// Check if this is a result/error response (no id field means it's a response)
				var check map[string]interface{}
				if err := json.Unmarshal([]byte(line), &check); err == nil {
					if _, hasID := check["id"]; !hasID {
						// This is a response, we're done
						break
					}
				}
			}
		}

		// Send done event
		fmt.Fprintf(w, "event: done\ndata: \n\n")
		flusher.Flush()
	} else {
		// Non-streaming response - read until we get a complete response
		scanner := bufio.NewScanner(process.stdout)
		timeout := time.After(30 * time.Second)
		
		for {
			select {
			case <-timeout:
				http.Error(w, "Response timeout", http.StatusGatewayTimeout)
				return
			default:
				if scanner.Scan() {
					line := scanner.Text()
					
					// Try to parse as JSON
					var msg json.RawMessage
					if err := json.Unmarshal([]byte(line), &msg); err == nil {
						// Check message type
						var check map[string]interface{}
						if err := json.Unmarshal([]byte(line), &check); err == nil {
							// Determine message type based on JSON-RPC spec
							_, hasMethod := check["method"]
							_, hasResult := check["result"]
							_, hasError := check["error"]
							idField, hasID := check["id"]
							
							if hasMethod && hasID {
								// This is a request from server - continue reading
								continue
							} else if hasMethod && !hasID {
								// This is a notification from server - continue reading
								continue
							} else if (hasResult || hasError) && hasID {
								// This is a response to our request
								w.Header().Set("Content-Type", "application/json")
								w.Write([]byte(line))
								return
							} else if (hasResult || hasError) && idField == nil {
								// This is a response (id is explicitly null) - likely to a notification
								// MUST return 202 Accepted for responses to notifications
								w.WriteHeader(http.StatusAccepted)
								return
							}
						}
					}
				} else {
					http.Error(w, "Failed to read response", http.StatusInternalServerError)
					return
				}
			}
		}
	}
}

func handleSSEStream(w http.ResponseWriter, _ *http.Request) {
	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Create a new process for this SSE session
	process, err := NewProcess(mcpCommand)
	if err != nil {
		http.Error(w, "Failed to create process", http.StatusInternalServerError)
		return
	}
	if err := process.Start(); err != nil {
		http.Error(w, "Failed to start process", http.StatusInternalServerError)
		return
	}
	defer process.Kill()

	// Keep the connection open and stream any server-initiated messages
	scanner := bufio.NewScanner(process.stdout)
	for scanner.Scan() {
		line := scanner.Text()
		
		// Validate it's JSON before sending
		var msg json.RawMessage
		if err := json.Unmarshal([]byte(line), &msg); err == nil {
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		}
	}
}