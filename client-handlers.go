package chatsrv

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/shlex"
)

// Not a ClientHandler itself, but concrete ClientHandlers
// alias this and override Handle().
type defaultClientHandler struct {
	server *server
}

// initServerClientHandler should be passed to NewClient as the initial client handler.
// It asks the client to identify themself, and puts them onto the chat server.
type initServerClientHandler defaultClientHandler

func (ch initServerClientHandler) Handle(client *Client) string {
	exitReason := idClientHandler{ch.server}.Handle(client)
	if client.Stopped() || exitReason != "" {
		return exitReason
	}

	return chatClientHandler{ch.server}.Handle(client)
}

// idClientHandler asks the client for a nick.
// If none is provided, or the nick is invalid, it will  return  the reason.
// Otherwise, it sets "nick" on the client's Context and returns "".
type idClientHandler defaultClientHandler

func (ch idClientHandler) Handle(client *Client) string {
	client.Send <- []byte(fmt.Sprintf("%s\nNick: ", ch.server.config.ServerName))

	data, ok := <-client.Recv
	if !ok {
		return "Interrupted"
	}

	nick := string(data)

	if nick == "" {
		client.Send <- []byte("You must provide a nick\n")
		return "No nick provided"
	}

	for _, r := range nick {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			client.Send <- []byte("Invalid nick; must contain only letters or numbers\n")
			return "Nick must contain only letters or numbers"
		}
	}

	// received a valid nick
	client.SetVar("nick", nick)
	return ""
}

// chatClientHandler connects the client to the chat service
type chatClientHandler defaultClientHandler

func (ch chatClientHandler) Handle(client *Client) string {
	// Add the user to the chat server
	nick, ok := client.GetVar("nick").(string)
	if !ok {
		return "Invalid nick"
	}

	// Responses will be piped from responseChan to client.Send.
	// This is safer than giving the server client.Send, since checks for client.Stopped() can be done here,
	// and the server doesn't have to worry about sending to a closed channel.
	responseChan := make(chan []byte)
	// If the client disconnects before the server is done sending to responseChan,
	// the channel could fill up, which would hang the chat server.
	// Make sure it is empty
	defer func() {
		for _ = range responseChan {
		}
	}()

	// Add this client as a user on the server
	ch.server.in <- &serverCommand{
		nick:         nick,
		client:       client,
		responseChan: responseChan,
		command:      "adduser",
	}

	// Support multiline messages when pasting in text
	// Limitting to a defined number of lines to prevent spamming and filling the memory.
	// Warning: In the name of efficiency, the message slice will not be cleared after each send.
	// Only send message[:messageLineNumber], not the entire slice!
	message := make([]string, ch.server.config.MessageLineLimit)
	messageLineNumber := 0 // Lines start at 0; reset when message is sent.
	messagePasteTimeout := ch.server.config.MessagePasteTimeout

	// Get a timer, but stop it right away, since we don't need it until the user starts sending messages to the room
	messagePasteTimer := time.NewTimer(messagePasteTimeout)
	stopTimerSafely(messagePasteTimer)
	// and make sure the timer is stopped when the client quits.
	defer stopTimerSafely(messagePasteTimer)

	for {
		// Track the client's nick variable, in case the server changes it
		nick, ok = client.GetVar("nick").(string)
		if !ok {
			return "Invalid nick"
		}
		select {
		case data, ok := <-responseChan:
			if !ok {
				// Server closes responseChan to kic a client
				client.Send <- []byte("Goodbye\n")
				return "Disconnected by server"
			}
			client.Send <- data
		case data, ok := <-client.Recv:
			if !ok {
				ch.server.in <- &serverCommand{
					nick:         nick,
					client:       client,
					responseChan: responseChan,
					command:      "rmuser",
				}

				return "User disconnected"
			}

			// Strip all non graphic unicode characters, and convert data to a string
			input := strings.Map(func(r rune) rune {
				if unicode.IsGraphic(r) {
					return r
				}

				return rune(-1)
			}, string(data))
			if strings.HasPrefix(input, "/") {
				// This is a command
				// Stop the message timeout timer, until it is needed again.
				stopTimerSafely(messagePasteTimer)
				// Before executing this command,
				// send the message, if there is one waiting to be sent.
				sendMessage(ch.server, nick, client, responseChan, message[:messageLineNumber])
				messageLineNumber = 0
				args, err := shlex.Split(input)
				if err != nil {
					client.Send <- []byte("Error\n")
				}

				if len(args) < 1 {
					client.Send <- []byte("No command specified\n")
					continue
				}
				commandName := strings.TrimPrefix(args[0], "/")
				args = args[1:]
				ch.server.in <- &serverCommand{
					nick:          nick,
					client:        client,
					responseChan:  responseChan,
					command:       commandName,
					args:          args,
					userInitiated: true,
				}
			} else {
				if input == "" {
					continue // No holding down enter and spamming
				}
				// Another line was received to be sent as a message.
				// Reset the timer and add this line to the message to be sent.
				stopTimerSafely(messagePasteTimer)
				messagePasteTimer.Reset(messagePasteTimeout)
				if messageLineNumber < len(message) {
					message[messageLineNumber] = input
					messageLineNumber++
				} else {
					// No more lines allowed in message.
					// Send what's there and start another message.
					sendMessage(ch.server, nick, client, responseChan, message[:messageLineNumber])
					messageLineNumber = 0
					message[messageLineNumber] = input
					messageLineNumber++
				}
			}
		case <-messagePasteTimer.C:
			// The message paste timeout was exceeded; send the message to the server
			sendMessage(ch.server, nick, client, responseChan, message[:messageLineNumber])
			messageLineNumber = 0
		}
	}

	return "User quit"
}

// Helper functions

// sendMessage sends a message to be sent to a room on the server.
// Only runes which unicode.IsGraphic returns true for will be included.
func sendMessage(server *server, nick string, client *Client, responseChan chan<- []byte, message []string) {
	if len(message) == 0 {
		// Nothing to send
		return
	}
	roomName, ok := server.userActiveRoom[nick]
	if !ok {
		client.Send <- []byte("You'll need to join a room before you can talk.\n/users lists all users, /rooms lists rooms, /join room joins a room,\n/leave leaves the room.\n")
		return
	}

	fullMessage := strings.Join(message, "\n")
	server.in <- &serverCommand{
		nick:         nick,
		client:       client,
		responseChan: responseChan,
		command:      "say",
		args:         []string{roomName, fullMessage},
	}
}

// stopTimerSafely stops a timer and drains it's channel
// in case it expired before it was stopped
func stopTimerSafely(timer *time.Timer) {
	timer.Stop()
	// If the timer was expired,
	// the channel will need to be drained.
	for len(timer.C) > 0 {
		<-timer.C
	}
}
