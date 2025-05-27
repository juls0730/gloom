package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofiber/fiber/v3"
)

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

	router.Get("/dir/:path?", func(c fiber.Ctx) error {
		// Get the directory path from the URL parameter
		// If the parameter is empty, default to the current working directory.
		dirPath := c.Params("path")
		if dirPath == "" {
			var err error
			dirPath, err = os.Getwd() // Get current working directory
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
					"error": "Failed to get current working directory",
				})
			}
		}

		// Ensure the path is absolute for security and clarity
		absPath, err := filepath.Abs(dirPath)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("Failed to get absolute path for %s: %v", dirPath, err),
			})
		}

		// Read the directory contents
		files, err := os.ReadDir(absPath)
		if err != nil {
			// Handle different error types for better feedback
			if os.IsNotExist(err) {
				return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
					"error": fmt.Sprintf("Directory not found: %s", absPath),
				})
			}
			if os.IsPermission(err) {
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
					"error": fmt.Sprintf("Permission denied to access directory: %s", absPath),
				})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": fmt.Sprintf("Failed to read directory %s: %v", absPath, err),
			})
		}

		// Extract file names and prepare the response
		var fileNames []string
		for _, file := range files {
			fileNames = append(fileNames, file.Name())
		}

		// Return the list of file names as JSON
		return c.JSON(fiber.Map{
			"directory": absPath,
			"files":     fileNames,
		})
	})
}

// Exported symbol
var Plugin MyPlugin
