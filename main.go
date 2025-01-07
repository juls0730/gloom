package main

import (
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"net"
	"net/rpc"
	"os"
	"plugin"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/logger"
	"github.com/joho/godotenv"
	"github.com/juls0730/gloom/libs"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var embeddedAssets embed.FS

type Plugin interface {
	Init() error
	Name() string
	RegisterRoutes(app fiber.Router)
}

type PluginInstance struct {
	Plugin Plugin
	Name   string
	Router *fiber.App
}

type GLoom struct {
	Plugins   []PluginInstance
	domainMap libs.SyncMap[string, *PluginInstance]
	DB        *sql.DB
	fiber     *fiber.App
}

func NewGloom(app *fiber.App) (*GLoom, error) {
	if err := os.MkdirAll("plugs", 0755); err != nil {
		if os.IsNotExist(err) {
			panic(err)
		}
	}

	db, err := sql.Open("sqlite3", "gloom.db")
	if err != nil {
		return nil, err
	}

	schema, err := embeddedAssets.ReadFile("schema.sql")
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(string(schema))
	if err != nil {
		return nil, err
	}

	gloom := &GLoom{
		Plugins:   []PluginInstance{},
		domainMap: libs.SyncMap[string, *PluginInstance]{},
		DB:        db,
		fiber:     app,
	}

	plugins, err := db.Query("SELECT path, domains FROM plugins")
	if err != nil {
		return nil, err
	}
	defer plugins.Close()

	for plugins.Next() {
		var plugin struct {
			Path   string
			Domain string
		}

		if err := plugins.Scan(&plugin.Path, &plugin.Domain); err != nil {
			return nil, err
		}

		domains := strings.Split(plugin.Domain, ",")

		gloom.RegisterPlugin(plugin.Path, domains)
	}

	return gloom, nil
}

func (gloom *GLoom) RegisterPlugin(pluginPath string, domains []string) {
	slog.Info("Registering plugin", "pluginPath", pluginPath, "domains", domains)
	p, err := plugin.Open(pluginPath)
	if err != nil {
		panic(err)
	}

	symbol, err := p.Lookup("Plugin")
	if err != nil {
		panic(err)
	}

	pluginLib, ok := symbol.(Plugin)
	if !ok {
		panic("Plugin is not a Plugin")
	}

	err = pluginLib.Init()
	if err != nil {
		panic(err)
	}

	router := fiber.New()
	pluginLib.RegisterRoutes(router)

	pluginInstance := PluginInstance{
		Plugin: pluginLib,
		Name:   pluginLib.Name(),
		Router: router,
	}

	gloom.Plugins = append(gloom.Plugins, pluginInstance)
	pluginPtr := &gloom.Plugins[len(gloom.Plugins)-1]
	for _, domain := range domains {
		gloom.domainMap.Store(domain, pluginPtr)
	}
}

func (gloom *GLoom) StartRPCServer() error {
	rpcServer := &GloomRPC{gloom: gloom}
	err := rpc.Register(rpcServer)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", ":7143")
	if err != nil {
		return err
	}

	fmt.Printf("RPC server running on port 7143\n")
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				fmt.Println("RPC connection error:", err)
				continue
			}
			go rpc.ServeConn(conn)
		}
	}()

	return nil
}

type GloomRPC struct {
	gloom *GLoom
}

type PluginData struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
}

// Example RPC method: List all registered plugins
func (rpc *GloomRPC) ListPlugins(_ struct{}, reply *[]PluginData) error {
	var plugins []PluginData = make([]PluginData, 0)
	var domains map[string][]string = make(map[string][]string)

	rpc.gloom.domainMap.Range(func(domain string, plugin *PluginInstance) bool {
		domains[plugin.Name] = append(domains[plugin.Name], domain)
		return true
	})

	for _, plugin := range rpc.gloom.Plugins {
		var pluginDataStruct PluginData
		pluginDataStruct.Name = plugin.Name
		pluginDataStruct.Domains = domains[plugin.Name]

		plugins = append(plugins, pluginDataStruct)
	}

	*reply = plugins
	return nil
}

type PluginUpload struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
	Data    []byte   `json:"data"`
}

func (rpc *GloomRPC) UploadPlugin(plugin PluginUpload, reply *string) error {
	slog.Info("Uploading plugin", "plugin", plugin.Name, "domains", plugin.Domains)
	if err := os.WriteFile(fmt.Sprintf("plugs/%s", plugin.Name), plugin.Data, 0644); err != nil {
		return err
	}

	fmt.Print("Plugin uploaded successfully")

	rpc.gloom.DB.Exec("INSERT INTO plugins (path, domains) VALUES (?, ?)", "plugs/"+plugin.Name, strings.Join(plugin.Domains, ","))
	rpc.gloom.RegisterPlugin("plugs/"+plugin.Name, plugin.Domains)
	*reply = "Plugin uploaded successfully"
	return nil
}

func init() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env file found")
	}
}

func main() {
	app := fiber.New(fiber.Config{
		BodyLimit: 1024 * 1024 * 1024 * 5, // 5GB
	})

	app.Use(logger.New(logger.Config{
		CustomTags: map[string]logger.LogFunc{
			"app": func(output logger.Buffer, c fiber.Ctx, data *logger.Data, extraParam string) (int, error) {
				output.WriteString(c.Host())
				return len(output.Bytes()), nil
			},
		},
		Format: " ${time} | ${status} | ${latency} | ${ip} | ${method} | ${app} | ${path}\n",
	}))

	gloom, err := NewGloom(app)
	if err != nil {
		panic(err)
	}

	app.Use(func(c fiber.Ctx) error {
		host := c.Host()
		if plugin, ok := gloom.domainMap.Load(host); ok {
			plugin.Router.Handler()(c.RequestCtx())
			return nil
		}

		return c.Status(404).SendString("Domain not found")
	})

	if err := gloom.StartRPCServer(); err != nil {
		panic("Failed to start RPC server: " + err.Error())
	}

	if os.Getenv("DISABLE_GLOOMI") != "true" {
		hostname := os.Getenv("GLOOMI_HOSTNAME")
		if hostname == "" {
			hostname = "127.0.0.1"
		}

		gloom.RegisterPlugin("plugs/gloomi.so", []string{hostname})
	}

	fmt.Println("Server running at http://localhost:3000")
	if err := app.Listen(":3000"); err != nil {
		panic(err)
	}
}
