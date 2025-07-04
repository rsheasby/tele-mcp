package main

import (
	"io"
	"log"
	"sync"

	"github.com/gorilla/websocket"
)

type Bridge struct {
	conn       *websocket.Conn
	process    *Process
	pool       *ProcessPool
	mcpCommand string
	done       chan struct{}
	wg         sync.WaitGroup
}

func NewBridge(conn *websocket.Conn, pool *ProcessPool, mcpCommand string) *Bridge {
	return &Bridge{
		conn:       conn,
		pool:       pool,
		mcpCommand: mcpCommand,
		done:       make(chan struct{}),
	}
}

func (b *Bridge) Start() error {
	process := b.pool.Get()
	if process == nil {
		var err error
		process, err = NewProcess(b.mcpCommand)
		if err != nil {
			return err
		}
		if err := process.Start(); err != nil {
			return err
		}
	}
	b.process = process
	
	b.wg.Add(2)
	go b.pipeWebSocketToStdin()
	go b.pipeStdoutToWebSocket()
	
	return nil
}

func (b *Bridge) Wait() {
	b.wg.Wait()
}

func (b *Bridge) pipeWebSocketToStdin() {
	defer b.wg.Done()
	defer b.cleanup()
	
	for {
		messageType, message, err := b.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket read error: %v", err)
			}
			return
		}
		
		if messageType == websocket.TextMessage {
			if _, err := b.process.stdin.Write(message); err != nil {
				log.Printf("Write to stdin error: %v", err)
				return
			}
			if _, err := b.process.stdin.Write([]byte("\n")); err != nil {
				log.Printf("Write newline error: %v", err)
				return
			}
		}
	}
}

func (b *Bridge) pipeStdoutToWebSocket() {
	defer b.wg.Done()
	
	buffer := make([]byte, 4096)
	for {
		n, err := b.process.stdout.Read(buffer)
		if err != nil {
			if err != io.EOF {
				log.Printf("Read from stdout error: %v", err)
			}
			return
		}
		
		if n > 0 {
			if err := b.conn.WriteMessage(websocket.TextMessage, buffer[:n]); err != nil {
				log.Printf("WebSocket write error: %v", err)
				return
			}
		}
	}
}

func (b *Bridge) cleanup() {
	close(b.done)
	if b.process != nil {
		b.process.Kill()
	}
}