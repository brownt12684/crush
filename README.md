# Crush — Unrestricted Fork

<p align="center">
    <a href="https://stuff.charm.sh/crush/charm-crush.png"><img width="450" alt="Charm Crush Logo" src="https://github.com/user-attachments/assets/cf8ca3ce-8b02-43f0-9d0f-5a331488da4b" /></a><br />
    <a href="https://github.com/brownt12684/crush/releases"><img src="https://img.shields.io/github/release/brownt12684/crush" alt="Latest Release"></a>
</p>

<p align="center">Your new coding bestie, now available in your favourite terminal.<br />Your tools, your code, and your workflows, wired into your LLM of choice.</p>

## Banned Commands Removed

This fork removes **all command restrictions** from upstream Crush. Every shell command runs without blocking — no more "command is banned" errors. The following categories are fully unrestricted:

| Category | Previously Blocked Commands |
|----------|---------------------------|
| Network / Download tools | `curl`, `wget`, `ssh`, `scp`, `nc`, `telnet`, `firefox`, `chrome`, `safari`, `aria2c`, `axel`, `links`, `lynx`, `w3m`, `curlie`, `http-prompt`, `httpie`, `xh` |
| System administration | `sudo`, `su`, `doas` |
| Package managers (Linux) | `apt`, `apt-get`, `dpkg`, `dnf`, `yum`, `pacman`, `apk`, `zypper`, `rpm`, `brew install`, `cargo install`, `pip install --user`, `npm install -g`, `pnpm add -g`, `yarn global add`, `gem install`, `go install` |
| System modification | `systemctl`, `service`, `crontab`, `at`, `batch`, `mount`, `umount`, `fdisk`, `mkfs`, `parted`, `chkconfig` |
| Network configuration | `ip`, `ifconfig`, `iptables`, `firewall-cmd`, `netstat`, `route`, `pfctl`, `ufw` |

No config changes needed — just install and run.

## Features

- **Multi-Model:** choose from a wide range of LLMs or add your own via OpenAI- or Anthropic-compatible APIs
- **Flexible:** switch LLMs mid-session while preserving context
- **Session-Based:** maintain multiple work sessions and contexts per project
- **LSP-Enhanced:** Crush uses LSPs for additional context, just like you do
- **Extensible:** add capabilities via MCPs (`http`, `stdio`, and `sse`)
- **Unrestricted Bash:** all commands run freely — no banned list

## Installation (Windows)

Download the latest release from [GitHub Releases](https://github.com/brownt12684/crush/releases).

### Quick Start

1. Download the `.zip` file for your architecture (`x86_64` or `arm64`)
2. Extract the archive
3. Run `crush.exe` from PowerShell or Command Prompt

```powershell
# Example: download and extract (PowerShell)
Invoke-WebRequest -Uri "https://github.com/brownt12684/crush/releases/latest/download/crush_<VERSION>_Windows_x86_64.zip" -OutFile "crush.zip"
Expand-Archive -Path crush.zip -DestinationPath .\crush
.\crush\crush.exe
```

### System Requirements

- **OS:** Windows 10 or later (x86_64 / amd64)
- **Shell:** PowerShell, Command Prompt, or Windows Terminal
- **Go runtime:** not required — the binary is statically compiled with `CGO_ENABLED=0`

## API Keys

Crush works with Anthropic, OpenAI, Gemini, OpenRouter, and many more. Press <kbd>ctrl+l</kbd> to open the model picker and choose your provider.

| Environment Variable | Provider |
|---------------------|----------|
| `ANTHROPIC_API_KEY` | Anthropic |
| `OPENAI_API_KEY` | OpenAI |
| `GEMINI_API_KEY` | Google Gemini |
| `OPENROUTER_API_KEY` | OpenRouter |
| `HF_TOKEN` | Hugging Face Inference |
| `GROQ_API_KEY` | Groq |

See [upstream docs](https://github.com/charmbracelet/crush) for the full provider list.

## Configuration

Crush runs great with no configuration. To customize, create a `crushrc` file:

```bash
# Add Ollama as a local provider.
provider add ollama --type ollama --base-url "http://localhost:11434/v1"
model add ollama/llama3.3 --name "Llama 3.3" --context-window 128000

# Auto-approve some tools.
permissions allow view edit
```

Config locations (Windows):

| Priority | Path |
|----------|------|
| 1 | `.\.crushrc` (project-local) |
| 2 | `.\crushrc` (project-local) |
| 3 | `%USERPROFILE%\.config\crush\crushrc` (global) |

## License

Based on [Charmbracelet Crush](https://github.com/charmbracelet/crush). Licensed under FSL-1.1-MIT.
