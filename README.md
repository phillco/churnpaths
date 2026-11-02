# churnpaths

One list of high-churn, regenerable folders (`node_modules`, build outputs,
package caches, scratch job directories), resolved once and applied to every
tool that would otherwise spend time and space on them: Spotlight, Time
Machine and Backblaze. macOS only. Python 3.11+, no dependencies.

Each of those tools has its own exclusion mechanism and its own idea of a path
pattern. Keeping three lists in sync drifts. `churnpaths` keeps one list,
expands it into exact folders, and hands the same answer to each applier.

## The list

Default location `~/.config/churnpaths/churn-paths.txt`. Override with, in
order: `-s/--source`, `$CHURNPATHS_SOURCE`, `source = "..."` in
`~/.config/churnpaths/config.toml` (`$CHURNPATHS_CONFIG` picks another config
file). Config may also set `state` (Spotlight ownership file; or
`$CHURNPATHS_STATE`). See `examples/churn-paths.txt`.

```
path ~/.cache                       exact folder; spaces allowed
glob ~/work/scratch-*               one child component, never recursive
name node_modules ~/projects        discovery up to 4 levels below the root
include ~/.local/state/x/list.txt   generated file of `path` lines
@build-host glob ~/work/job-*       host-scoped (case insensitive, @* = any)
```

- `~` and absolute paths only. `..`, relative paths, `/`, the home directory,
  whole volumes and mount points are rejected.
- Comments start a line with `#`; there are no inline comments.
- `name` walks stay on one device, skip `.git`/`.jj`, never follow symlinks,
  and prune below a match. Python virtualenvs (`.venv`, `venv`) need a
  `pyvenv.cfg` or `bin/activate`; Rust `target` needs build evidence. If the
  walk exceeds its time or directory budget it fails rather than returning a
  partial list.
- `include` files hold exact `path` lines written by another tool. A missing
  file is empty, and invalid or non-folder entries are skipped (reported as
  `skipped`), not fatal.
- Symlinked exact paths or symlinked ancestors fail closed. A symlinked `glob`
  match is reported and skipped.
- An exact `path` that does not exist yet is reported as `missing`; appliers
  keep it excluded so a recreated folder is never briefly backed up.

## Commands

```sh
churnpaths                  # preview Spotlight additions/removals
churnpaths -l -j            # resolve only: {paths, missing, unmatched, skipped}
churnpaths -a               # apply Spotlight Search Privacy entries
churnpaths -c               # exit 1 when Spotlight differs from the list

churnpaths-timemachine apply -n    # preview
churnpaths-timemachine apply       # exclude from Time Machine
churnpaths-timemachine status      # per-folder state
churnpaths-timemachine -c          # exit 1 if any folder is still included

churnpaths-backblaze               # preview (dry run is the default)
churnpaths-backblaze -a            # write the managed block
churnpaths-backblaze -c            # exit 1 on drift
```

All commands take `-h`, `-s FILE` and `-H HOST` (inspect another host's
rules). Fish completions are in `completions/`.

## How the appliers work

`churnpaths -m MECHANISM -j` prints the resolved JSON; an applier calls it and
only deals with exact absolute folders.

- **Spotlight** (`churnpaths` itself). Calls the private Search Privacy backend
  (`_MDCopyExclusionList` / `_MDSetExclusion`), the same one System Settings
  uses. It is not a supported API: it fails closed if symbols or readback
  differ, and never edits plists. Only entries this tool added are recorded in
  its ownership state (`~/.local/state/churnpaths/`), so removing a line
  removes just that entry and pre-existing Search Privacy entries are left
  alone. Every change is logged with the prior list before the call. `--apply`
  requires `-H` to match the local machine.
- **Time Machine** (`churnpaths-timemachine`). Sets the sticky
  "excluded from backup" resource property through the public CoreFoundation
  API (no sudo; not shown in System Settings), then verifies each batch with
  `tmutil isexcluded -X`. It avoids `tmutil addexclusion`, which can wait on a
  Spotlight marker and stall in folders that Spotlight already ignores. Sticky
  exclusions are never removed automatically: undo with
  `tmutil removeexclusion PATH`.
- **Backblaze** (`churnpaths-backblaze`). Maintains one marked block in
  Backblaze's documented editable exclusions XML
  (`/Library/Backblaze.bzpkg/bzdata/bzexcluderules_editable.xml`). Each folder
  becomes a prefix rule ending in `/`, so a similarly named sibling doesn't
  match. All other bytes are preserved. Writes make a private backup first,
  refuse if the file changed since it was read, replace it atomically with the
  original owner/mode, and read it back. The file is usually root-owned; when
  the staged copy can't take the original owner, the tool runs
  `sudo -n chown` on its own temporary file. Removing a list entry removes only
  the tool's own rule. Pass `-x` to point at another file.

## Not included

- Carbon Copy Cloner: it can consume Time Machine exclusion flags (enable
  "Respect macOS exclusions" in each task's filter), so `churnpaths-timemachine`
  already feeds it. No CCC settings are edited.
- Backblaze's `bzcli` native deferral path. It was dropped in favour of the
  documented XML file.

## Safety notes

- Everything is dry-run or read-only unless you pass `apply`/`-a`.
- Excluding a folder from backup means it is not restored. Only list folders
  you can regenerate.
- Review `churnpaths -l -j` before the first apply, especially `name` rules.
- Spotlight and Backblaze changes are reversible by removing the line and
  re-applying; Time Machine's are not (see above).
- Re-run the appliers after recreating folders or creating new matching ones;
  nothing watches the filesystem.

## Tests

```sh
uv run python -m unittest discover -s tests -v
```

Tests use temporary directories and mocks; they touch no live exclusion
settings.

## License

MIT
