package main

import (
	"github.com/hellofresh/janus/cmd"
	"github.com/hellofresh/janus/loghook"
	log "github.com/sirupsen/logrus"
)

func init() {
	log.AddHook(loghook.NewContextHook())
}

var version = "0.0.0-dev"

func main() {
	rootCmd := cmd.NewRootCmd(version)

	if err := rootCmd.Execute(); err != nil {
		log.WithError(err).Fatal("Could not run command")
	}
}
