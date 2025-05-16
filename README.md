# GLoom

GLoom is a plugin-based web app manager written in Go (perhaps a pico-paas). GLoom's focus is to provide and simple and efficient way to host micro-web apps easily. Currently, GLoom is a fun little proof of concept, and now even supports unloading plugins, and gracefully handles plugins that crash, but it is not yet ready for production use, and may not ever be. GLoom is still in early development, so expect some rough edges and bugs and at its heart, GLoom is just a proof of concept, fun to write, and fun to use, but not production ready. If you want a more stable and production ready web app manager, check out my other project, [Flux](https://github.com/juls0730/flux) ;). 

## Features

- Plugin-based architecture
- RPC-based communication between GLoom and plugins
- Built-in plugin management system

## Getting Started

### Prerequisites

- Go 1.20 or higher
- [zqdgr](https://github.com/juls0730/zqdgr)

This project is primarily written for Linux, and has only been tested on Linux, you might have luck with other operating systems, but it is not guaranteed to work, feel free to open an issue if you encounter any problems.

### Installation

1. Clone the repository:
    ```bash
    git clone https://github.com/juls0730/gloom.git
    ```

2. Run the project:
    ```bash
    zqdgr run
    ```

    or if you want to build the project:
    ```bash
    zqdgr build
    ```
    
    and if you want to build the project without the GLoom management Interface (you will not be able to manage plugins wunless you have another interface like GLoomI or mark all the plugins you want to use as preloaded):
    ```bash
    zqdgr build:gloom
    ```

    This will give you the same standalone binary that you would get if you ran `zqdgr build`, but without the GLoom
    management interface and with a blank config file.

## Configuring

GLoom is primarily configured through the config.toml file, but there are a few environment variables that can be used
to confgiure GLoom. The following environment variables are supported: 

- `DEBUG` - Enables debug logging. This is a boolean value, so you can set it to any truthy value to enable debug
  logging.
- `GLOOM_DIR` - The directory where GLoom stores its data. plugins will be stored in this directory, the GLoom config
  will be stored in this directory, and the pluginHost binary will be unpacked into this directory. the default value is
  `$XDG_DATA_HOME/gloom` if it exists, otherwise `$HOME/.local/share/gloom`.

GLoom will also use a config file in the `GLOOM_DIR` to configure plugins and other settings. The config file is a toml file, and the default config is:

```toml
[[plugins]]
file = "gloomi.so"
domains = ["localhost"]
```

The `[[plugins]]` array is a list of plugins to preload. Each plugin is an object with the following fields:

- `file` - The path to the plugin file. This is a string value.
- `domains` - An array of domains to forward requests to this plugin. This is an array of strings.


## Usage

please read [DOCUMENTATION.md](DOCUMENTATION.md)

## Contributing

Contributions are welcome!

## License

GLoom is licensed under the MIT License and ever file is licensed under it unless otherwise specified.
