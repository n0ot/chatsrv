package chatsrv

// Room represents a chat room on the server.
// The creater may or may not be a moderator (is when NewRoom is called).
// The room is closed when there are no more members.
type room struct {
	creater  string
	mods     map[string]struct{}
	users    map[string]struct{} // mods not included
	name     string
	topic    string
	modPass  string // A normal user can become a moderator with this password
	roomPass string // Makes a room private
}
