package chatsrv

import (
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"
)

// Create a set of commands for the server
var internalCommands map[string]commandHandler // Can be run by internal callers, but not by a user
var commands map[string]commandHandler         // User accessible commands

// commandHandler handles a command sent to the server
type commandHandler interface {
	Handle(*server, *serverCommand)
}

// commandHandlerFunc is an adapter to use an ordinary function as a commandHandler.
// If f is a func(*server, *serverCommand), commandHandlerFunc(f)
// is a commandHandler whose Handle() method calls f.
type commandHandlerFunc func(*server, *serverCommand)

// Handle calls f(command)
func (f commandHandlerFunc) Handle(server *server, command *serverCommand) {
	f(server, command)
}

// The server receives a command, and handles it
type serverCommand struct {
	nick          string        // Command's originator
	client        *Client       // Lookup by nick is easier, but impossible if no nick->client mapping exists yet.
	responseChan  chan<- []byte // Replies go here
	command       string        // Name of command to handle
	args          []string      // Arguments sent along with command
	userInitiated bool          // If true, the user typed /command at the keyboard
}

// Initialize the commands and internalCommands map,
// and add each command.
func init() {
	internalCommands = make(map[string]commandHandler)
	commands = make(map[string]commandHandler)

	// Map internal commands
	internalCommands["adduser"] = cmdAdduser
	internalCommands["rmuser"] = cmdRmuser
	internalCommands["say"] = cmdSay

	// Map user accessible commands
	commands["users"] = cmdUsers
	commands["rooms"] = cmdRooms
	commands["create"] = cmdCreate
	commands["join"] = cmdJoin
	commands["leave"] = cmdLeave
	commands["quit"] = cmdQuit
	commands["whois"] = cmdWhois
	commands["nick"] = cmdNick
	commands["me"] = cmdMe
}

// Internal commands

// cmdAdduser adds a user to the server
var cmdAdduser commandHandlerFunc = func(server *server, command *serverCommand) {
	// Convert to lowercase so people can't connect with the same nick with different case.
	// This is only necessary for this map, since case-insensitive dupes will be filtered here.
	if _, exists := server.clients[strings.ToLower(command.nick)]; exists {
		command.responseChan <- []byte("That nick is already taken.\n")
		close(command.responseChan) // Signals client handler to kick user
		return
	}

	server.clients[strings.ToLower(command.nick)] = command.client
	server.userResponseChan[command.nick] = command.responseChan
	command.responseChan <- []byte(fmt.Sprintf("%s\n\nWelcome %s\n", server.config.Motd, command.nick))
}

// cmdRmuser removes a user from the server
var cmdRmuser commandHandlerFunc = func(server *server, command *serverCommand) {
	_, ok := server.clients[strings.ToLower(command.nick)]
	if !ok {
		command.responseChan <- []byte("That user doesn't exist\n")
		return
	}

	reason := strings.Join(command.args, " ")
	if reason == "" {
		reason = "User disconnected"
	}

	roomName := server.userActiveRoom[command.nick]
	if roomName != "" {
		// Remove the user from the room they're in
		leaveRoom(server, command.nick, roomName, reason)
	}

	delete(server.clients, strings.ToLower(command.nick))
	delete(server.userResponseChan, command.nick)
	close(command.responseChan) // Signals client handler to kick user.
}

// cmdSay says something in a room
var cmdSay commandHandlerFunc = func(server *server, command *serverCommand) {
	if len(command.args) < 2 {
		command.responseChan <- []byte("You must specify a room to say something to.\n")
		return
	}

	roomName := command.args[0]
	message := strings.Join(command.args[1:], " ")

	err := sayToRoom(server, roomName, fmt.Sprintf("%s: %s", command.nick, message))
	if err != nil {
		command.responseChan <- []byte(fmt.Sprintf("%s\n", err))
		return
	}
}

// User commands

// cmdUsers Lists users logged onto the server
var cmdUsers commandHandlerFunc = func(server *server, command *serverCommand) {
	if len(server.clients) == 0 {
		command.responseChan <- []byte("Nobody is logged on. And yet, here you are... This shouldn't be happening!\n")
		return
	}

	response := make([]string, 0, len(server.clients)+1)
	response = append(response, "User\tRoom\tLast seen")

	for nickLower, client := range server.clients {
		// Try to get the real case of the nick
		nick, ok := client.GetVar("nick").(string)
		if !ok {
			nick = nickLower
		}

		roomName := server.userActiveRoom[nick]
		lastSeen, err := getLastSeen(server, client)
		if err != nil {
			log.Printf("Error getting last seen value for nick %s, %s, %s\n", nick, client, err)
		}
		response = append(response, fmt.Sprintf("%s\t%s\t%s", nick, roomName, lastSeen))
	}

	command.responseChan <- []byte(strings.Join(response, "\n") + "\n")
}

// cmdRooms Lists all rooms on the server
var cmdRooms commandHandlerFunc = func(server *server, command *serverCommand) {
	if len(server.rooms) == 0 {
		command.responseChan <- []byte("There are no rooms. Why not create the first?\n")
		return
	}

	response := make([]string, 0, len(server.rooms)+1)
	response = append(response, "Rooms:")

	for _, room := range server.rooms {
		var access string
		if room.roomPass != "" {
			access = "private"
		} else {
			access = "public"
		}

		response = append(response, fmt.Sprintf("%s\t%s", room.name, access))
	}

	command.responseChan <- []byte(strings.Join(response, "\n") + "\n")
}

// cmdCreate Creates a new room,
// And adds the creater to it as a moderator
var cmdCreate commandHandlerFunc = func(server *server, command *serverCommand) {
	if len(command.args) < 1 {
		command.responseChan <- []byte("Use /create <name> [<topic> [<roompass>]]\nRoompass will only apply until the room is destroyed.")
		return
	}

	var name, topic, roomPass string
	name = command.args[0]
	if len(command.args) >= 2 {
		topic = command.args[1]
	}
	if len(command.args) >= 3 {
		roomPass = command.args[2]
	}

	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			command.responseChan <- []byte("Room names can contain only letters and numbers\n")
			return
		}
	}

	_, exists := server.rooms[strings.ToLower(name)]
	if exists {
		command.responseChan <- []byte("That room already exists\n")
		return
	}

	room := room{
		creater:  command.nick,
		mods:     make(map[string]struct{}),
		users:    make(map[string]struct{}),
		name:     name,
		topic:    topic,
		roomPass: roomPass,
	}

	oldRoomName, ok := server.userActiveRoom[command.nick]
	if ok {
		err := leaveRoom(server, command.nick, oldRoomName, "")
		if err != nil {
			command.responseChan <- []byte(fmt.Sprintf("Error leaving old room: %s\n", err))
			return
		}
	}

	room.mods[command.nick] = struct{}{}

	server.rooms[strings.ToLower(name)] = &room
	server.userActiveRoom[command.nick] = name

	command.responseChan <- []byte(fmt.Sprintf("Joined %s; topic: %s\n", name, topic))
}

// cmdJoin joins a room
var cmdJoin commandHandlerFunc = func(server *server, command *serverCommand) {
	if len(command.args) < 1 {
		command.responseChan <- []byte("Which room do you want to join?\n")
		return
	}

	roomName := command.args[0]
	var roomPass string
	if len(command.args) >= 2 {
		roomPass = command.args[1]
	}

	room, ok := server.rooms[strings.ToLower(roomName)]
	if !ok {
		command.responseChan <- []byte("That room doesn't exist\n")
		return
	}

	_, isMod := room.mods[command.nick]
	if isMod {
		command.responseChan <- []byte("You are already in that room as a moderator\n")
		return
	}
	_, isUser := room.users[command.nick]
	if isUser {
		command.responseChan <- []byte("You are already in that room\n")
		return
	}

	if room.roomPass != "" {
		if roomPass == "" {
			command.responseChan <- []byte(fmt.Sprintf("That room is private.\nType /join %s <roompass> to get in.\n", room.name))
			return
		} else if room.roomPass != roomPass {
			command.responseChan <- []byte("Wrong password.\n")
			return
		}
	}

	oldRoomName, ok := server.userActiveRoom[command.nick]
	if ok {
		err := leaveRoom(server, command.nick, oldRoomName, "")
		if err != nil {
			command.responseChan <- []byte(fmt.Sprintf("Error leaving old room: %s\n", err))
			return
		}
	}

	room.users[command.nick] = struct{}{}
	server.userActiveRoom[command.nick] = room.name

	err := sayToRoom(server, roomName, fmt.Sprintf("%s has joined the room", command.nick))
	if err != nil {
		command.responseChan <- []byte(fmt.Sprintf("Error while joining room: %s\n", err))
		delete(room.users, command.nick)
		delete(server.userActiveRoom, command.nick)
		return
	}

	command.responseChan <- []byte(fmt.Sprintf("Joined %s; topic: %s\n", room.name, room.topic))
}

// cmdLeave leaves a room
var cmdLeave commandHandlerFunc = func(server *server, command *serverCommand) {
	roomName, ok := server.userActiveRoom[command.nick]
	if !ok {
		command.responseChan <- []byte("You aren't in a room\n")
		return
	}

	err := leaveRoom(server, command.nick, roomName, strings.Join(command.args, " "))
	if err != nil {
		command.responseChan <- []byte(fmt.Sprintf("Error leaving room: %s\n", err))
		return
	}

	command.responseChan <- []byte(fmt.Sprintf("Left %s\n", roomName))
}

// cmdQuit Quits the server.
// Unlike rmuser, cmdQuit specifies that the user manually typed /quit
var cmdQuit commandHandlerFunc = func(server *server, command *serverCommand) {
	var reason []string
	if len(command.args) == 0 {
		reason = append(reason, "User quit")
	} else {
		reason = append(reason, "User quit:")
		reason = append(reason, command.args...)
	}

	newCommand := &serverCommand{
		nick:          command.nick,
		client:        command.client,
		responseChan:  command.responseChan,
		command:       command.command,
		args:          reason,
		userInitiated: command.userInitiated,
	}

	cmdRmuser(server, newCommand)
}

// cmdWhois gets info about a user
var cmdWhois commandHandlerFunc = func(server *server, command *serverCommand) {
	var nick string
	if len(command.args) >= 1 {
		nick = command.args[0]
	} else {
		nick = command.nick
	}

	client, ok := server.clients[strings.ToLower(nick)]
	if !ok {
		command.responseChan <- []byte("That user doesn't exist.\n")
		return
	}

	// Try to get correct case of nick
	if nickCorrect, ok := client.GetVar("nick").(string); ok {
		nick = nickCorrect
	}

	var remoteAddr string
	remoteAddr, ok = client.GetVar("remote_addr").(string)

	roomName := server.userActiveRoom[nick]
	lastSeen, _ := getLastSeen(server, client)

	whoisInfo := make([]string, 0, 4)

	whoisInfo = append(whoisInfo, fmt.Sprintf("User %s:", nick))
	if remoteAddr != "" {
		whoisInfo = append(whoisInfo, remoteAddr)
	}
	if roomName != "" {
		whoisInfo = append(whoisInfo, fmt.Sprintf("Room: %s", roomName))
	}
	if lastSeen != "" {
		whoisInfo = append(whoisInfo, fmt.Sprintf("Last seen: %s", lastSeen))
	}

	command.responseChan <- []byte(strings.Join(whoisInfo, "\n") + "\n")
}

// cmdNick changes a user's nick
var cmdNick commandHandlerFunc = func(server *server, command *serverCommand) {
	if len(command.args) < 1 {
		command.responseChan <- []byte("What do you want to change your nick to?\n")
		return
	}
	nick := command.args[0]
	for _, r := range nick {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			command.responseChan <- []byte("Nicks can contain only letters and numbers\n")
			return
		}
	}

	// Convert to lowercase so people can't connect with the same nick with different case.
	// This is only necessary for this map, since case-insensitive dupes will be filtered here.
	if _, exists := server.clients[strings.ToLower(nick)]; exists {
		command.responseChan <- []byte("That nick is already taken.\n")
		return
	}

	server.clients[strings.ToLower(nick)] = command.client
	delete(server.clients, strings.ToLower(command.nick))
	server.userResponseChan[nick] = command.responseChan
	delete(server.userResponseChan, command.nick)
	command.client.SetVar("nick", nick)

	roomName := server.userActiveRoom[command.nick]
	if roomName != "" {
		// User is in a room
		server.userActiveRoom[nick] = roomName
		room, ok := server.rooms[strings.ToLower(roomName)]
		if ok {
			_, isMod := room.mods[command.nick]
			if isMod {
				delete(room.mods, command.nick)
				room.mods[nick] = struct{}{}
			}
			_, isUser := room.users[command.nick]
			if isUser {
				delete(room.users, command.nick)
				room.users[nick] = struct{}{}
			}
			if room.creater == command.nick {
				room.creater = nick
			}
		}
		sayToRoom(server, roomName, fmt.Sprintf("%s is now known as %s", command.nick, nick))
	} else {
		command.responseChan <- []byte(fmt.Sprintf("You are now known as %s\n", nick))
	}
}

// cmdMe sends an emote in the form "<nick> <message>"
var cmdMe commandHandlerFunc = func(server *server, command *serverCommand) {
	if len(command.args) < 1 {
		command.responseChan <- []byte("Try something like \"/me sits down\" without the quotes.\n")
		return
	}

	action := strings.Join(command.args, " ")
	roomName, ok := server.userActiveRoom[command.nick]
	if !ok {
		command.responseChan <- []byte("You must be in a room to do that.\n")
		return
	}

	sayToRoom(server, roomName, fmt.Sprintf("%s %s", command.nick, action))
}

// Helper functions

// sayToRoom says something to all members in a room
func sayToRoom(server *server, roomName, message string) error {
	room, ok := server.rooms[strings.ToLower(roomName)]
	if !ok {
		return fmt.Errorf("Room doesn't exist")
	}

	message += "\n"

	for nick, _ := range room.mods {
		responseChan := server.userResponseChan[nick]
		if responseChan == nil {
			continue
		}
		responseChan <- []byte(message)
	}
	for nick, _ := range room.users {
		responseChan := server.userResponseChan[nick]
		if responseChan == nil {
			continue
		}
		responseChan <- []byte(message)
	}

	return nil
}

// leaveRoom leaves a room
func leaveRoom(server *server, nick, roomName, reason string) error {
	room, ok := server.rooms[strings.ToLower(roomName)]
	if !ok {
		return fmt.Errorf("Room doesn't exist")
	}

	message := make([]string, 0, 2)
	message = append(message, fmt.Sprintf("%s has left the room", nick))
	if reason != "" {
		message = append(message, fmt.Sprintf(": %s", reason))
	}

	// Nick should be in only one of mods or users;
	// deleting from both will be okay.
	delete(room.mods, nick)
	delete(room.users, nick)
	delete(server.userActiveRoom, nick)

	sayToRoom(server, roomName, strings.Join(message, ""))

	// If the room is empty, delete it.
	if (len(room.mods) + len(room.users)) == 0 {
		delete(server.rooms, strings.ToLower(roomName))
	}

	return nil
}

// getLastSeen Gets the time the server last received anything from the user
func getLastSeen(server *server, client *Client) (string, error) {
	if !client.VarExists("last_seen") {
		return "never", nil
	}

	lastSeenTime, ok := client.GetVar("last_seen").(time.Time)
	if !ok {
		return "", fmt.Errorf("Couldn't get last seen time")
	}

	sinceLastSeen := time.Since(lastSeenTime)

	if (sinceLastSeen / time.Minute) < 1 {
		return "Just now", nil
	}

	if (sinceLastSeen / time.Hour) < 1 {
		numMinutes := sinceLastSeen / time.Minute
		var minutes string
		if numMinutes == 1 {
			minutes = "minute"
		} else {
			minutes = "minutes"
		}

		return fmt.Sprintf("%d %s ago", numMinutes, minutes), nil
	}

	if (sinceLastSeen / time.Hour) < 24 {
		numHours := sinceLastSeen / time.Hour
		numMinutes := (sinceLastSeen / time.Minute) - (numHours * 60)
		var hours string
		if numHours == 1 {
			hours = "hour"
		} else {
			hours = "hours"
		}
		var minutes string
		if numMinutes == 1 {
			minutes = "minute"
		} else {
			minutes = "minutes"
		}

		return fmt.Sprintf("%d %s, %d %s ago", numHours, hours, numMinutes, minutes), nil
	}

	numDays := sinceLastSeen / (time.Hour * 24)
	numHours := (sinceLastSeen / time.Hour) - (numDays * 24)
	numMinutes := (sinceLastSeen / time.Minute) - (((numDays * 24) + numHours) * 60)
	var days string
	if numDays == 1 {
		days = "day"
	} else {
		days = "days"
	}
	var hours string
	if numHours == 1 {
		hours = "hour"
	} else {
		hours = "hours"
	}
	var minutes string
	if numMinutes == 1 {
		minutes = "minute"
	} else {
		minutes = "minutes"
	}

	return fmt.Sprintf("%d %s, %d %s, %d %s ago", numDays, days, numHours, hours, numMinutes, minutes), nil
}
