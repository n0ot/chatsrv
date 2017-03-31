This is a simple chat server written in Go. It is a work in progress, and is mainly meant as a toy to help me learn Go.

To use:

* go get github.com/n0ot/chatsrv
* go install github.com/n0ot/chatsrv/chatsrv_cmd
* Create a directory ".chatsrv" in your home directory, and copy chatsrv_cmd/example.conf to $HOME/.chatsrv/conf
* Create a file in $HOME/.chatsrv, called motd, with any text you want to be displayed when users connect.
* Run chatsrv_cmd

To connect, use netcat:

    nc localhost 36362

If you want to use tls, set useTls = true in the configuration, and point certFile and keyFile at your certificate and private key.
[Ncat](https://nmap.org/ncat/) is an improved version of netcat that supports ssl. To connect to a host with ssl, use

    ncat --ssl localhost 36362

In order to prevent man-in-the-middle attacks, the certificate should be verified. If your certificate was signed by a trusted CA, you can just run

    ncat --ssl-verify chatsrv.example.com 36362

Otherwise, you'll need to give the certificate (not the private key) to people who want to connect, and they'll need to type

    ncat --ssl-verify --ssl-trustfile yourcert.pem chatsrv.example.com 36362

Commands are:

* /users: Says who's on the server
* /rooms: Lists the rooms on the server
* /whois [nick]: Displays information about a user; displays information about yourself if nick is omitted.
* /create <roomname> [<topic> [<roompass>]]: Create a room. If roompass is set, the room will be private until it is destroyed. Rooms are destroyed when everyone leaves.
* /join <room> [<roompass>]: Joins a room. Use roompass if the room is private.
* /leave [<reason>]: Leaves a room.
* /quit: Quit from the server.
