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
	Init() (*fiber.Config, error)
	RegisterRoutes(app fiber.Router)
	Name() string
}

type PluginInstance struct {
	Plugin Plugin
	Name   string
	Path   string
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

	return gloom, nil
}

func (gloom *GLoom) LoadInitialPlugins() error {
	plugins, err := gloom.DB.Query("SELECT path, domains FROM plugins")
	if err != nil {
		return err
	}
	defer plugins.Close()

	for plugins.Next() {
		var plugin struct {
			Path   string
			Domain string
		}

		if err := plugins.Scan(&plugin.Path, &plugin.Domain); err != nil {
			return err
		}

		domains := strings.Split(plugin.Domain, ",")

		if err := gloom.RegisterPlugin(plugin.Path, domains); err != nil {
			slog.Warn("Failed to register plugin", "pluginPath", plugin.Path, "error", err)
		}
	}

	return nil
}

func (gloom *GLoom) RegisterPlugin(pluginPath string, domains []string) error {
	slog.Info("Registering plugin", "pluginPath", pluginPath, "domains", domains)

	p, err := plugin.Open(pluginPath)
	if err != nil {
		return err
	}

	symbol, err := p.Lookup("Plugin")
	if err != nil {
		return err
	}

	pluginLib, ok := symbol.(Plugin)
	if !ok {
		return fmt.Errorf("plugin is not a Plugin")
	}

	fiberConfig, err := pluginLib.Init()
	if err != nil {
		return err
	}

	if fiberConfig == nil {
		fiberConfig = &fiber.Config{}
	}

	router := fiber.New(*fiberConfig)
	pluginLib.RegisterRoutes(router)

	pluginInstance := PluginInstance{
		Plugin: pluginLib,
		Name:   pluginLib.Name(),
		Path:   pluginPath,
		Router: router,
	}

	gloom.Plugins = append(gloom.Plugins, pluginInstance)
	pluginPtr := &gloom.Plugins[len(gloom.Plugins)-1]
	for _, domain := range domains {
		gloom.domainMap.Store(domain, pluginPtr)
	}

	return nil
}

func (gloom *GLoom) DeletePlugin(pluginName string) {
	gloom.domainMap.Range(func(domain string, plugin *PluginInstance) bool {
		if plugin.Name == pluginName {
			gloom.domainMap.Delete(domain)
		}
		return true
	})
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
	var plugExists bool
	rpc.gloom.DB.QueryRow("SELECT path FROM plugins WHERE path = ?", "plugs/"+plugin.Name).Scan(&plugExists)

	var domains []string
	if plugExists {
		// if plugin exists, we need to not check for domains that this plug has already registered, but instead check for new domains this plugin is registering
		domains = make([]string, 0)
		var existingDomains []string
		err := rpc.gloom.DB.QueryRow("SELECT domains FROM plugins WHERE path = ?", "plugs/"+plugin.Name).Scan(&existingDomains)
		if err != nil {
			return err
		}

		for _, domain := range existingDomains {
			var found bool
			for _, domainToCheck := range plugin.Domains {
				if domain == domainToCheck {
					found = true
					break
				}
			}
			if !found {
				domains = append(domains, domain)
			}

			found = false
		}
	} else {
		domains = plugin.Domains
	}

	for _, domain := range domains {
		_, ok := rpc.gloom.domainMap.Load(domain)
		if ok {
			*reply = fmt.Sprintf("Domain %s already exists", domain)
			return nil
		}
	}

	// regardless of if plugin exists or not, we'll upload the file since this could be an update to an existing plugin
	if err := os.WriteFile(fmt.Sprintf("plugs/%s", plugin.Name), plugin.Data, 0644); err != nil {
		return err
	}

	fmt.Println("Plugin uploaded successfully")

	if plugExists {
		// exit out early otherwise we risk creating multiple of the same plugin and causing undefined behavior
		*reply = "Plugin updated successfully"
		return nil
	}

	if err := rpc.gloom.RegisterPlugin("plugs/"+plugin.Name, plugin.Domains); err != nil {
		slog.Warn("Failed to register uplaoded plguin", "pluginPath", "plugs/"+plugin.Name, "error", err)
		*reply = "Plugin upload failed"
		return nil
	}
	rpc.gloom.DB.Exec("INSERT INTO plugins (path, domains) VALUES (?, ?)", "plugs/"+plugin.Name, strings.Join(plugin.Domains, ","))
	*reply = "Plugin uploaded successfully"
	return nil
}

func (rpc *GloomRPC) DeletePlugin(pluginName string, reply *string) error {
	var targetPlugin PluginInstance
	for _, plugin := range rpc.gloom.Plugins {
		if plugin.Name == pluginName {
			targetPlugin = plugin
			break
		}
	}

	_, err := rpc.gloom.DB.Exec("DELETE FROM plugins WHERE path = ?", targetPlugin.Path)
	if err != nil {
		*reply = "Plugin not found"
		return err
	}

	err = os.Remove(targetPlugin.Path)
	if err != nil {
		*reply = "Plugin not found"
		return err
	}

	rpc.gloom.DeletePlugin(pluginName)

	*reply = "Plugin deleted successfully"
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

	gloom.LoadInitialPlugins()

	if os.Getenv("DISABLE_GLOOMI") != "true" {
		hostname := os.Getenv("GLOOMI_HOSTNAME")
		if hostname == "" {
			hostname = "127.0.0.1"
		}

		if err := gloom.RegisterPlugin("plugs/gloomi.so", []string{hostname}); err != nil {
			panic("Failed to register GLoomI: " + err.Error())
		}
	}

	fmt.Println("Server running at http://localhost:3000")
	if err := app.Listen(":3000"); err != nil {
		panic(err)
	}
}
