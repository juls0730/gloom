package main

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/rpc"
	"strings"

	"github.com/gofiber/fiber/v3"
)

type GLoomI struct {
	client *rpc.Client
}

func (p *GLoomI) Init() error {
	// Connect to the RPC server
	client, err := rpc.Dial("tcp", "localhost:7143")
	if err != nil {
		return fmt.Errorf("failed to connect to Gloom RPC server: %w", err)
	}
	p.client = client
	return nil
}

func (p *GLoomI) Name() string {
	return "GLoomI"
}

type PluginData struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
}

func GetPlugins(client *rpc.Client) ([]PluginData, error) {
	var plugins []PluginData
	err := client.Call("GloomRPC.ListPlugins", struct{}{}, &plugins)

	return plugins, err
}

func (p *GLoomI) RegisterRoutes(router fiber.Router) {
	apiRouter := router.Group("/api")
	{
		apiRouter.Get("/plugins", func(c fiber.Ctx) error {
			plugins, err := GetPlugins(p.client)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).SendString("Failed to list plugins: " + err.Error())
			}

			return c.Status(fiber.StatusOK).JSON(plugins)
		})

		type UploadRequest struct {
			Domains string `form:"domains"`
		}

		apiRouter.Post("/plugins", func(c fiber.Ctx) error {
			pluginUpload := new(UploadRequest)
			if err := c.Bind().Form(pluginUpload); err != nil {
				return c.Status(fiber.StatusBadRequest).SendString("Failed to bind form data: " + err.Error())
			}

			if pluginUpload.Domains == "" {
				return c.Status(fiber.StatusBadRequest).SendString("No domains provided")
			}

			domains := make([]string, 0)
			for _, domain := range strings.Split(pluginUpload.Domains, ",") {
				domains = append(domains, strings.TrimSpace(domain))
			}

			var pluginFile *multipart.FileHeader
			pluginFile, err := c.FormFile("plugin")
			if err != nil {
				return c.Status(fiber.StatusBadRequest).SendString("Failed to get plugin file: " + err.Error())
			}

			pluginData, err := pluginFile.Open()
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).SendString("Failed to open plugin file: " + err.Error())
			}

			var pluginUploadStruct struct {
				Domains []string `json:"domains"`
				Name    string   `json:"name"`
				Data    []byte   `json:"data"`
			}
			pluginUploadStruct.Name = pluginFile.Filename
			pluginUploadStruct.Domains = domains
			pluginUploadStruct.Data, err = io.ReadAll(pluginData)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).SendString("Failed to read plugin file: " + err.Error())
			}

			err = p.client.Call("GloomRPC.UploadPlugin", pluginUploadStruct, nil)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).SendString("Failed to upload plugin: " + err.Error())
			}

			return c.Status(fiber.StatusOK).SendString("Plugin uploaded successfully")
		})

		apiRouter.Delete("/plugins/:pluginName", func(c fiber.Ctx) error {
			pluginName := c.Params("pluginName")
			var response string
			err := p.client.Call("GloomRPC.DeletePlugin", pluginName, &response)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).SendString("Failed to list plugins: " + err.Error())
			}

			c.Status(fiber.StatusOK).SendString(response)
			return nil
		})
	}
}

// Exported symbol
var Plugin GLoomI
