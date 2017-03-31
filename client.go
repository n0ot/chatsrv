package chatsrv

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"

	"github.com/satori/go.uuid"
)

type InputMode byte

const (
	InputModeLines = InputMode(iota)
	InputModeBytes
	InputModeRunes
)

const SendBuffSize = 10 // Buffer size of channel for sending data to clients

// Represents a client on the server.
// It is the ClientHandler's responsibility to make sure nothing sends on client.Send
// after the client is stopped, as client.Send will be closed.
type Client struct {
	rw            io.ReadWriteCloser // used to talk to the outside world
	scanner       *bufio.Scanner     // Used to buffer input from the client
	Send          chan []byte        // Bytes sent here will be written to the client
	Recv          chan []byte        // Bytes received by client will be sent here.
	inputMode     InputMode          // Determines how received data is chunked before being send to the Recv channel
	uuid          uuid.UUID
	friendlyName  string                 // ClientHandlers can set this if they know of a better way to display the client in addition to the UUID
	contextRWLock sync.RWMutex           // protects context
	context       map[string]interface{} // Arbetrary information can be stored here
	done          chan struct{}          // Closed when client is finished
	stoppedLock   sync.Mutex             // protects stopped
	stopped       bool                   // True if client has been stopped
	stoppedReason string                 // Reason the client was stopped
}

// NewClient initializes a new client, and
// starts the initial client handler.
// When the initial ClientHandler stops,
// the client will be disconnected.
func NewClient(rw io.ReadWriteCloser, inputMode InputMode, clientHandler ClientHandler) (*Client, error) {
	client := &Client{
		rw:      rw,
		scanner: bufio.NewScanner(rw),
		Send:    make(chan []byte, SendBuffSize),
		Recv:    make(chan []byte),
		uuid:    uuid.NewV4(),
		context: make(map[string]interface{}),
		done:    make(chan struct{}, 1),
	}

	err := client.SetInputMode(inputMode)
	if err != nil {
		return nil, err
	}

	// Connect Send/Recv channels to the io.ReadWriteCloser.
	go client.send()
	go client.receive()
	go client.handle(clientHandler)

	return client, nil
}

// send receives data on the client's Send channel, and sends it to the client.
// It will close client.rw when the client was stopped.
func (client *Client) send() {
	// This method needs to close client.rw when the client is stopped.
	// It takes responsibility for this to prevent writing to rw when it is closed (by someone else).
	defer client.rw.Close()

	for data := range client.Send {
		// Keep sending till data is empty, or there is an error
		for len(data) > 0 {
			n, err := client.rw.Write(data)
			if err != nil {
				log.Printf("Error sending data to client %s: %s\n", client, err)
				client.Stop("Send error")
				return
			}

			if n > len(data) {
				// Shouldn't happen, but if it does, data would be indexed out of bounds
				n = len(data)
			}

			data = data[n:]
		}
	}
}

// receive receives data from the client, and sends it down it's Recv channel.
// Data is chunked, depending on client.InputMode.
// If client.InputMode == InputModeLines, newline characters
// will not be included.
// If the input mode is InputModeBytes,
// bytes will be sent as soon as they are received.
// If more than one byte was read from the client, they will all be sent at once.
func (client *Client) receive() {
	defer close(client.Recv)

	for client.scanner.Scan() {
		select {
		case client.Recv <- client.scanner.Bytes():
		case <-client.done:
			return
		}
	}

	// Check to see if the scanner stopped because of an error.
	// If there was an error, but client is stopped, it happened because
	// client.rw was closed, and the error can be ignored.
	if err := client.scanner.Err(); err != nil && !client.stopped {
		log.Printf("Error while receiving data from client %s: %s\n", client, err)
		client.Stop("Receive error")
	} else {
		client.Stop("Client disconnected")
	}
}

// handle Passes messages between a client and a client handler.
// Waits for the client handler to finish,
// and then stops the client.
func (client *Client) handle(clientHandler ClientHandler) {
	exitReason := clientHandler.Handle(client)
	client.Stop(exitReason)
	reasonStrs := make([]string, 0, 2)
	reasonStrs = append(reasonStrs, fmt.Sprintf("%s exited; reason: %s", client, client.StoppedReason()))
	if client.StoppedReason() != exitReason {
		reasonStrs = append(reasonStrs, fmt.Sprintf("client handler returned: %s", exitReason))
	}
	log.Println(strings.Join(reasonStrs, "; "))
}

func (client *Client) InputMode() InputMode {
	return client.inputMode
}

func (client *Client) SetInputMode(inputMode InputMode) error {
	switch inputMode {
	case InputModeLines:
		client.scanner.Split(bufio.ScanLines)
	case InputModeBytes:
		client.scanner.Split(bufio.ScanBytes)
	case InputModeRunes:
		client.scanner.Split(bufio.ScanRunes)
	default:
		return fmt.Errorf("Chatsrv Client: invalid InputMode")
	}

	client.inputMode = inputMode
	return nil
}

// Stopped returns true if the client was stopped.
func (client *Client) Stopped() bool {
	return client.stopped
}

// StoppedReason returns the reason the client was stopped.
// Returns "" if the client is still running, or if no reason was set.
func (client *Client) StoppedReason() string {
	return client.stoppedReason
}

// Stop stops a client, closing it's ReadWriteCloser.
// Stop is idempotent; calling Stop more than once will have no effect.
// If there is any more data on the send channel to be sent,
// an attempt will be made to send the rest of it before closing the connection.
func (client *Client) Stop(reason string) {
	client.stoppedLock.Lock()
	defer client.stoppedLock.Unlock()
	if client.stopped {
		return
	}

	client.stoppedReason = reason
	close(client.done)
	close(client.Send)
	client.stopped = true
}

func (client *Client) Uuid() uuid.UUID {
	return client.uuid
}

func (client *Client) String() string {
	if "" != client.friendlyName {
		return fmt.Sprintf("Client(%s)", client.friendlyName)
	}

	return fmt.Sprintf("Client(%s)", client.uuid)
}

// FriendlyName Gets the client's friendly name. This is how the client will be identified when converted to a string.
func (client *Client) FriendlyName() string {
	return client.friendlyName
}

// SetFriendlyName specifies how the client should identify itself when being converted to a string. This replaces the default, which is the client's UUID.
// Set this to "" to restore the default.
func (client *Client) SetFriendlyName(friendlyName string) {
	client.friendlyName = friendlyName
}

// GetVar gets a client's custom variable.
// This method is thread safe.
func (client *Client) GetVar(varName string) interface{} {
	client.contextRWLock.RLock()
	value := client.context[varName]
	client.contextRWLock.RUnlock()
	return value
}

// VarExists checks to see if a variable has been set
// This method is thread safe.
func (client *Client) VarExists(varName string) bool {
	client.contextRWLock.RLock()
	_, exists := client.context[varName]
	client.contextRWLock.RUnlock()
	return exists
}

// UnsetVar unsets a variable
// This method is thread safe.
func (client *Client) UnsetVar(varName string) {
	client.contextRWLock.Lock()
	delete(client.context, varName)
	client.contextRWLock.Unlock()
}

// SetVar sets a client's custom variable.
// This method is thread safe.
func (client *Client) SetVar(varName string, value interface{}) {
	client.contextRWLock.Lock()
	client.context[varName] = value
	client.contextRWLock.Unlock()
}

// ClientHandler provides the server's end of the conversation with a client.
// For example, it might echo text they type back to them,
// or patch them into a chat.
//
// ClientHandlers can call other ClientHandlers,
// but will be blocked until the called handler returns.
// Receive from client.Recv, and respond to client.Send.
// When client.Stop(reason string) is called, client.Send is closed.
// It is therefore a good idea to make sure client.Stopped() is false when regaining control from a called ClientHandler
// to prevent panics.
type ClientHandler interface {
	Handle(*Client) string
}

// ClientHandlerFunc is an adapter to use an ordinary function as a ClientHandler.
// If f is a func(*Client) string, ClientHandlerFunc(f)
// is a ClientHandler whose Handle() method calls f.
type ClientHandlerFunc func(*Client) string

// Handle calls f(client)
func (f ClientHandlerFunc) Handle(client *Client) string {
	return f(client)
}
