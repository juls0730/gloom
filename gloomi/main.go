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

func (p *GLoomI) Init() (*fiber.Config, error) {
	// Connect to the RPC server
	client, err := rpc.Dial("tcp", "127.0.0.1:7143")
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Gloom RPC server: %w", err)
	}
	p.client = client
	return &fiber.Config{
		BodyLimit: 1024 * 1024 * 1024 * 5, // 5GiB
	}, nil
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
				fmt.Printf("Loaded plugins: %+v\n", plugins)
				return c.Status(fiber.StatusInternalServerError).SendString("Failed to list plugins: " + err.Error())
			}

			return c.Status(fiber.StatusOK).JSON(plugins)
		})

		type UploadRequest struct {
			Name    string `form:"name"`
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

			if pluginUpload.Name == "" {
				return c.Status(fiber.StatusBadRequest).SendString("No name provided")
			}

			// check if string is alphanumeric
			for _, char := range pluginUpload.Name {
				if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_') {
					return c.Status(fiber.StatusBadRequest).SendString("Invalid name provided")
				}
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
				FileName string   `json:"fileName"`
				Name     string   `json:"name"`
				Domains  []string `json:"domains"`
				Data     []byte   `json:"data"`
			}
			pluginUploadStruct.FileName = pluginFile.Filename
			pluginUploadStruct.Name = pluginUpload.Name
			pluginUploadStruct.Domains = domains
			pluginUploadStruct.Data, err = io.ReadAll(pluginData)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).SendString("Failed to read plugin file: " + err.Error())
			}

			reply := new(string)
			err = p.client.Call("GloomRPC.UploadPlugin", pluginUploadStruct, reply)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).SendString("Failed to upload plugin: " + err.Error())
			}

			return c.Status(fiber.StatusOK).SendString(*reply)
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
