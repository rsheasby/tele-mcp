package main

import (
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Process struct {
	command string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	mu      sync.Mutex
	killed  bool
}

func NewProcess(command string) (*Process, error) {
	return &Process{
		command: command,
	}, nil
}

func (p *Process) Start() error {
	args := strings.Fields(p.command)
	if len(args) == 0 {
		return nil
	}
	
	p.cmd = exec.Command(args[0], args[1:]...)
	
	var err error
	p.stdin, err = p.cmd.StdinPipe()
	if err != nil {
		return err
	}
	
	p.stdout, err = p.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	
	p.stderr, err = p.cmd.StderrPipe()
	if err != nil {
		return err
	}
	
	go p.logStderr()
	
	if err := p.cmd.Start(); err != nil {
		return err
	}
	
	log.Printf("Started process: %s (PID: %d)", p.command, p.cmd.Process.Pid)
	return nil
}

func (p *Process) logStderr() {
	buffer := make([]byte, 1024)
	for {
		n, err := p.stderr.Read(buffer)
		if err != nil {
			return
		}
		if n > 0 {
			log.Printf("Process stderr: %s", string(buffer[:n]))
		}
	}
}

func (p *Process) Kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	if p.killed || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	
	p.killed = true
	
	log.Printf("Killing process: %s (PID: %d)", p.command, p.cmd.Process.Pid)
	
	// Close stdin to signal the process
	if p.stdin != nil {
		p.stdin.Close()
	}
	
	// Give it a moment to exit gracefully
	done := make(chan error, 1)
	go func() {
		done <- p.cmd.Wait()
	}()
	
	select {
	case <-done:
		log.Printf("Process exited gracefully: %s", p.command)
		return
	case <-time.After(2 * time.Second):
		// Force kill
		p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
			log.Printf("Process terminated: %s", p.command)
			return
		case <-time.After(1 * time.Second):
			p.cmd.Process.Signal(syscall.SIGKILL)
			log.Printf("Process killed: %s", p.command)
		}
	}
}