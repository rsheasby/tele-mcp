package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
)

var (
	mcpCommand  string
	bootCommand string
	port        int
	wsPath      string
	poolSize    int
	upgrader    = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
	pool *ProcessPool
)

func main() {
	parseConfig()
	
	// Run boot command if specified
	if bootCommand != "" {
		log.Printf("Running boot command: %s", bootCommand)
		if err := runBootCommand(); err != nil {
			log.Printf("Warning: boot command failed: %v", err)
		}
	}
	
	log.Printf("Starting tele-mcp on port %d with path %s", port, wsPath)
	log.Printf("MCP command: %s", mcpCommand)
	log.Printf("Pool size: %d", poolSize)
	
	var err error
	pool, err = NewProcessPool(mcpCommand, poolSize)
	if err != nil {
		log.Fatalf("Failed to create process pool: %v", err)
	}
	defer pool.Shutdown()
	
	http.HandleFunc(wsPath, handleWebSocket)
	
	addr := fmt.Sprintf(":%d", port)
	log.Printf("Listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func parseConfig() {
	flag.StringVar(&mcpCommand, "command", "", "MCP command to execute")
	flag.StringVar(&bootCommand, "boot", "", "Command to run once on startup")
	flag.IntVar(&port, "port", 8080, "WebSocket server port")
	flag.StringVar(&wsPath, "path", "/ws", "WebSocket endpoint path")
	flag.IntVar(&poolSize, "pool", 0, "Process pool size")
	flag.Parse()
	
	if envCmd := os.Getenv("MCP_COMMAND"); envCmd != "" {
		mcpCommand = envCmd
	}
	if envBoot := os.Getenv("BOOT_COMMAND"); envBoot != "" {
		bootCommand = envBoot
	}
	if envPort := os.Getenv("PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			port = p
		}
	}
	if envPath := os.Getenv("WS_PATH"); envPath != "" {
		wsPath = envPath
	}
	if envPool := os.Getenv("POOL_SIZE"); envPool != "" {
		if p, err := strconv.Atoi(envPool); err == nil {
			poolSize = p
		}
	}
	
	if mcpCommand == "" {
		log.Fatal("MCP_COMMAND environment variable or -command flag must be set")
	}
	
	if poolSize > 10 {
		poolSize = 10
		log.Printf("Pool size capped at 10")
	}
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()
	
	log.Printf("New WebSocket connection from %s", r.RemoteAddr)
	
	bridge := NewBridge(conn, pool, mcpCommand)
	if err := bridge.Start(); err != nil {
		log.Printf("Bridge error: %v", err)
		return
	}
	
	bridge.Wait()
	log.Printf("WebSocket connection closed from %s", r.RemoteAddr)
}

func runBootCommand() error {
	args := strings.Fields(bootCommand)
	if len(args) == 0 {
		return nil
	}
	
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	
	return cmd.Run()
}