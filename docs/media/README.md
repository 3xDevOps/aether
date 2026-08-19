# Demo recording

There is no demo GIF yet. The README carries a placeholder rather than faked
media; this file is the recipe for producing the real one.

Target: `docs/media/demo.gif`, referenced from the README as
`![Aether demo](docs/media/demo.gif)`.

## What to show

One continuous take, roughly 25-40 seconds, no cuts and no narration. The point
is that the work happens somewhere else and comes back as a branch.

1. **Launch** (~5s) - `aether run "add a health check endpoint" --agent claude`
   in a terminal. The run ID prints.
2. **Watch** (~15s) - `aether dash`, then the dashboard: the run card moving
   through the board, the live terminal mirror with the agent actually working,
   the diff timeline picking up changed files.
3. **Steer** (~5s) - optional but the best moment if it fits: `aether inject`
   from the terminal and the colored banner appearing in the dashboard's
   terminal view a second later.
4. **Pull** (~8s) - back in the terminal, `aether pull <run>` and then
   `git diff main...aether/aether/run-...` showing real code.

Cut it before anything hangs. If the agent takes a long pause, record again -
do not speed up the middle, because a sped-up terminal reads as fake.

## Setup

- Terminal at **100x28** or narrower. Anything wider is unreadable once GitHub
  scales the image down.
- Dashboard browser window at **1280x800**, dark theme (it is the default and
  it screenshots better).
- Use a real repo with recognizable file names. Do not use the `fake` harness
  for the published demo - the point is a real agent - but do rehearse the
  timing with it first.
- Clear the board of unrelated runs, and use a display name that is not
  `admin`.
- No tokens on screen. `aether dash` puts one in the URL bar: hide the address
  bar, or crop it out.

## Recording

Terminal-only segments are easiest as an asciinema cast converted to GIF:

```sh
asciinema rec demo.cast --cols 100 --rows 28
agg demo.cast docs/media/demo.gif --font-size 18
```

For the dashboard segment you need a screen recorder. Any of these work:

```sh
# Wayland/X11 screen capture to mp4, then convert
wf-recorder -g "$(slurp)" -f demo.mp4          # Wayland
ffmpeg -f x11grab -framerate 24 -i :0.0 demo.mp4   # X11

ffmpeg -i demo.mp4 -vf "fps=12,scale=1000:-1:flags=lanczos,palettegen" palette.png
ffmpeg -i demo.mp4 -i palette.png -lavfi "fps=12,scale=1000:-1:flags=lanczos [x]; [x][1:v] paletteuse" docs/media/demo.gif
```

## Budget

**Keep the GIF under 5 MB**, ideally under 3. It loads on every visit to the
repo front page. Levers, in the order to reach for them: shorten the take, drop
to 10-12 fps, scale to 1000px wide, reduce the palette
(`palettegen=max_colors=64`).

Commit the GIF, replace the placeholder comment and the "Demo recording wanted"
note in the README with the image, and delete this paragraph's obligation from
the top of this file.
