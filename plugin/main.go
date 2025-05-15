package main

import "github.com/gofiber/fiber/v3"

type MyPlugin struct{}

func (p *MyPlugin) Init() (*fiber.Config, error) {
	return nil, nil
}

func (p *MyPlugin) RegisterRoutes(router fiber.Router) {
	router.Get("/", func(c fiber.Ctx) error {
		return c.Status(fiber.StatusOK).SendString("Welcome to MyPlugin!")
	})

	router.Get("/hello", func(c fiber.Ctx) error {
		return c.SendString("Hello from MyPlugin!")
	})
}

// Exported symbol
var Plugin MyPlugin
