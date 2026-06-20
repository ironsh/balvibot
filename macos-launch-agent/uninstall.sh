#!/bin/sh
set -eu

LABEL="com.balvi.balvibot.llama-server"
PLIST_DST="$HOME/Library/LaunchAgents/$LABEL.plist"
SCRIPT_DST="$HOME/Library/Application Support/balvibot/llama-server-launch.sh"
USER_ID="$(id -u)"

launchctl bootout "gui/$USER_ID" "$PLIST_DST" >/dev/null 2>&1 || true
rm -f "$PLIST_DST" "$SCRIPT_DST"

echo "Uninstalled $LABEL"
