package chatsrv

import (
	"strings"
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
	client.Send <- []byte("Nick: ")

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

	ch.server.in <- &serverCommand{
		nick:         nick,
		client:       client,
		responseChan: responseChan,
		command:      "adduser",
	}

	for {
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

			input := string(data)
			if strings.HasPrefix(input, "/") {
				// This is a command
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
				roomName, ok := ch.server.userActiveRoom[nick]
				if !ok {
					client.Send <- []byte("You'll need to join a room before you can talk.\n/users lists all users, /rooms lists rooms, /join room joins a room,\n/leave leaves the room.\n")
					continue
				}
				ch.server.in <- &serverCommand{
					nick:         nick,
					client:       client,
					responseChan: responseChan,
					command:      "say",
					args:         []string{roomName, input},
				}
			}
		}
	}

	return "User quit"
}
