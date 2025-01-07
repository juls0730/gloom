package main

import "github.com/gofiber/fiber/v3"

type MyPlugin struct{}

func (p MyPlugin) Name() string {
	return "my plugin"
}

func (p MyPlugin) Init() error {
	return nil
}

func (p MyPlugin) Domains() []string {
	return []string{"myplugin.local"}
}

func (p MyPlugin) RegisterRoutes(router fiber.Router) {
	router.Get("/", func(c fiber.Ctx) error {
		return c.SendString("Welcome to MyPlugin!")
	})

	router.Get("/hello", func(c fiber.Ctx) error {
		return c.SendString("Hello from MyPlugin!")
	})
}

// Exported symbol
var Plugin MyPlugin
