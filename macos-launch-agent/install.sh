#!/bin/sh
set -eu

LABEL="com.balvi.balvibot.llama-server"
SUPPORT_DIR="$HOME/Library/Application Support/balvibot"
LAUNCH_AGENTS_DIR="$HOME/Library/LaunchAgents"
LOG_DIR="$HOME/Library/Logs"
SCRIPT_SRC="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/llama-server-launch.sh"
SCRIPT_DST="$SUPPORT_DIR/llama-server-launch.sh"
PLIST_DST="$LAUNCH_AGENTS_DIR/$LABEL.plist"
USER_ID="$(id -u)"

mkdir -p "$SUPPORT_DIR" "$LAUNCH_AGENTS_DIR" "$LOG_DIR"
install -m 0755 "$SCRIPT_SRC" "$SCRIPT_DST"

cat > "$PLIST_DST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$LABEL</string>

  <key>ProgramArguments</key>
  <array>
    <string>$SCRIPT_DST</string>
  </array>

  <key>RunAtLoad</key>
  <true/>

  <key>KeepAlive</key>
  <true/>

  <key>StandardOutPath</key>
  <string>$LOG_DIR/balvibot-llama-server.out.log</string>

  <key>StandardErrorPath</key>
  <string>$LOG_DIR/balvibot-llama-server.err.log</string>

  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
</dict>
</plist>
EOF

launchctl bootout "gui/$USER_ID" "$PLIST_DST" >/dev/null 2>&1 || true
launchctl bootstrap "gui/$USER_ID" "$PLIST_DST"
launchctl enable "gui/$USER_ID/$LABEL"
launchctl kickstart -k "gui/$USER_ID/$LABEL"

echo "Installed and started $LABEL"
echo "Logs:"
echo "  $LOG_DIR/balvibot-llama-server.out.log"
echo "  $LOG_DIR/balvibot-llama-server.err.log"
