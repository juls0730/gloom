package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"plugin"

	"github.com/gofiber/fiber/v3"
)

var pluginPath string
var socketPath string
var controlPath string

func init() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: pluginHost <pluginPath> <socketPath>")
		os.Exit(1)
	}

	pluginPath = os.Args[1]
	socketPath = os.Args[2]
	if len(os.Args) > 3 {
		controlPath = os.Args[3]
	}
}

type Plugin interface {
	Init() (*fiber.Config, error)
	RegisterRoutes(app fiber.Router)
	// Name() string
}

type PluginInstance struct {
	Plugin Plugin
	Name   string
	Path   string
	Router *fiber.App
}

// Init is the entry point for a container process
func (p *PluginInstance) Run(pluginName string) {
	log.Printf("Starting container with plugin %s", pluginName)
	// Load and initialize the plugin here
}

func main() {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)

	go func() {
		<-signalChan
		// TODO: maybe do something graceful here
		fmt.Println("Received SIGINT, shutting down...")
		os.Exit(0)
	}()

	var writer io.Writer
	writer = os.Stderr
	if controlPath != "" {
		fmt.Printf("Waiting for control connection on %s\n", controlPath)

		controlListener, err := net.Listen("unix", controlPath)
		if err != nil {
			log.Fatalf("Error listening on control socket: %v", err)
		}
		defer controlListener.Close()

		conn, err := controlListener.Accept()
		if err != nil {
			log.Printf("Error accepting control connection: %v", err)
			return
		}
		defer conn.Close()

		var ok bool
		writer, ok = conn.(io.Writer)
		if !ok {
			log.Printf("Control connection is not a writer")
			return
		}
	}

	if _, err := os.Stat(socketPath); err == nil {
		fmt.Fprintf(writer, "Error: Socket %s already exists\n", socketPath)
		os.Exit(1)
	}

	realPluginPath, err := filepath.Abs(pluginPath)
	if err != nil {
		fmt.Fprintf(writer, "Error: could not get absolute plugin path: %v\n", err)
		os.Exit(1)
	}

	p, err := plugin.Open(realPluginPath)
	if err != nil {
		fmt.Fprintf(writer, "Error: could not open plugin %s: %v\n", realPluginPath, err)
		os.Exit(1)
	}

	symbol, err := p.Lookup("Plugin")
	if err != nil {
		fmt.Fprintf(writer, "Error: could not find 'Plugin' symbol in %s: %v\n", realPluginPath, err)
		os.Exit(1)
	}

	pluginLib, ok := symbol.(Plugin)
	if !ok {
		fmt.Fprintf(writer, "Error: symbol 'Plugin' in %s is not a Plugin interface\n", realPluginPath)
		os.Exit(1)
	}

	pluginConfig, err := pluginLib.Init()
	if err != nil {
		fmt.Fprintf(writer, "Error: error initializing plugin %s: %v\n", realPluginPath, err)
		os.Exit(1)
	}

	config := fiber.Config{}
	if pluginConfig != nil {
		config = *pluginConfig
	}
	router := fiber.New(config)

	pluginLib.RegisterRoutes(router)

	// listen for connections on the socket
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		fmt.Fprintf(writer, "Error: error listening on socket %s: %v\n", socketPath, err)
		os.Exit(1)
	}

	fmt.Fprintf(writer, "ready\n")

	// technically this can still error
	router.Listener(listener, fiber.ListenConfig{
		DisableStartupMessage: true,
	})
}
