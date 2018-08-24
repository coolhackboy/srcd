package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"unicode"
	"path/filepath"

	"github.com/srchain/srcd/cmd/utils"
	"github.com/srchain/srcd/node"
	"github.com/naoina/toml"

	"gopkg.in/urfave/cli.v1"
)

var (
	configFileFlag = cli.StringFlag{
		Name:  "config",
		Usage: "TOML configuration file",
	}
)

type config struct {
	Node      node.Config		// like eth
	Peer      peer.Config		// like peer
	Dashboard dashboard.Config
}

func loadConfig(file string, cfg *gethConfig) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()

	err = tomlSettings.NewDecoder(bufio.NewReader(f)).Decode(cfg)
	// Add file name to errors that have a line number.
	if _, ok := err.(*toml.LineError); ok {
		err = errors.New(file + ", " + err.Error())
	}
	return err
}

func makeConfig(ctx *cli.Context) *config {
	// Default config.
	cfg := config{
		Node:      node.DefaultConfig,
		Peer:      peer.DefaultConfig,
		Dashboard: dashboard.DefaultConfig,
	}

	// Load config file.
	if file := ctx.GlobalString(configFileFlag.Name); file != "" {
		if err := loadConfig(file, &cfg); err != nil {
			utils.Fatalf("%v", err)
		}
	}

	// Apply flags.
	utils.SetNodeConfig(ctx, &cfg.Node)
	utils.SetPeerConfig(ctx, &cfg.Peer)
	utils.SetDashboardConfig(ctx, &cfg.Dashboard)

	return &cfg
}

func makePeer(ctx *cli.Context) *peer.Peer {
	cfg := makeConfig(ctx)

	peer := peer.New(&cfg.Peer)

	return peer
}