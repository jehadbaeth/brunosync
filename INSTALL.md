# Installing brunosync

brunosync is a single file. There is nothing to build and no runtime to install.

## macOS and Linux

```sh
curl -fsSL https://raw.githubusercontent.com/jehadbaeth/brunosync/main/install.sh | sh
```

The script picks the right binary for your OS and CPU, checks it against the published SHA256 checksum and puts it in `~/.local/bin`. Two optional settings:

```sh
# a specific version
curl -fsSL https://raw.githubusercontent.com/jehadbaeth/brunosync/main/install.sh | BRUNOSYNC_VERSION=v0.1.0 sh
# another directory
curl -fsSL https://raw.githubusercontent.com/jehadbaeth/brunosync/main/install.sh | BRUNOSYNC_INSTALL_DIR=/usr/local/bin sh
```

If the script says `~/.local/bin` is not on your `PATH`, add `export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc` or `~/.bashrc`.

### Manual download

Pick the file for your machine on the [releases page](https://github.com/jehadbaeth/brunosync/releases/latest):

| Machine | File |
|---|---|
| Mac with Apple Silicon (M1 and later) | `brunosync-darwin-arm64` |
| Mac with Intel CPU | `brunosync-darwin-amd64` |
| Linux x86_64 | `brunosync-linux-amd64` |
| Linux ARM64 | `brunosync-linux-arm64` |
| Windows | `brunosync-windows-amd64.exe` |

```sh
chmod +x brunosync-darwin-arm64
mv brunosync-darwin-arm64 ~/.local/bin/brunosync
```

**macOS Gatekeeper:** the binary is not signed by Apple. A file downloaded with a browser is quarantined and macOS refuses to open it ("cannot be verified"). Clear the flag once:

```sh
xattr -d com.apple.quarantine ~/.local/bin/brunosync
```

The install script uses curl, which does not set that flag, so this only matters for browser downloads.

## Windows

In PowerShell:

```powershell
$dir = "$env:LOCALAPPDATA\brunosync"
New-Item -ItemType Directory -Force $dir | Out-Null
Invoke-WebRequest https://github.com/jehadbaeth/brunosync/releases/latest/download/brunosync-windows-amd64.exe -OutFile "$dir\brunosync.exe"
[Environment]::SetEnvironmentVariable("Path", "$env:Path;$dir", "User")
```

Open a new terminal afterwards so the `Path` change applies.

## With Go

If you have Go 1.26 or newer:

```sh
go install github.com/jehadbaeth/brunosync/cmd/brunosync@latest
```

## Check

```sh
brunosync version
```

## Requirements

* `git`, only when the config uses `git:` sources or the `git` output.
* Bruno v3 or newer to open the generated collection (it uses the OpenCollection YAML format).

## Updating

Run the install command again. Your config and collection are not touched.
