# CIRQLE fork

CIRQLE's patches on top of mautrix-whatsapp, kept on the `cirqle` branch
(based on the release tag we run). The image is built by
`.github/workflows/cirqle-image.yml` as `ghcr.io/tesla-prep/mautrix-whatsapp:cirqle-<sha>`
and deployed from `cirqle-agents/deploy/team-messenger/prod/compose.yaml`.

## Patches

- WhatsApp channels can be created: group type `channel` in the provisioning
  API (`POST /_matrix/provision/v3/create_group/channel` with `name`, optional
  `topic`) and the `create-channel <name>` command. Accepts the channel terms
  (notice 20601217) and uses the create queries WhatsApp Web sends today
  (whatsmeow's older one is refused), falling back to whatsmeow's.
- `delete-channel` in a channel room deletes the channel (owner only).
- Photos, videos, audio and files sent to a channel room are uploaded as
  channel media (unencrypted, with the upload's media handle).
- Reactions to channel messages are sent with the message's server ID.
- In channels the login administers, every Matrix user in the room may post
  (power level 0), so relayed staff can write there.

## Updating to a new upstream release

```sh
git fetch upstream --tags
git rebase --onto vX.Y.Z <old tag> cirqle
```
