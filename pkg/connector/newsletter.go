// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2026 CIRQLE
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/provisionutil"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

const (
	// WhatsApp asks an account to accept the channel creation terms once before its first
	// channel (WhatsApp Web AB prop newsletter_creation_tos_id, 2026-10).
	newsletterCreationTOSNoticeID = "20601217"
	// The xwa2_newsletter_delete_v2 mutation, which whatsmeow doesn't wrap yet.
	mutationDeleteNewsletter = "30062808666639665"
)

// xwa2_newsletter_create query IDs taking input {name, description}, newest first.
// WhatsApp answers whatsmeow's older query (newsletter_input) with "400 Bad Request".
var mutationCreateNewsletterIDs = []string{"25149874324715067", "8823471724422422"}

type respCreateNewsletter struct {
	Newsletter *types.NewsletterMetadata `json:"xwa2_newsletter_create"`
}

// Notices around channels: consumer 20601216, creation 20601217, admin invite 20601218.
var newsletterTOSNoticeIDs = []string{"20601216", "20601217", "20601218"}

func tosNoticeNodes(ids []string) []waBinary.Node {
	nodes := make([]waBinary.Node, len(ids))
	for i, id := range ids {
		nodes[i] = waBinary.Node{Tag: "notice", Attrs: waBinary.Attrs{"id": id}}
	}
	return nodes
}

// acceptChannelTerms accepts the channel creation terms the way WhatsApp Web does,
// and the older way whatsmeow documents if that is refused. It logs the terms' state.
func (wa *WhatsAppClient) acceptChannelTerms(ctx context.Context) error {
	log := zerolog.Ctx(ctx)
	internals := wa.Client.DangerousInternals()
	state, err := internals.SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "tos",
		Type:      whatsmeow.DangerousInfoQueryType("get"),
		To:        types.ServerJID,
		Content:   []waBinary.Node{{Tag: "request", Content: tosNoticeNodes(newsletterTOSNoticeIDs)}},
	})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to read channel terms state")
	} else {
		log.Info().Str("state", state.String()).Msg("Channel terms state")
	}
	_, err = internals.SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "tos",
		Type:      whatsmeow.DangerousInfoQueryType("set"),
		To:        types.ServerJID,
		Content: []waBinary.Node{{
			Tag:     "request",
			Attrs:   waBinary.Attrs{"type": "session_update"},
			Content: tosNoticeNodes([]string{newsletterCreationTOSNoticeID}),
		}},
	})
	if err == nil {
		return nil
	}
	log.Warn().Err(err).Msg("Failed to accept channel terms with a session update, trying the older request")
	return wa.Client.AcceptTOSNotice(ctx, newsletterCreationTOSNoticeID, "5")
}

// createNewsletter tries the current create queries first and whatsmeow's own last.
// The current queries set no picture; set it from the phone.
func (wa *WhatsAppClient) createNewsletter(ctx context.Context, req whatsmeow.CreateNewsletterParams) (*types.NewsletterMetadata, error) {
	input := map[string]any{"name": req.Name, "description": nil}
	if req.Description != "" {
		input["description"] = req.Description
	}
	for _, queryID := range mutationCreateNewsletterIDs {
		raw, err := wa.Client.DangerousInternals().SendMexIQ(ctx, queryID, map[string]any{"input": input})
		if err == nil {
			var resp respCreateNewsletter
			if err = json.Unmarshal(raw, &resp); err == nil && resp.Newsletter != nil {
				return resp.Newsletter, nil
			} else if err == nil {
				err = errors.New("empty response")
			}
		}
		zerolog.Ctx(ctx).Warn().Err(err).Str("query_id", queryID).Msg("Channel create query failed, trying the next one")
	}
	return wa.Client.CreateNewsletter(ctx, req)
}

// createChannel creates a WhatsApp channel owned by this login. The caller
// (provisionutil.CreateGroup) creates the portal room from the returned info.
func (wa *WhatsAppClient) createChannel(ctx context.Context, params *bridgev2.GroupCreateParams) (*bridgev2.CreateChatResponse, error) {
	if wa.disableNewsletter {
		return nil, bridgev2.RespError(mautrix.MForbidden.WithMessage("Channels are not available for this login"))
	} else if params.RoomID != "" {
		return nil, bridgev2.RespError(mautrix.MInvalidParam.WithMessage("Channels can't be created from an existing room"))
	}
	req := whatsmeow.CreateNewsletterParams{
		Name:        ptr.Val(params.Name).Name,
		Description: ptr.Val(params.Topic).Topic,
	}
	if params.Avatar != nil && params.Avatar.URL != "" {
		avatarBytes, err := wa.Main.Bridge.Bot.DownloadMedia(ctx, params.Avatar.URL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to download avatar: %w", err)
		}
		req.Picture, err = convertRoomAvatar(avatarBytes)
		if err != nil {
			return nil, err
		}
	}
	if err := wa.acceptChannelTerms(ctx); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to accept channel terms, trying to create the channel anyway")
	}
	info, err := wa.createNewsletter(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create channel: %w", err)
	} else if info == nil || info.ID.IsEmpty() {
		return nil, errors.New("failed to create channel: empty response")
	}
	if info.ViewerMeta == nil {
		info.ViewerMeta = &types.NewsletterViewerMetadata{Role: types.NewsletterRoleOwner, Mute: types.NewsletterMuteOff}
	}
	zerolog.Ctx(ctx).Info().Stringer("channel_jid", info.ID).Msg("Created WhatsApp channel")
	return &bridgev2.CreateChatResponse{
		PortalKey:  wa.makeWAPortalKey(info.ID),
		PortalInfo: wa.wrapNewsletterInfo(ctx, info),
	}, nil
}

// sendNewsletterReaction reacts to a channel message, which channels address by its server ID.
func (wa *WhatsAppClient) sendNewsletterReaction(ctx context.Context, jid types.JID, target *database.Message, emoji string) (*database.Reaction, error) {
	serverID := target.Metadata.(*waid.MessageMetadata).NewsletterServerID
	if serverID == 0 {
		return nil, bridgev2.WrapErrorInStatus(errors.New("can't react to this channel message")).
			WithErrorAsMessage().WithIsCertain(true).WithSendNotice(false).WithErrorReason(event.MessageStatusUnsupported)
	}
	err := wa.Client.NewsletterSendReaction(ctx, jid, serverID, emoji, "")
	if err != nil {
		return nil, err
	}
	return &database.Reaction{
		Metadata: &waid.ReactionMetadata{
			SenderDeviceID:     wa.JID.Device,
			NewsletterServerID: serverID,
		},
	}, nil
}

var cmdCreateChannel = &commands.FullHandler{
	Func: fnCreateChannel,
	Name: "create-channel",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Create a new WhatsApp channel",
		Args:        "<_name_>",
	},
	RequiresLogin: true,
}

func fnCreateChannel(ce *commands.Event) {
	name := strings.TrimSpace(ce.RawArgs)
	if name == "" {
		ce.Reply("**Usage:** `$cmdprefix create-channel <name>`")
		return
	}
	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("Login not found")
		return
	}
	resp, err := provisionutil.CreateGroup(ce.Ctx, login, &bridgev2.GroupCreateParams{
		Type: "channel",
		Name: &event.RoomNameEventContent{Name: name},
	})
	if err != nil {
		ce.Reply("Failed to create channel: %v", err)
		return
	}
	ce.Reply("Created channel [%s](%s)", name, resp.MXID.URI().MatrixToURL())
}

var cmdDeleteChannel = &commands.FullHandler{
	Func: fnDeleteChannel,
	Name: "delete-channel",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Delete the WhatsApp channel of this room for all its followers. Only the channel owner can do it.",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

func fnDeleteChannel(ce *commands.Event) {
	jid, err := waid.ParsePortalID(ce.Portal.ID)
	if err != nil || jid.Server != types.NewsletterServer {
		ce.Reply("This room is not a WhatsApp channel")
		return
	}
	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("Login not found")
		return
	}
	wa := login.Client.(*WhatsAppClient)
	_, err = wa.Client.DangerousInternals().SendMexIQ(ce.Ctx, mutationDeleteNewsletter, map[string]any{
		"newsletter_id": jid.String(),
	})
	if err != nil {
		ce.Reply("Failed to delete channel: %v", err)
		return
	}
	ce.Log.Info().Stringer("channel_jid", jid).Msg("Deleted WhatsApp channel")
	ce.Reply("Deleted channel `%s`", jid)
}
