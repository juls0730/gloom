package main

import (
	"fmt"
	"os"
	"plugin"
	"sync"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/logger"
)

type Plugin interface {
	Name() string
	Init() error
	Domains() []string
	RegisterRoutes(app fiber.Router)
}

var domainMap sync.Map // Maps domains to plugins

func main() {
	app := fiber.New()

	app.Use(logger.New())

	plugins := loadPlugins()

	for _, p := range plugins {
		for _, domain := range p.Domains() {
			fmt.Printf("Registering domain: %s for plugin: %s\n", domain, p.Name())
			domainMap.Store(domain, p)
		}
	}

	app.Use(func(c fiber.Ctx) error {
		host := c.Host()
		if value, ok := domainMap.Load(host); ok {
			plugin := value.(Plugin)

			pluginRouter := fiber.New()
			plugin.RegisterRoutes(pluginRouter)

			pluginRouter.Handler()(c.RequestCtx())
			return nil
		}

		return c.Status(404).SendString("Domain not found")
	})

	fmt.Println("Server running at http://localhost:3000")
	app.Listen(":3000")
}

func loadPlugins() []Plugin {
	if err := os.MkdirAll("plugs", 0755); err != nil {
		if os.IsNotExist(err) {
			panic(err)
		}
	}

	pluginPaths := []string{"plugs/example.so"}
	var plugins []Plugin

	for _, path := range pluginPaths {
		p, err := plugin.Open(path)
		if err != nil {
			panic(err)
		}

		symbol, err := p.Lookup("Plugin")
		if err != nil {
			panic(err)
		}

		pluginInstance, ok := symbol.(Plugin)
		if !ok {
			panic("Invalid plugin type")
		}

		err = pluginInstance.Init()
		if err != nil {
			panic(err)
		}

		plugins = append(plugins, pluginInstance)
	}

	return plugins
}
