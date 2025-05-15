# GLoom

GLoom is a plugin-based web app manager written in Go (perhaps a pico-paas). GLoom's focus is to provide and simple and efficient way to host micro-web apps easily. Currently, GLoom is a fun little proof of concept, and now even supports unloading plugins, and gracefully handles plugins that crash, but it is not yet ready for production use, and may not ever be. GLoom is still in early development, so expect some rough edges and bugs and at its heart, GLoom is just a proof of concept, fun to write, and fun to use, but not production ready.

## Features

- Plugin-based architecture
- RPC-based communication between GLoom and plugins
- Built-in plugin management system

## Getting Started

### Prerequisites

- Go 1.20 or higher
- [zqdgr](https://github.com/juls0730/zqdgr)

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
    
    and if you want to build the project without the GLoom management Interface (you will not be able to manage plugins wunless you have another interface like GLoomI):
    ```bash
    zqdgr build:no-gloomi
    ```

    and make sure to set the `DISABLE_GLOOMI` environment variable to `true` in the `.env` file.

## Configuring

GLoom is configured using environment variables. The following environment variables are supported:

- `DEBUG` - Enables debug logging. This is a boolean value, so you can set it to any truthy value to enable debug logging.
- `DISABLE_GLOOMI` - Disables the GLoomI plugin. This is a boolean value, so you can set it to any truthy value to disable the GLoomI plugin.
- `PLUGINS_DIR` - The directory where plugins are stored. This is a string value, so you can set it to any directory path you want. The default value is `plugs`.

## Usage

please read [DOCUMENTATION.md](DOCUMENTATION.md)

## Contributing

Contributions are welcome!

## License

GLoom is licensed under the MIT License.
