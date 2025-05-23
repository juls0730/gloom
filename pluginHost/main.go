package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"plugin"
	"strings"
	"syscall"

	"github.com/gofiber/fiber/v3"
)

var pluginPath string
var socketPath string
var chrootDir string

// Idk why I originally wrote this solution when stderr is literally just the best solution for me, but this
// makes the pluginHost more generally useful outside of GLoom, so I'm keeping it
// TODO: maybe make it a compiler flag, though I'm sure its not making the binary *that* much bigger
var controlPath string

type Plugin interface {
	Init() (*fiber.Config, error)
	RegisterRoutes(app fiber.Router)
}

type PluginInstance struct {
	Plugin Plugin
	Name   string
	Path   string
	Router *fiber.App
}

func main() {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)

	var listener net.Listener
	var router *fiber.App

	go func() {
		<-signalChan
		fmt.Println("Received SIGINT, shutting down...")

		if router != nil {
			if err := router.Shutdown(); err != nil {
				log.Printf("Error: error shutting down router: %v", err)
			}
		}

		if listener != nil {
			if err := listener.Close(); err != nil {
				log.Printf("Error: error closing listener: %v", err)
			}

			os.Remove(socketPath)
		}

		os.Exit(0)
	}()

	fs := flag.NewFlagSet("pluginHost", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: pluginHost <pluginPath> <socketPath> [controlPath]")
	}

	fs.StringVar(&pluginPath, "plugin-path", "", "Path to the plugin")
	fs.StringVar(&socketPath, "socket-path", "", "Path to the socket")
	fs.StringVar(&controlPath, "control-path", "", "Path to the control socket")
	fs.StringVar(&chrootDir, "chroot-dir", "", "Path to the chroot directory")

	err := fs.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: error parsing arguments: %v", err)
		os.Exit(1)
	}
	os.Args = fs.Args()

	if pluginPath == "" {
		fmt.Fprintf(os.Stderr, "Error: plugin path not specified")
		os.Exit(1)
	}

	if socketPath == "" {
		fmt.Fprintf(os.Stderr, "Error: socket path not specified")
		os.Exit(1)
	}

	var Print = func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format, args...)
		fmt.Fprintf(os.Stderr, "\n")
	}

	if controlPath != "" {
		fmt.Printf("Waiting for control connection on %s\n", controlPath)

		controlListener, err := net.Listen("unix", controlPath)
		if err != nil {
			log.Fatalf("Error: error listening on control socket: %v", err)
		}
		defer controlListener.Close()

		conn, err := controlListener.Accept()
		if err != nil {
			log.Printf("Error: error accepting control connection: %v", err)
			return
		}
		defer conn.Close()

		var ok bool
		var writer io.Writer
		writer, ok = conn.(io.Writer)
		// send the one message we can send and kill the connection
		Print = func(format string, args ...any) {
			fmt.Fprintf(writer, format, args...)
			fmt.Fprintf(writer, "\n")
			conn.Close()
			os.Remove(controlPath)
		}
		if !ok {
			log.Printf("Error: control connection is not a writer")
			return
		}
	}

	if _, err := os.Stat(socketPath); err == nil {
		Print("Error: socket %s already exists", socketPath)
		os.Exit(1)
	}

	realPluginPath, err := filepath.Abs(pluginPath)
	if err != nil {
		Print("Error: could not get absolute plugin path: %v", err)
		os.Exit(1)
	}

	if chrootDir != "" {
		if !strings.HasPrefix(socketPath, chrootDir) {
			Print("Error: socket path is not in the chroot directory, but chroot is enabled, and therefore the socket cannot be used by the plugin. This is a GLoom bug, please report it.")
			os.Exit(1)
		}

		// we are chrooting and changing to nobody to "sandbox" the plugin
		// nobodyUser, err := user.Lookup("nobody")
		// if err != nil {
		// 	Print("Error: failed to get nobody user: %w", err)
		// }

		// nobodyUid, err := strconv.Atoi(nobodyUser.Uid)
		// if err != nil {
		// 	Print("Error: failed to parse nobody uid: %w", err)
		// }

		// nobodyGid, err := strconv.Atoi(nobodyUser.Gid)
		// if err != nil {
		// 	Print("Error: failed to parse nobody gid: %w", err)
		// }

		pluginData, err := os.ReadFile(realPluginPath)
		if err != nil {
			Print("Error: failed to read plugin: %w", err)
		}

		pluginFileName := filepath.Base(realPluginPath)

		// copy the plugin to the chroot directory
		if err := os.WriteFile(filepath.Join(chrootDir, pluginFileName), pluginData, 0644); err != nil {
			Print("Error: failed to copy plugin to chroot: %w", err)
		}

		// if err := os.Chown(chrootDir, nobodyUid, nobodyGid); err != nil {
		// 	Print("Error: failed to chown chroot directory: %w", err)
		// }

		realPluginPath = "/" + pluginFileName
		socketPath = "/" + filepath.Base(socketPath)

		if err := syscall.Chroot(chrootDir); err != nil {
			Print("Error: failed to chroot: %w", err)
		}

		// if err := syscall.Setgid(nobodyGid); err != nil {
		// 	Print("Error: failed to setgid: %w", err)
		// }

		// if err := syscall.Setuid(nobodyUid); err != nil {
		// 	Print("Error: failed to setuid: %w", err)
		// }
	}

	p, err := plugin.Open(realPluginPath)
	if err != nil {
		Print("Error: could not open plugin %s: %v", realPluginPath, err)
		os.Exit(1)
	}

	symbol, err := p.Lookup("Plugin")
	if err != nil {
		Print("Error: could not find 'Plugin' symbol in %s: %v", realPluginPath, err)
		os.Exit(1)
	}

	pluginLib, ok := symbol.(Plugin)
	if !ok {
		Print("Error: symbol 'Plugin' in %s is not a Plugin interface", realPluginPath)
		os.Exit(1)
	}

	pluginConfig, err := pluginLib.Init()
	if err != nil {
		Print("Error: error initializing plugin %s: %v", realPluginPath, err)
		os.Exit(1)
	}

	config := fiber.Config{}
	if pluginConfig != nil {
		config = *pluginConfig
	}
	router = fiber.New(config)

	pluginLib.RegisterRoutes(router)

	// listen for connections on the socket
	listener, err = net.Listen("unix", socketPath)
	if err != nil {
		Print("Error: error listening on socket %s: %v", socketPath, err)
		os.Exit(1)
	}

	if err := router.Listener(listener, fiber.ListenConfig{
		DisableStartupMessage: true,
		BeforeServeFunc: func(app *fiber.App) error {
			Print("ready")
			return nil
		},
	}); err != nil {
		Print("Error: error starting server: %v", err)
		os.Exit(1)
	}
}
