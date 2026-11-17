# Renaming an account

The account name is the key of everything per account: `config.toml`, the vault entries `secret/<name>` and
`tokencache/<name>`, the state in `state.json`, and the account of every call-log line. `ops.RenameAccount` moves
all of it.

## Order

1. Under the config lock: validate the new name and uniqueness, refuse a name that already has a stored secret
   (an app setup may be creating that account).
2. Under the token-cache lock of both names: copy the vault entries to the new names (they are encrypted together
   with their name, so they are read and written again). An unreadable old token cache is not copied (warning).
3. Rename trigger files and a waiting import hand-over.
4. Save `config.toml`. Anything that failed up to here is put back.
5. Hand the state over, then delete the old vault entries.

## State

Only the engine writes `state.json`. Without an engine, `ops` holds the engine lock and applies the rename to
`state.json` itself; the rename is written first as an *uncommitted* file, which counts only if the config got as
far as having the new name (a crash after the save still finds it). With an engine, the rename is written after the
config was saved as a file of its own, `state.rename.<seq>.json`; the engine claims files in sequence order, loads
the config after the claim, and applies the renames in order, so chains and swaps come out right. A state the
loaded config did not have is kept in memory for an hour so a rename that arrives a moment late still finds it.
A round in flight keeps its runtime record and books its result under the new name.

The call log is not rewritten. `state.json` records `renamed_from`, and reading the log of the new name includes
the entries of the old ones.

## Known limitations

The engine claims the pending renames before it reads the config, but the config file is read without the config
lock, so a rename saved during the read is picked up by the next reload (the in-memory set-aside covers the gap).
An invalid config makes the engine hand the claimed files back and retry on its next reload, and logs that each time.
A token refresh that is in flight under the old name when the rename is applied may write its token cache under
the old name, which is then not used. The shortcut that skips the one-and-a-half second engine-lock probe when the
heartbeat is fresh can, rarely, take a lock held by a status probe for a running engine and hand the rename over
in a file that waits for the next start.
