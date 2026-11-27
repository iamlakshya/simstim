# Change keys

## The launcher

Every command in the launcher has a name. List them, with their keys:

```sh
simstim config keymap
```

Then rebind them in `config.toml` (`simstim config path` prints where it is):

```toml
[keymap]
"session: delete" = "x"          # one key
"session: rename" = ["r", "f2"]  # several
"host: details" = ""             # palette only
```

Every command stays in the command palette (`ctrl+o`) whatever its keys. The full list is in the [keys reference](../reference/keymap.md).

To find a misspelt command name or a key that the launcher cannot read, check the file:

```sh
simstim config check
```

It prints each mistake with its line, such as `~/.config/simstim/config.toml:12: "sesion: delete" in [keymap] is not a launcher command. Did you mean "session: delete"?`.

## While attached

Only two keys are the client's while you are attached to a session; everything else goes to the agent. Change them in `config.toml`:

```toml
[attach]
detach_key = "ctrl+]"
image_paste_key = "ctrl+v"
```

`simstim doctor keys` shows each key as your terminal sends it, so you can pick keys that reach the client.
