// Package chatsrv implements a simple chat server.
package chatsrv

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"net"
	"strings"
	"sync"
	"time"

	"crypto/tls"
)

const acceptBuffSize = 100 // Buffer size of channel for accepting commands

// Contains state for the server
type server struct {
	config           ServerConfig
	rooms            map[string]*room
	clients          map[string]*Client
	userActiveRoom   map[string]string
	userResponseChan map[string]chan<- []byte
	in               chan *serverCommand // Server accepts commands on this channel
	runningLock      sync.Mutex          // protects running
	running          bool
}

type ServerConfig struct {
	ServerName          string
	BindAddr            string
	CertFile            string
	KeyFile             string
	UseTls              bool
	Motd                string
	MessageLineLimit    int
	MessagePasteTimeout time.Duration
}

// NewServer creates a new server with the specified configuration
func NewServer(config *ServerConfig) *server {
	server := server{
		config:           *config,
		rooms:            make(map[string]*room),
		clients:          make(map[string]*Client),
		userActiveRoom:   make(map[string]string),
		userResponseChan: make(map[string]chan<- []byte),
		in:               make(chan *serverCommand, acceptBuffSize),
	}

	return &server
}

// Start starts the chat server on the given host/port
func (server *server) Start() {
	server.runningLock.Lock()
	if server.running {
		server.runningLock.Unlock()
		log.Printf("Server is already running\n")
		return
	}
	server.running = true
	server.runningLock.Unlock()

	var listener net.Listener
	var listenerErr error
	if server.config.UseTls {
		cert, err := tls.LoadX509KeyPair(server.config.CertFile, server.config.KeyFile)
		if err != nil {
			log.Printf("Error loading X.509 key pair: %s\n", err)
			return
		}

		tlsConf := &tls.Config{Certificates: []tls.Certificate{cert}}
		listener, listenerErr = tls.Listen("tcp", server.config.BindAddr, tlsConf)
		if listenerErr != nil {
			log.Printf("Cannot start the server, binding on %s; %s\n", server.config.BindAddr, listenerErr)
			return
		}
		log.Printf("Listening on %s with TLS enabled\n", server.config.BindAddr)
	} else {
		listener, listenerErr = net.Listen("tcp", server.config.BindAddr)
		if listenerErr != nil {
			log.Printf("Cannot start the server, binding on %s; %s\n", server.config.BindAddr, listenerErr)
			return
		}
		log.Printf("Listening on %s\n", server.config.BindAddr)
	}

	defer listener.Close()
	go server.acceptCommands()

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Error accepting connection: %s\n", err)
			continue
		}

		client, err := NewClient(conn, InputModeLines, initServerClientHandler{server})
		if err != nil {
			log.Printf("Error creating client: %s\n", err)
			continue
		}

		remoteAddr, _, err := net.SplitHostPort(conn.RemoteAddr().String())
		remoteHost := getHostFromAddrIfPossible(remoteAddr)
		log.Printf("Connected: %s from %s\n", client, remoteHost)
		client.SetVar("remote_addr", remoteHost)
	}
}

// Receives commands from the server's incoming channel, and processes them.
func (server *server) acceptCommands() {
	for command := range server.in {
		err := server.handleCommand(command)
		if err != nil {
			log.Printf("Error while processing command: %s\n", err)
		}
	}
}

// handleCommand looks up a command in the internalCommands or commands map, found in server-commands.go,
// and if found, runs it.
func (server *server) handleCommand(command *serverCommand) error {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Error processing command %s for %s: %s\n", command.command, command.client, r)
		}
	}()

	if command.nick == "" {
		return fmt.Errorf("No nick supplied in command")
	}
	if command.client == nil {
		return fmt.Errorf("No client supplied in command")
	}

	responseChan := command.responseChan
	if responseChan == nil {
		return fmt.Errorf("Received command, but no response channel; command: %q", command)
	}

	command.client.SetVar("last_seen", time.Now())

	if command.command == "" {
		responseChan <- []byte("No command specified\n")
		return nil
	}

	var handler commandHandler = nil
	// If the command was run internally, it also has access to the internalCommands mapping
	if !command.userInitiated {
		handler = internalCommands[command.command]
	}

	// If a handler was found at this point,
	// don't override it with a user command.
	if handler == nil {
		handler = commands[command.command]
	}

	if handler == nil {
		responseChan <- []byte(fmt.Sprintf("Invalid command: %s\n", command.command))
		return nil
	}

	handler.Handle(server, command)
	return nil
}

// getHostFromAddrIfPossible tries to get the reverse dns host for an address.
// If that isn't possible, it just returns the address.
func getHostFromAddrIfPossible(addr string) string {
	var hosts string
	names, err := net.LookupAddr(addr)
	if err == nil { // No need to report errors; just fallback to IP
		hosts = strings.Join(names, ", ")
	}

	if hosts == "" {
		return addr
	}

	return fmt.Sprintf("%s (%s)", hosts, addr)
}
