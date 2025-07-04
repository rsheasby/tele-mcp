package main

import (
	"log"
	"sync"
	"time"
)

type ProcessPool struct {
	command   string
	size      int
	pool      chan *Process
	mu        sync.Mutex
	shutdown  chan struct{}
	wg        sync.WaitGroup
}

func NewProcessPool(command string, size int) (*ProcessPool, error) {
	p := &ProcessPool{
		command:  command,
		size:     size,
		pool:     make(chan *Process, size),
		shutdown: make(chan struct{}),
	}
	
	if size > 0 {
		log.Printf("Pre-spawning %d processes", size)
		for i := 0; i < size; i++ {
			if err := p.spawn(); err != nil {
				log.Printf("Failed to spawn process %d: %v", i, err)
			}
		}
		
		p.wg.Add(1)
		go p.maintainPool()
	}
	
	return p, nil
}

func (p *ProcessPool) spawn() error {
	process, err := NewProcess(p.command)
	if err != nil {
		return err
	}
	
	if err := process.Start(); err != nil {
		return err
	}
	
	select {
	case p.pool <- process:
		return nil
	default:
		process.Kill()
		return nil
	}
}

func (p *ProcessPool) Get() *Process {
	select {
	case process := <-p.pool:
		return process
	default:
		return nil
	}
}

func (p *ProcessPool) maintainPool() {
	defer p.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-p.shutdown:
			return
		case <-ticker.C:
			p.mu.Lock()
			current := len(p.pool)
			needed := p.size - current
			p.mu.Unlock()
			
			if needed > 0 {
				log.Printf("Replenishing pool: %d processes needed", needed)
				for i := 0; i < needed; i++ {
					if err := p.spawn(); err != nil {
						log.Printf("Failed to spawn process: %v", err)
					}
				}
			}
		}
	}
}

func (p *ProcessPool) Shutdown() {
	close(p.shutdown)
	p.wg.Wait()
	
	close(p.pool)
	for process := range p.pool {
		process.Kill()
	}
}