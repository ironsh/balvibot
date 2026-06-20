# macOS LaunchAgent For llama-server

This directory installs a per-user LaunchAgent that starts `llama-server` on
login with the Qwen GGUF model used by balvibot.

Before installing the LaunchAgent, install the model once so launchd does not
perform the first large download in the background:

```sh
llama-server -hf unsloth/Qwen3.5-9B-GGUF:Q4_K_M
```

Wait for the model download to complete, then stop the server with `Ctrl-C`.

Install and start the LaunchAgent:

```sh
./macos-launch-agent/install.sh
```

The service listens on all interfaces through `--host 0.0.0.0`.

Useful commands:

```sh
launchctl print gui/$(id -u)/com.balvi.balvibot.llama-server
launchctl kickstart -k gui/$(id -u)/com.balvi.balvibot.llama-server
tail -f "$HOME/Library/Logs/balvibot-llama-server.out.log"
tail -f "$HOME/Library/Logs/balvibot-llama-server.err.log"
```

Uninstall:

```sh
./macos-launch-agent/uninstall.sh
```
