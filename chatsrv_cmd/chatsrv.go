package main

import (
	"flag"
	"github.com/n0ot/chatsrv"
	"github.com/spf13/viper"
	"io/ioutil"
	"log"
	"os"
	"os/user"
	"path"
)

func init() {
	// Try to get the user's home directory
	usr, err := user.Current()
	if err != nil {
		log.Fatalf("Cannot get a user to fetch the home directory\n")
	}
	if usr.HomeDir == "" {
		log.Fatalf("Cannot get home directory. Reading the wrong configuration file could yield undefined results:\n")
	}

	configFile := flag.String("config", path.Join(usr.HomeDir, ".chatsrv", "conf"), "Location of configuration file")
	flag.Parse()

	viper.SetConfigFile(*configFile)
	viper.SetConfigType("toml")
	viper.SetDefault("tls.useTls", false)
	err = viper.ReadInConfig()
	if err != nil {
		log.Fatalf("Cannot read configuration: %s\n", err)
	}
}

func main() {
	motdFile := os.ExpandEnv(viper.GetString("motdFile"))
	motd, err := ioutil.ReadFile(motdFile)
	if err != nil {
		log.Fatalf("Error reading motd from file. If you don't want a message of the day, create an empty file: %s\n", err)
	}

	config := &chatsrv.ServerConfig{
		BindAddr:   viper.GetString("bindAddr"),
		ServerName: viper.GetString("serverName"),
		Motd:       string(motd),
		UseTls:     viper.GetBool("tls.useTls"),
		CertFile:   os.ExpandEnv(viper.GetString("tls.certFile")),
		KeyFile:    os.ExpandEnv(viper.GetString("tls.keyFile")),
	}

	server := chatsrv.NewServer(config)
	server.Start()
}
