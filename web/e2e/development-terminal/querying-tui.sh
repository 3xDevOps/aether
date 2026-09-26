# A real busybox TUI: alternate screen, wide/combining Unicode, palette changes,
# raw PTY queries, and persistent process side effects. No API is mocked.
printf 'start\n' >> "$HOME/development-tui-starts"
stty -echo -icanon min 0 time 1
trap 'printf "\033[?1049l"; exit' TERM INT
printf '\033[?1049h\033[2J\033[HShared TUI: 界 λ é\r\n'
printf 'X writes a file; Q queries the terminal\r\n'
printf '\033]4;1;rgb:aa/bb/cc\007\033[31mPalette preserved\033[0m'
printf '\033[?1000h\033[?1006h'
while :; do
  printf '.' >> "$HOME/development-tui-heartbeat"
  key=$(dd bs=1 count=1 2>/dev/null)
  if [ -n "$key" ]; then
    printf '%s' "$key" >> "$HOME/development-tui-input"
    case "$key" in
      X) printf 'effect\n' >> "$HOME/development-tui-effects"
         printf '\033[5;1HSide effect written' ;;
      Q) printf '\033[c\033[6n\033]11;?\007' ;;
    esac
  fi
done
