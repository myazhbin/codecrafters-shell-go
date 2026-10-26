#!/bin/bash
SOUND_DIR="/Users/zed/Desktop/codecrafters-shell-go/.claude/sounds/completion"
FILES=($(ls "$SOUND_DIR"/*.mp3 "$SOUND_DIR"/*.webm 2>/dev/null))
PICK="${FILES[$RANDOM % ${#FILES[@]}]}"
afplay "$PICK"